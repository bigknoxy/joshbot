package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/mcp"
	"github.com/charmbracelet/log"
)

// mcpNamespacePrefix is prepended to every MCP tool name. Namespacing is a
// security control, not cosmetics: a malicious or careless server must not be
// able to register a tool called "shell" or "filesystem" and shadow the
// built-in. Because the built-ins never carry this prefix, and Registry.Register
// refuses a duplicate name, an MCP tool can only ever collide with another MCP
// tool from the same server (which cannot happen — a server's tool names are
// unique), never with a built-in.
const mcpNamespacePrefix = "mcp__"

// mcpCallTimeout bounds a single tools/call. A server that hangs fails this call
// rather than hanging the agent loop.
const mcpCallTimeout = 60 * time.Second

// mcpConnectTimeout bounds the initialize handshake and tools/list during
// registration, so one slow server cannot stall startup indefinitely.
const mcpConnectTimeout = 15 * time.Second

// mcpMaxOutputChars caps what an MCP tool result contributes to the agent's
// context, matching the shell and filesystem tools' default. An MCP server is
// third-party code returning text straight into the prompt, so an uncapped
// result is both a context blow-up and a cost blow-up.
const mcpMaxOutputChars = 4000

// rawSchemaProvider lets a tool supply a JSON Schema for its parameters
// directly, bypassing GenerateSchema. MCP tools carry a full inputSchema from
// the server that would lose fidelity if flattened into []Parameter.
type rawSchemaProvider interface {
	RawSchema() json.RawMessage
}

// mcpTool adapts a single tool discovered on an MCP server to the Tool
// interface, so the ReAct loop dispatches it like any built-in.
type mcpTool struct {
	client   *mcp.Client
	toolName string // original name on the server
	fullName string // namespaced name registered with joshbot
	desc     string
	schema   json.RawMessage // server-provided JSON Schema (may be nil)
	timeout  time.Duration
	maxChars int // cap on returned output; 0 means mcpMaxOutputChars
}

// Name returns the namespaced tool name.
func (t *mcpTool) Name() string { return t.fullName }

// Description returns the server-provided description.
func (t *mcpTool) Description() string { return t.desc }

// Parameters returns an empty slice: the real schema is served via RawSchema so
// nested object schemas survive intact. GenerateSchema is not consulted for
// this tool.
func (t *mcpTool) Parameters() []Parameter { return nil }

// RawSchema returns the server's inputSchema, or nil if it had none.
func (t *mcpTool) RawSchema() json.RawMessage { return t.schema }

// Execute forwards the call to the MCP server with a bounded context.
func (t *mcpTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	base, ok := ctx.(context.Context)
	if !ok || base == nil {
		base = context.Background()
	}
	callCtx, cancel := context.WithTimeout(base, t.timeout)
	defer cancel()

	out, err := t.client.CallTool(callCtx, t.toolName, args)
	if err != nil {
		return ToolResult{Error: err}
	}
	return ToolResult{Output: truncateMCPOutput(out, t.maxChars)}
}

// truncateMCPOutput applies the same truncation convention the built-in tools
// use, so a verbose server reads the same way as a verbose command.
func truncateMCPOutput(out string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = mcpMaxOutputChars
	}
	if len(out) <= maxChars {
		return out
	}
	return out[:maxChars] + fmt.Sprintf("\n... (truncated, %d chars total)", len(out))
}

// mcpServersFromConfig converts enabled MCP server config into mcp.Server specs.
// Servers with no command or with Enabled unset are skipped.
func mcpServersFromConfig(cfg config.MCPConfig) []mcp.Server {
	var servers []mcp.Server
	for name, sc := range cfg.Servers {
		if !sc.Enabled || sc.Command == "" {
			continue
		}
		var env []string
		for k, v := range sc.Env {
			env = append(env, k+"="+v)
		}
		servers = append(servers, mcp.Server{
			Name:    name,
			Command: sc.Command,
			Args:    sc.Args,
			Env:     env,
		})
	}
	return servers
}

// RegisterMCPTools connects to every enabled MCP server in cfg, enumerates its
// tools, and registers each under a namespaced name (mcp__<server>__<tool>).
//
// It returns a *mcp.Manager that owns the spawned processes; the caller MUST
// call its Close() at shutdown to reap them. When no servers are enabled it
// returns nil and registers nothing — callers may call Close on a nil manager
// safely only if they nil-check; the returned value is nil precisely so an
// unused import path stays cheap.
//
// A server that fails to start, handshake, or list tools is logged and skipped;
// it never aborts registration of the others or of the built-in tools. This is
// the lazy, fail-soft posture: MCP is additive and must never break the agent.
func RegisterMCPTools(ctx context.Context, reg *Registry, cfg config.MCPConfig) *mcp.Manager {
	servers := mcpServersFromConfig(cfg)
	if len(servers) == 0 {
		return nil
	}

	mgr := mcp.NewManager(servers)
	for _, client := range mgr.Clients() {
		registerOneMCPServer(ctx, reg, client)
	}
	return mgr
}

// registerOneMCPServer connects one client and registers its tools. Failures
// are logged and swallowed so one bad server does not sink the rest.
func registerOneMCPServer(ctx context.Context, reg *Registry, client *mcp.Client) {
	connectCtx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	defer cancel()

	if err := client.Connect(connectCtx); err != nil {
		log.Warn("mcp: failed to connect server, skipping", "server", client.Name(), "error", err)
		return
	}

	infos, err := client.ListTools(connectCtx)
	if err != nil {
		log.Warn("mcp: failed to list tools, skipping server", "server", client.Name(), "error", err)
		return
	}

	registered := 0
	for _, info := range infos {
		full := fmt.Sprintf("%s%s__%s", mcpNamespacePrefix, client.Name(), info.Name)
		tool := &mcpTool{
			client:   client,
			toolName: info.Name,
			fullName: full,
			desc:     info.Description,
			schema:   info.InputSchema,
			timeout:  mcpCallTimeout,
			maxChars: mcpMaxOutputChars,
		}
		if err := reg.Register(tool); err != nil {
			// A namespaced MCP name cannot collide with a built-in; a collision
			// here means two servers share a name, which is an operator config
			// error worth surfacing.
			log.Warn("mcp: failed to register tool", "tool", full, "error", err)
			continue
		}
		registered++
	}
	log.Info("mcp: registered server tools", "server", client.Name(), "count", registered)
}
