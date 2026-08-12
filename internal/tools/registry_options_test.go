package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/mcp"
)

// --- approval Decision ---

func TestDecisionString(t *testing.T) {
	// Deny is the zero value on purpose, and String must agree: a mislabelled
	// zero value turns "nobody answered" into an approval in every log line an
	// operator would use to audit the gate.
	if (Decision(0)).String() != "deny" {
		t.Errorf("zero Decision String() = %q, want deny", Decision(0).String())
	}
	if Deny.String() != "deny" {
		t.Errorf("Deny.String() = %q, want deny", Deny.String())
	}
	if Approve.String() != "approve" {
		t.Errorf("Approve.String() = %q, want approve", Approve.String())
	}
	// Anything that is not Approve must read as a denial, never as an unknown
	// value that a log reader could mistake for success.
	if (Decision(99)).String() != "deny" {
		t.Errorf("unknown Decision String() = %q, want deny", Decision(99).String())
	}
}

// --- Registry options ---

func shellToolFrom(t *testing.T, reg *Registry) *ShellTool {
	t.Helper()
	tool, ok := reg.Get("shell")
	if !ok {
		t.Fatal("registry has no shell tool")
	}
	sh, ok := tool.(*ShellTool)
	if !ok {
		t.Fatalf("shell tool is %T, want *ShellTool", tool)
	}
	return sh
}

// The options are the only way an operator's shell_sandbox / shell_approval
// config reaches the shell tool. An option that is accepted but not applied
// leaves the gate advertised and off — the worst possible failure mode.
func TestRegistryOptionsReachTheShellTool(t *testing.T) {
	ws := t.TempDir()

	t.Run("defaults are off", func(t *testing.T) {
		sh := shellToolFrom(t, RegistryWithDefaults(ws, true, 5, 5, nil, []string{"echo"}, nil, nil))
		if sh.sandbox != SandboxOff {
			t.Errorf("sandbox = %q, want off by default", sh.sandbox)
		}
		if sh.approval != ApprovalOff {
			t.Errorf("approval = %q, want off by default", sh.approval)
		}
		if sh.allowNetwork {
			t.Error("allowNetwork must default to false")
		}
	})

	t.Run("WithShellApproval is applied", func(t *testing.T) {
		sh := shellToolFrom(t, RegistryWithDefaults(ws, true, 5, 5, nil, []string{"echo"}, nil, nil,
			WithShellApproval(ApprovalAlways)))
		if sh.approval != ApprovalAlways {
			t.Errorf("approval = %q, want %q — the configured gate never reached the tool", sh.approval, ApprovalAlways)
		}
	})

	t.Run("WithShellSandbox carries both mode and network flag", func(t *testing.T) {
		sh := shellToolFrom(t, RegistryWithDefaults(ws, true, 5, 5, nil, []string{"echo"}, nil, nil,
			WithShellSandbox(SandboxWorkspace, true)))
		if sh.sandbox != SandboxWorkspace {
			t.Errorf("sandbox = %q, want %q", sh.sandbox, SandboxWorkspace)
		}
		if !sh.allowNetwork {
			t.Error("allowNetwork = false, want the configured true")
		}
	})

	t.Run("options compose", func(t *testing.T) {
		sh := shellToolFrom(t, RegistryWithDefaults(ws, true, 5, 5, nil, []string{"echo"}, nil, nil,
			WithShellSandbox(SandboxWorkspace, false),
			WithShellApproval(ApprovalInteractive)))
		if sh.sandbox != SandboxWorkspace || sh.approval != ApprovalInteractive || sh.allowNetwork {
			t.Errorf("composed options lost a setting: sandbox=%q approval=%q network=%v",
				sh.sandbox, sh.approval, sh.allowNetwork)
		}
	})
}

// An approval gate installed on the shell tool must also govern the async path;
// otherwise async=true is a one-flag bypass. This exercises it through the
// registry, so a registry that silently dropped the option would be caught too.
func TestRegistryApprovalGateCoversAsyncExecution(t *testing.T) {
	reg := RegistryWithDefaults(t.TempDir(), true, 5, 5, nil, nil, nil, nil,
		WithShellApproval(ApprovalAlways))
	sh := shellToolFrom(t, reg)

	// No approver on the context: ApproverFromContext hands back DenyAll.
	res := sh.Execute(context.Background(), map[string]any{"command": "echo hi", "async": true})
	if res.Error == nil {
		t.Fatalf("an unattended async command must be denied, got output %q", res.Output)
	}
	if !strings.Contains(res.Error.Error(), "not approved") {
		t.Errorf("error = %q, want it to say the command was not approved", res.Error)
	}
	// The refusal must be immediate and self-explaining: a background turn has
	// nobody to ask, so it is denied rather than left blocking.
	if !strings.Contains(res.Error.Error(), "no approver is attached") {
		t.Errorf("error = %q, want it to explain that no approver was attached", res.Error)
	}
}

// The web aliases must all be registered, because they are what the model is
// actually offered; a dropped alias is a capability that silently disappears.
func TestRegistryWithDefaultsRegistersWebAliases(t *testing.T) {
	reg := RegistryWithDefaults(t.TempDir(), true, 5, 5, nil, nil, nil, nil)
	for _, name := range []string{"web", "web_search", "web_fetch", "web_code", "web_company", "web_research"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q is not registered", name)
		}
	}
	// A scheduler that is not running must not be advertised.
	if _, ok := reg.Get("cron"); ok {
		t.Error("the cron tool must not be registered without a cron.Service")
	}
	// No message sender means no message tools.
	for _, name := range []string{"message", "channel_message"} {
		if _, ok := reg.Get(name); ok {
			t.Errorf("tool %q must not be registered without a MessageSender", name)
		}
	}
}

// --- MCP registration ---

// A server that cannot be started must be logged and skipped, not fatal: MCP is
// additive and a broken server must never take the agent's built-in tools down
// with it.
func TestRegisterOneMCPServerFailSoftOnUnstartableServer(t *testing.T) {
	reg := NewRegistry()
	client := mcp.NewClient(mcp.Server{
		Name:    "broken",
		Command: "joshbot-no-such-binary-exists-here",
	})

	registerOneMCPServer(context.Background(), reg, client, nil)

	for _, name := range reg.List() {
		if strings.HasPrefix(name, mcpNamespacePrefix) {
			t.Errorf("a server that never connected registered tool %q", name)
		}
	}
}

// RegisterMCPTools must return nil (and register nothing) when no server is
// enabled, so callers can skip Close and no MCP tool appears from thin air.
func TestRegisterMCPToolsWithOnlyDisabledServers(t *testing.T) {
	reg := NewRegistry()
	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"off":         {Enabled: false, Command: "echo"},
		"no-command":  {Enabled: true, Command: ""},
		"also-absent": {Enabled: false},
	}}
	if mgr := RegisterMCPTools(context.Background(), reg, cfg, nil); mgr != nil {
		t.Errorf("expected a nil manager when nothing is enabled, got %T", mgr)
	}
	if len(reg.List()) != 0 {
		t.Errorf("registry gained %d tools from disabled servers", len(reg.List()))
	}
}

// An enabled-but-unstartable server still yields a manager (its processes must
// be reaped) and still registers nothing.
func TestRegisterMCPToolsEnabledButBrokenServer(t *testing.T) {
	reg := NewRegistry()
	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"broken": {Enabled: true, Command: "joshbot-no-such-binary-exists-here"},
	}}
	mgr := RegisterMCPTools(context.Background(), reg, cfg, nil)
	if mgr == nil {
		t.Fatal("an enabled server must yield a manager the caller can Close")
	}
	defer mgr.Close()

	if len(reg.List()) != 0 {
		t.Errorf("a server that failed to start registered %d tools", len(reg.List()))
	}
}

// --- SubagentConfigTool ---

func newTestSubagentManager(t *testing.T, dir string) *SubagentConfigManager {
	t.Helper()
	mgr, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager(%q): %v", dir, err)
	}
	return mgr
}

func TestSubagentConfigTool_Metadata(t *testing.T) {
	tool := NewSubagentConfigTool(nil)
	if tool.Name() != "subagent_config" {
		t.Errorf("Name() = %q, want subagent_config", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	params := tool.Parameters()
	if len(params) != 3 {
		t.Fatalf("Parameters() returned %d params, want 3", len(params))
	}
	if params[0].Name != "operation" || !params[0].Required {
		t.Errorf("first parameter = %+v, want a required 'operation'", params[0])
	}
	// The enum is the model's menu; each entry must be a real branch.
	mgr := newTestSubagentManager(t, t.TempDir())
	real := NewSubagentConfigTool(mgr)
	for _, op := range params[0].Enum {
		res := real.Execute(nil, map[string]any{"operation": op})
		if res.Error != nil && strings.Contains(res.Error.Error(), "unknown operation") {
			t.Errorf("operation %q is advertised but Execute rejects it as unknown", op)
		}
	}
}

func TestSubagentConfigTool_ExecuteErrors(t *testing.T) {
	tests := []struct {
		name    string
		mgr     *SubagentConfigManager
		args    map[string]any
		wantErr string
	}{
		{"nil manager", nil, map[string]any{"operation": "list"}, "not configured"},
		{"missing operation", newTestSubagentManager(t, t.TempDir()), map[string]any{}, "'operation' is required"},
		{"unknown operation", newTestSubagentManager(t, t.TempDir()), map[string]any{"operation": "nuke"}, "unknown operation"},
		{"get without name", newTestSubagentManager(t, t.TempDir()), map[string]any{"operation": "get"}, "'name' is required"},
		{"get unknown name", newTestSubagentManager(t, t.TempDir()), map[string]any{"operation": "get", "name": "ghost"}, "not found"},
		{"save without name", newTestSubagentManager(t, t.TempDir()), map[string]any{"operation": "save"}, "'name' is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := NewSubagentConfigTool(tt.mgr).Execute(nil, tt.args)
			if res.Error == nil {
				t.Fatalf("expected an error, got output %q", res.Output)
			}
			if !strings.Contains(res.Error.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", res.Error, tt.wantErr)
			}
		})
	}
}

// handleSave decodes a JSON-shaped config object by hand. Every field it claims
// to accept in its parameter description must actually round-trip through save
// and get — a field silently dropped here is a config the operator believes
// they set.
func TestSubagentConfigTool_SaveRoundTripsEveryField(t *testing.T) {
	mgr := newTestSubagentManager(t, t.TempDir())
	tool := NewSubagentConfigTool(mgr)

	res := tool.Execute(nil, map[string]any{
		"operation": "save",
		"name":      "researcher",
		"config": map[string]any{
			"description":   "does research",
			"model":         "vendor/model",
			"temperature":   0.25,
			"max_tokens":    float64(1234), // JSON numbers arrive as float64
			"system_prompt": "be brief",
			"tools":         []any{"web_search", "filesystem", 42},
			"skills":        []any{"skill-a"},
			"tags":          []any{"tag-a", "tag-b"},
		},
	})
	if res.Error != nil {
		t.Fatalf("save: %v", res.Error)
	}

	cfg, ok := mgr.Get("researcher")
	if !ok {
		t.Fatal("saved config is not retrievable")
	}
	if cfg.Description != "does research" {
		t.Errorf("Description = %q", cfg.Description)
	}
	if cfg.Model != "vendor/model" {
		t.Errorf("Model = %q", cfg.Model)
	}
	if cfg.Temperature != 0.25 {
		t.Errorf("Temperature = %v, want 0.25", cfg.Temperature)
	}
	if cfg.MaxTokens != 1234 {
		t.Errorf("MaxTokens = %d, want 1234 (a JSON float64 must be truncated to int)", cfg.MaxTokens)
	}
	if cfg.SystemPrompt != "be brief" {
		t.Errorf("SystemPrompt = %q", cfg.SystemPrompt)
	}
	// The non-string element must be dropped, not stringified into a tool name
	// that does not exist.
	if len(cfg.Tools) != 2 || cfg.Tools[0] != "web_search" || cfg.Tools[1] != "filesystem" {
		t.Errorf("Tools = %v, want only the string entries", cfg.Tools)
	}
	if len(cfg.Skills) != 1 || cfg.Skills[0] != "skill-a" {
		t.Errorf("Skills = %v", cfg.Skills)
	}
	if len(cfg.Tags) != 2 {
		t.Errorf("Tags = %v", cfg.Tags)
	}

	// And the get view must show what was saved.
	out := tool.Execute(nil, map[string]any{"operation": "get", "name": "researcher"}).Output
	for _, want := range []string{"researcher", "does research", "be brief", "vendor/model", "1234", "web_search", "skill-a", "tag-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output missing %q:\n%s", want, out)
		}
	}
}

func TestSubagentConfigTool_SaveIgnoresMistypedConfig(t *testing.T) {
	mgr := newTestSubagentManager(t, t.TempDir())
	tool := NewSubagentConfigTool(mgr)

	// A model sending a string where an object belongs must not crash the tool.
	res := tool.Execute(nil, map[string]any{"operation": "save", "name": "plain", "config": "not-an-object"})
	if res.Error != nil {
		t.Fatalf("a mistyped config must be ignored, not fatal: %v", res.Error)
	}
	cfg, ok := mgr.Get("plain")
	if !ok {
		t.Fatal("config was not saved")
	}
	if cfg.Name != "plain" {
		t.Errorf("Name = %q, want plain", cfg.Name)
	}
}

func TestSubagentConfigTool_ListAndDiscover(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestSubagentManager(t, dir)
	tool := NewSubagentConfigTool(mgr)

	empty := tool.Execute(nil, map[string]any{"operation": "list"})
	if !strings.Contains(empty.Output, "No subagent configs found") {
		t.Errorf("empty list output = %q, want the explicit empty message", empty.Output)
	}

	for _, name := range []string{"zulu", "alpha"} {
		res := tool.Execute(nil, map[string]any{
			"operation": "save",
			"name":      name,
			"config":    map[string]any{"description": "d-" + name, "tools": []any{"shell"}, "skills": []any{"s"}, "tags": []any{"t"}},
		})
		if res.Error != nil {
			t.Fatalf("save %s: %v", name, res.Error)
		}
	}

	out := tool.Execute(nil, map[string]any{"operation": "list"}).Output
	if !strings.Contains(out, "(2)") {
		t.Errorf("list should state the count, got:\n%s", out)
	}
	iAlpha, iZulu := strings.Index(out, "alpha"), strings.Index(out, "zulu")
	if iAlpha == -1 || iZulu == -1 || iAlpha > iZulu {
		t.Errorf("list must be sorted by name, got:\n%s", out)
	}

	// discover re-reads the directory the saves wrote to, so it must find them.
	res := tool.Execute(nil, map[string]any{"operation": "discover"})
	if res.Error != nil {
		t.Fatalf("discover: %v", res.Error)
	}
	if !strings.Contains(res.Output, "2 subagent config") {
		t.Errorf("discover output = %q, want it to report the 2 configs on disk", res.Output)
	}
}
