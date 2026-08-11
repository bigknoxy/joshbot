package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// mcpMaxDescriptionChars caps one server-supplied tool description. A
// description is prompt text the server writes directly into the model's
// context on every single request, so an unbounded one is a permanent context
// tax as well as an obvious place to hide a wall of injected instructions.
const mcpMaxDescriptionChars = 1024

// promptEnvelopeTags are the XML-ish tags joshbot's own system prompt uses to
// delimit its sections (internal/agent/context.go and internal/agent/agent.go). A
// server-supplied description containing "</skills>" would otherwise close a
// section joshbot opened and let the rest of the description read as joshbot's
// own instructions rather than as a tool's description.
//
// The list is matched narrowly, on these names only. Escaping every "<" would
// mangle the placeholder syntax ("<path>", "<name>") that real MCP servers use
// throughout their descriptions, for no gain: an unknown tag is inert text.
//
// TestPromptEnvelopeTagsCoverAgentContext pins this against the actual prompt
// builder, so adding a section there without adding it here fails the build.
var promptEnvelopeTags = []string{
	"memory",
	"skills",
	"current_time",
	"conversation_context",
	"personality",
}

// sanitizeMCPDescription makes a server-supplied description safe to place in
// the system prompt: envelope tags are defanged and the result is length
// bounded.
//
// Defanging replaces the angle brackets rather than deleting the text, so an
// operator reading `joshbot mcp list` still sees exactly what the server sent
// and can recognise an injection attempt for what it is.
func sanitizeMCPDescription(desc string) string {
	for _, tag := range promptEnvelopeTags {
		for _, form := range []string{"<" + tag + ">", "</" + tag + ">"} {
			// Case-insensitively replace: XML tags are conventionally lower
			// case here, but a parser-shaped injection would not bother.
			for {
				i := indexFold(desc, form)
				if i < 0 {
					break
				}
				desc = desc[:i] + "(" + desc[i+1:i+len(form)-1] + ")" + desc[i+len(form):]
			}
		}
	}
	if len(desc) > mcpMaxDescriptionChars {
		desc = desc[:mcpMaxDescriptionChars] + "... (truncated)"
	}
	return desc
}

// indexFold is strings.Index with ASCII case folding. The tag names are ASCII
// by construction, so a full Unicode case fold would buy nothing.
func indexFold(s, substr string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(substr))
}

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
// A server whose manifest the operator has not approved is connected (that is
// the only way to learn what it advertises) but none of its tools are
// registered: nothing it says reaches the model. See internal/mcp/trust.go for
// why the gate is on the manifest rather than on execution.
func RegisterMCPTools(ctx context.Context, reg *Registry, cfg config.MCPConfig, trust *mcp.TrustStore) *mcp.Manager {
	servers := mcpServersFromConfig(cfg)
	if len(servers) == 0 {
		return nil
	}

	mgr := mcp.NewManager(servers)
	for _, client := range mgr.Clients() {
		registerOneMCPServer(ctx, reg, client, trust)
	}
	return mgr
}

// registerOneMCPServer connects one client and registers its tools. Failures
// are logged and swallowed so one bad server does not sink the rest.
func registerOneMCPServer(ctx context.Context, reg *Registry, client *mcp.Client, trust *mcp.TrustStore) {
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

	registerManifest(reg, client, infos, trust)
}

// registerManifest applies the trust gate to one server's advertised manifest
// and registers what survives. It is split out from registerOneMCPServer so the
// gate can be exercised without spawning a process: the decision is a security
// boundary and must be tested directly, not only through a live server.
func registerManifest(reg *Registry, client *mcp.Client, infos []mcp.ToolInfo, trust *mcp.TrustStore) int {
	// The gate. A nil store is untrusted, not trusted: a caller that forgot to
	// load one must lose MCP tools, never gain unapproved ones.
	if !trust.IsTrusted(client.Name(), infos) {
		log.Warn("mcp: server is not approved, its tools are not being used",
			"server", client.Name(),
			"tools", len(infos),
			"hint", fmt.Sprintf("review then run: joshbot mcp trust %s", client.Name()))
		return 0
	}

	registered := 0
	for _, info := range infos {
		full := fmt.Sprintf("%s%s__%s", mcpNamespacePrefix, client.Name(), info.Name)
		tool := &mcpTool{
			client:   client,
			toolName: info.Name,
			fullName: full,
			desc:     sanitizeMCPDescription(info.Description),
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
	return registered
}
