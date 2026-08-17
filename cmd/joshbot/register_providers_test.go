package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// registerProviders is the single place that turns config into a running
// fallback chain, for startup and for the configure tool's hot reload alike.
// Every gate in it fails *silently*: a provider left out because `enabled` was
// omitted, or because its key is empty, produces no error and no log the
// operator will read — the assistant simply answers from a different model, or
// stops answering. The chain order is equally invisible, and it decides which
// account gets billed when the primary is down.

// chatText pulls the assistant text out of a chat response.
func chatText(resp *providers.ChatResponse) string {
	if len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Message.Content
}

// mp builds an empty MultiProvider with a named default.
func mp(def string) *providers.MultiProvider {
	return providers.NewMultiProvider(providers.MultiProviderConfig{DefaultProvider: def})
}

// legacyCfg returns a legacy-format config carrying the given providers.
func legacyCfg(t *testing.T, provs map[string]config.ProviderConfig) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Providers = provs
	cfg.Agents.Defaults.Model = "test-model"
	return cfg
}

// "enabled": true is required to activate a provider, and omitting it is the
// single most common joshbot misconfiguration. What makes it worth a test is
// that the omission is indistinguishable from a typo at runtime: the provider
// is absent from the chain, nothing says so, and the operator sees their
// configured key apparently ignored.
func TestRegisterProvidersSkipsADisabledProvider(t *testing.T) {
	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"openrouter": {APIKey: "sk-live", Enabled: true},
		"groq":       {APIKey: "gsk-live", Enabled: false},
	})
	m := mp("openrouter")

	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}
	if !m.HasProvider("openrouter") {
		t.Error("openrouter was configured and enabled but is not in the chain")
	}
	if m.HasProvider("groq") {
		t.Error("groq has enabled=false and must not be registered")
	}
	// HasProvider alone would also be false for a groq registered *as*
	// disabled, so assert the entry is absent outright: a disabled entry still
	// occupies a slot in the chain and shows up in `joshbot status`.
	if names := m.GetProviderNames(); len(names) != 1 || names[0] != "openrouter" {
		t.Errorf("registered providers = %v, want only [openrouter]", names)
	}
}

// A key-bearing provider with an empty key is a half-finished config, not a
// usable fallback. Registering it puts a provider in the chain that answers 401
// on every request, so a transient failure on the primary turns into a hard
// failure instead of a fallback.
func TestRegisterProvidersSkipsAProviderWithNoAPIKey(t *testing.T) {
	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"openrouter": {APIKey: "sk-live", Enabled: true},
		"nvidia":     {APIKey: "", Enabled: true},
	})
	m := mp("openrouter")

	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}
	if m.HasProvider("nvidia") {
		t.Error("nvidia has an empty api_key and must not be registered")
	}
}

// Every provider present but none usable has to be an error at registration.
// Deferring it means the first Chat() reports "no providers configured" in the
// middle of a conversation, with nothing pointing at the config that caused it.
func TestRegisterProvidersFailsWhenNothingIsUsable(t *testing.T) {
	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"openrouter": {APIKey: "sk-live", Enabled: false},
		"groq":       {APIKey: "", Enabled: true},
	})
	m := mp("openrouter")

	err := registerProviders(cfg, m)
	if err == nil {
		t.Fatal("registerProviders accepted a config with no usable provider")
	}
	if n := len(m.GetProviderNames()); n != 0 {
		t.Errorf("%d provider(s) registered, want 0", n)
	}
}

// provider_defaults.fallback_order is the only control an operator has over
// which account is billed when the primary fails. If it is ignored, the
// hardcoded defaults apply and the wrong provider is charged, silently — the
// answer comes back normally either way.
func TestRegisterProvidersHonoursFallbackOrder(t *testing.T) {
	primary := newRecordingChatServer(t, "from-groq")
	secondary := newRecordingChatServer(t, "from-nvidia")

	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"groq":   {APIKey: "gsk-live", APIBase: primary.URL + "/v1", Enabled: true},
		"nvidia": {APIKey: "nvapi-live", APIBase: secondary.URL + "/v1", Model: "nv-model", Enabled: true},
	})
	// groq is listed first, which inverts the hardcoded default (nvidia is
	// priority 1, groq lands after the fallback list).
	cfg.ProviderDefaults.FallbackOrder = []string{"groq", "nvidia"}

	// The default provider is deliberately one that is not registered, so the
	// chain is decided purely by priority rather than by the default.
	m := mp("openrouter")
	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}

	resp, err := m.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if chatText(resp) != "from-groq" {
		t.Errorf("the chain answered %q; fallback_order put groq first", chatText(resp))
	}
	if n := len(secondary.requests()); n != 0 {
		t.Errorf("nvidia was called %d time(s); it is second in fallback_order", n)
	}
}

// A model-centric config whose entries all fail to resolve — every one
// disabled, or missing the api_key its provider requires — must error rather
// than register nothing and return success. GetAllModelConfigs drops
// unresolvable entries silently, so without this check the agent starts with an
// empty chain and fails on the first turn with no cause attached.
func TestRegisterProvidersRejectsAModelCentricConfigWithNoUsableModels(t *testing.T) {
	cfg := config.Defaults()
	cfg.ModelsConfig = config.ModelsConfig{
		Agent: config.AgentModelConfig{Model: "cheap"},
		Models: []config.ModelConfig{
			{Name: "cheap", Model: "openrouter/model-a", APIKey: "sk-test", Disabled: true},
			{Name: "big", Model: "openrouter/model-b", APIKey: "sk-test", Disabled: true},
		},
	}

	m := mp("openrouter")
	err := registerProviders(cfg, m)
	if err == nil {
		t.Fatal("a model-centric config with no models was accepted")
	}
	if !strings.Contains(err.Error(), "no models configured") {
		t.Errorf("error = %q, want it to say no models are configured", err)
	}
}

// ollama is the one provider that legitimately has no API key, and the gate
// for it is the only one written without an api_key check. A stray key check
// there would lock out every local-only install — the case joshbot is most
// often recommended for — with no error, just an assistant that has no model.
func TestRegisterProvidersRegistersKeylessOllama(t *testing.T) {
	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"ollama": {APIKey: "", Enabled: true},
	})
	m := mp("ollama")

	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}
	if !m.HasProvider("ollama") {
		t.Error("ollama needs no api_key and must register without one")
	}
}

// An enabled github-copilot with no stored token has to be skipped softly: the
// credential lives outside config.json, so this is the state every operator is
// in between editing config and running `joshbot auth github-copilot`. Failing
// the whole registration there would take the gateway down over a provider the
// operator has not finished setting up, and the working providers with it.
func TestRegisterProvidersSkipsUnauthenticatedCopilot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"openrouter":     {APIKey: "sk-live", Enabled: true},
		"github-copilot": {Enabled: true},
	})
	m := mp("openrouter")

	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("an unauthenticated copilot took down registration: %v", err)
	}
	if m.HasProvider("github-copilot") {
		t.Error("github-copilot has no token and must not be registered")
	}
	if !m.HasProvider("openrouter") {
		t.Error("the configured openrouter provider was lost with copilot")
	}
}

// The model-centric path registers by nickname, and the nickname is what
// `--model` and `joshbot models` resolve against. Registering under the model
// id instead would make every configured nickname unresolvable.
func TestRegisterProvidersRegistersModelCentricEntriesByName(t *testing.T) {
	cfg := config.Defaults()
	cfg.ModelsConfig = config.ModelsConfig{
		Agent: config.AgentModelConfig{Model: "cheap"},
		Models: []config.ModelConfig{
			{Name: "cheap", Model: "openrouter/model-a", APIKey: "sk-test"},
			{Name: "big", Model: "openrouter/model-b", APIKey: "sk-test"},
		},
	}

	m := mp("cheap")
	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}
	for _, name := range []string{"cheap", "big"} {
		if !m.HasProvider(name) {
			t.Errorf("model %q is not registered under its name", name)
		}
	}
}

// TestRegisterProvidersRegistersOpenAICentricProviders is the #235 regression
// test. Before the generic arm, legacy config silently ignored every provider
// with no bespoke branch — openai, anthropic, azure and litellm in particular,
// all of which `joshbot configure --provider` will write — so a valid, enabled,
// keyed entry produced no running provider and joshbot reported the operator's
// own "enabled": true as the cause. A legacy config carrying exactly those four
// must now register all four.
func TestRegisterProvidersRegistersOpenAICentricProviders(t *testing.T) {
	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"openai":    {APIKey: "sk-openai", Enabled: true},
		"anthropic": {APIKey: "sk-ant", Enabled: true},
		"azure":     {APIKey: "azure-key", APIBase: "https://example.azure.com/v1", Enabled: true},
		"litellm":   {APIKey: "litellm-key", APIBase: "https://litellm.local/v1", Enabled: true},
		"custom":    {APIKey: "custom-key", APIBase: "https://custom.local/v1", Enabled: true},
	})
	m := mp("openrouter")

	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}

	// Exactly the providers the bespoke arms used to skip — a regression means a
	// branch was deleted or the generic arm disabled.
	for _, name := range []string{"openai", "anthropic", "azure", "litellm", "custom"} {
		if !m.HasProvider(name) {
			t.Errorf("%s was configured, enabled and keyed but is not in the chain (#235)", name)
		}
	}
}

// The generic arm must not swallow the fail-fast: a name the registry cannot dial
// is a user mistake, so a config of only unsupported names still errors rather
// than quietly leaving the operator with nothing usable.
func TestRegisterProvidersUnknownNameStillFails(t *testing.T) {
	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"not-a-provider": {APIKey: "whatever", Enabled: true},
	})
	m := mp("openrouter")

	if err := registerProviders(cfg, m); err == nil {
		t.Fatal("a config of an unknown provider name registered as if usable")
	}
	if n := len(m.GetProviderNames()); n != 0 {
		t.Errorf("%d provider(s) registered for an unknown name, want 0", n)
	}
}

// The generic arm respects the enabled flag just like the bespoke arms: a
// disabled openai must not register, or the "enabled": true gate is meaningless.
func TestRegisterProvidersGenericArmRespectsDisabled(t *testing.T) {
	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"openai": {APIKey: "sk-openai", Enabled: false},
		"groq":   {APIKey: "gsk-live", Enabled: true},
	})
	m := mp("openrouter")

	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}
	if m.HasProvider("openai") {
		t.Error("a disabled openai registered via the generic arm")
	}
	if !m.HasProvider("groq") {
		t.Error("groq (a bespoke arm) failed to register")
	}
}
