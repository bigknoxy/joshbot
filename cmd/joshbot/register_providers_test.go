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
