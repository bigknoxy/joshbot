package main

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// setupComponents is the single place every long-lived entry point goes
// through to turn a config into a running agent. The behaviours pinned here
// are the ones that fail *silently* if they regress: a config with nothing
// usable in it must be refused at startup rather than at the first Chat(), and
// an unrecognised containment or approval value must be a startup error rather
// than a quiet fallback to "off" — an operator who typed "interactve" would
// otherwise believe every command was being confirmed while none of them were.

// setupConfig builds a config anchored in an isolated home. It deliberately
// starts from Defaults() so the test exercises the same field set a real
// install has.
func setupConfig(t *testing.T) *config.Config {
	t.Helper()

	home := withTempHome(t)
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = home + "/workspace"
	cfg.Agents.Defaults.Model = "test-model"
	// Keep MCP out of it: registerMCPServers spawns processes.
	cfg.MCP.Servers = nil
	// setupComponents starts background services that write into the
	// workspace — the consolidator runs its first pass immediately — and the
	// workspace lives under the temp home. Registering the stop here, after
	// withTempHome, puts it ahead of the TempDir removal in cleanup order, so
	// no goroutine is still writing while the directory is being deleted.
	t.Cleanup(stopBackgroundServices)
	return cfg
}

// A legacy provider map that produces zero usable providers must fail at
// startup with an actionable message, not defer to an opaque error on the
// first Chat() (issue #71).
func TestSetupComponentsRefusesConfigWithNoUsableProviders(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		// Present but disabled, and present but keyless: both unusable.
		"openrouter": {Enabled: false, APIKey: "sk-disabled"},
		"groq":       {Enabled: true},
	}

	_, _, _, _, _, _, err := setupComponents(cfg)
	if err == nil {
		t.Fatal("expected setupComponents to refuse a config with no usable providers")
	}
	if !strings.Contains(err.Error(), "groq") {
		t.Errorf("error should name the enabled-but-keyless provider, got: %v", err)
	}
}

// An empty provider map points at onboarding rather than listing nothing.
func TestSetupComponentsEmptyProvidersPointsAtOnboard(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{}

	_, _, _, _, _, _, err := setupComponents(cfg)
	if err == nil {
		t.Fatal("expected an error for an empty provider map")
	}
	if !strings.Contains(err.Error(), "onboard") {
		t.Errorf("error should tell the operator to run onboard, got: %v", err)
	}
	if got := codeForError(err); got != exitAuth {
		t.Errorf("exit code = %d, want exitAuth (%d)", got, exitAuth)
	}
}

// The model-centric branch registers each configured model on the
// multi-provider. It does not reach the legacy "no providers" guard, so this
// path needs its own coverage.
func TestSetupComponentsModelCentricConfigRegistersModels(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = nil
	cfg.ModelsConfig.Models = []config.ModelConfig{{
		Name:   "primary",
		Model:  "openrouter/openai/gpt-4o-mini",
		APIKey: "sk-test",
	}}

	_, provider, _, agentInstance, reg, _, err := setupComponents(cfg)
	if err != nil {
		t.Fatalf("setupComponents: %v", err)
	}
	if provider == nil || agentInstance == nil || reg == nil {
		t.Fatal("setupComponents returned nil components on the model-centric path")
	}
	if got := provider.Name(); got == "" {
		t.Error("multi-provider reported an empty name; no model was registered")
	}
}

// An unrecognised tools.shell_sandbox value must abort startup. Falling back to
// "off" would tell an operator their commands were contained when they were
// not.
func TestSetupComponentsUnknownSandboxModeIsAStartupError(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test"},
	}
	cfg.Tools.ShellSandbox = "workspce"

	_, _, _, _, _, _, err := setupComponents(cfg)
	if err == nil {
		t.Fatal("expected an unknown shell_sandbox value to be a startup error")
	}
	if !strings.Contains(err.Error(), "shell_sandbox") || !strings.Contains(err.Error(), "workspce") {
		t.Errorf("error should name the key and the bad value, got: %v", err)
	}
}

// Same rule for the approval gate.
func TestSetupComponentsUnknownApprovalModeIsAStartupError(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test"},
	}
	cfg.Tools.ShellApproval = "interactve"

	_, _, _, _, _, _, err := setupComponents(cfg)
	if err == nil {
		t.Fatal("expected an unknown shell_approval value to be a startup error")
	}
	if !strings.Contains(err.Error(), "shell_approval") || !strings.Contains(err.Error(), "interactve") {
		t.Errorf("error should name the key and the bad value, got: %v", err)
	}
}

// The resolved approval mode is published in shellApprovalMode, which is the
// only thing runAgentLoop consults before installing a terminal approver. If
// setupComponents stopped publishing it, the gate would be configured and
// never enforced.
func TestSetupComponentsPublishesApprovalMode(t *testing.T) {
	prev := shellApprovalMode
	t.Cleanup(func() { shellApprovalMode = prev })
	shellApprovalMode = tools.ApprovalOff

	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test"},
	}
	cfg.Tools.ShellApproval = "always"

	if _, _, _, _, _, _, err := setupComponents(cfg); err != nil {
		t.Fatalf("setupComponents: %v", err)
	}
	if shellApprovalMode != tools.ApprovalAlways {
		t.Errorf("shellApprovalMode = %v, want %v", shellApprovalMode, tools.ApprovalAlways)
	}
}

// A legacy config with one usable provider produces a working component set,
// and the tool registry carries the tools the bundled skills reference.
func TestSetupComponentsLegacyProviderBuildsRegistry(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test", APIBase: "https://example.invalid/v1"},
	}

	msgBus, provider, sessionMgr, agentInstance, reg, sender, err := setupComponents(cfg)
	if err != nil {
		t.Fatalf("setupComponents: %v", err)
	}
	if msgBus == nil || provider == nil || sessionMgr == nil || agentInstance == nil || reg == nil || sender == nil {
		t.Fatal("setupComponents returned a nil component")
	}
	if reg.Count() == 0 {
		t.Fatal("tool registry is empty")
	}
	for _, want := range []string{"filesystem", "shell", "cron"} {
		if _, ok := reg.Get(want); !ok {
			t.Errorf("tool %q is not registered; bundled skills reference it", want)
		}
	}
}

// An anthropic (or openai) entry in a legacy config used to be silently
// ignored: registerProviders only knew the six original providers, so
// `configure --provider anthropic` wrote config the runtime never read.
func TestSetupComponentsRegistersAnthropicAndOpenAILegacy(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"anthropic": {Enabled: true, APIKey: "sk-a", APIBase: "https://example.invalid/v1"},
		"openai":    {Enabled: true, APIKey: "sk-o", APIBase: "https://example.invalid/v1", Model: "gpt-4o"},
	}
	cfg.ProviderDefaults.Default = "anthropic"
	cfg.ProviderDefaults.FallbackOrder = []string{"anthropic", "openai"}

	_, provider, _, _, _, _, err := setupComponents(cfg)
	if err != nil {
		t.Fatalf("setupComponents: %v", err)
	}
	mp, ok := provider.(*providers.MultiProvider)
	if !ok {
		t.Fatalf("provider is %T, want *providers.MultiProvider", provider)
	}
	for _, name := range []string{"anthropic", "openai"} {
		if !mp.HasProvider(name) {
			t.Errorf("legacy config with %q was not registered", name)
		}
	}
}

// The menu covers every guided-path provider, recommended default first.
func TestInteractiveProviderMenuOrder(t *testing.T) {
	menu := interactiveProviderMenu()
	if menu[0] != "nvidia" {
		t.Errorf("menu[0] = %q, want the recommended nvidia first", menu[0])
	}
	found := map[string]bool{}
	for _, name := range menu {
		found[name] = true
	}
	for _, want := range []string{"anthropic", "openai", "openrouter", "groq", "ollama", "poolside", "github-copilot"} {
		if !found[want] {
			t.Errorf("menu is missing %q", want)
		}
	}
}
