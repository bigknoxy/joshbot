package main

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// The legacy provider map is wired into the MultiProvider by one hand-written
// block per provider in setupComponents. Nothing else checks that those blocks
// agree with each other, and every failure mode here is silent: a provider that
// stops being registered does not error at startup as long as *some other*
// provider registered, it just quietly never takes a turn in the fallback
// chain. These tests pin the two rules the blocks are supposed to share —
// "enabled": true is required, and a credential is required for every provider
// that has one — for each provider individually.

// multiProviderFrom runs setupComponents and returns the MultiProvider it
// built. The provider it hands back is the same object the agent dials, so
// asserting on this is asserting on what would actually be used.
func multiProviderFrom(t *testing.T, cfg *config.Config) *providers.MultiProvider {
	t.Helper()

	_, provider, _, _, _, _, err := setupComponents(cfg)
	if err != nil {
		t.Fatalf("setupComponents: %v", err)
	}
	mp, ok := provider.(*providers.MultiProvider)
	if !ok {
		t.Fatalf("setupComponents returned %T, not a *providers.MultiProvider", provider)
	}
	return mp
}

// legacyProviderCases lists every provider the legacy branch of
// setupComponents knows how to register, with the minimum config that should
// register it. github-copilot is excluded on purpose: it registers only when a
// real OAuth token is on disk, which is covered by the auth tests.
var legacyProviderCases = []struct {
	name string
	// needsKey records whether the block gates on a non-empty API key.
	// Ollama is local and deliberately does not.
	needsKey bool
	cfg      config.ProviderConfig
}{
	{"openrouter", true, config.ProviderConfig{Enabled: true, APIKey: "sk-test", APIBase: "https://example.invalid/v1"}},
	{"nvidia", true, config.ProviderConfig{Enabled: true, APIKey: "nvapi-test", Model: "meta/llama-3.1-8b-instruct"}},
	{"groq", true, config.ProviderConfig{Enabled: true, APIKey: "gsk-test"}},
	{"poolside", true, config.ProviderConfig{Enabled: true, APIKey: "ps-test"}},
	{"ollama", false, config.ProviderConfig{Enabled: true, Model: "llama3.2"}},
	{"custom", true, config.ProviderConfig{Enabled: true, APIKey: "ck-test", APIBase: "https://example.invalid/v1", Model: "custom-model"}},
}

// Each supported legacy provider must actually reach the MultiProvider when it
// is enabled and credentialed. A provider that silently fails to register is
// invisible until a fallback is needed and does not happen.
func TestSetupComponentsRegistersEachEnabledLegacyProvider(t *testing.T) {
	for _, tc := range legacyProviderCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setupConfig(t)
			cfg.Providers = map[string]config.ProviderConfig{tc.name: tc.cfg}

			mp := multiProviderFrom(t, cfg)
			if !mp.HasProvider(tc.name) {
				t.Fatalf("%s was enabled and credentialed but is not registered; registered: %v",
					tc.name, mp.GetProviderNames())
			}
		})
	}
}

// "enabled": true is load-bearing: omitting it must leave the provider out
// entirely rather than registering it in a disabled state, because a
// registered-but-disabled entry and an unregistered one are reported
// differently by every diagnostic joshbot has.
func TestSetupComponentsSkipsLegacyProviderWithoutEnabled(t *testing.T) {
	for _, tc := range legacyProviderCases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.cfg
			p.Enabled = false

			cfg := setupConfig(t)
			cfg.Providers = map[string]config.ProviderConfig{
				// A second, usable provider keeps setupComponents from
				// failing on "no providers registered", so the assertion is
				// about this provider and not about the startup guard.
				"openrouter": {Enabled: true, APIKey: "sk-other", APIBase: "https://example.invalid/v1"},
				tc.name:      p,
			}
			if tc.name == "openrouter" {
				cfg.Providers["groq"] = config.ProviderConfig{Enabled: true, APIKey: "gsk-other"}
			}

			mp := multiProviderFrom(t, cfg)
			for _, got := range mp.GetProviderNames() {
				if got == tc.name {
					t.Fatalf("%s was registered despite enabled=false; registered: %v",
						tc.name, mp.GetProviderNames())
				}
			}
		})
	}
}

// A provider that needs a credential must not be registered without one: a
// keyless registration turns a config mistake into a 401 mid-conversation
// instead of a startup complaint naming the provider.
func TestSetupComponentsSkipsKeylessLegacyProvider(t *testing.T) {
	for _, tc := range legacyProviderCases {
		if !tc.needsKey {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			p := tc.cfg
			p.APIKey = ""

			cfg := setupConfig(t)
			cfg.Providers = map[string]config.ProviderConfig{
				"ollama": {Enabled: true, Model: "llama3.2"},
				tc.name:  p,
			}

			mp := multiProviderFrom(t, cfg)
			for _, got := range mp.GetProviderNames() {
				if got == tc.name {
					t.Fatalf("%s was registered with no API key; registered: %v",
						tc.name, mp.GetProviderNames())
				}
			}
		})
	}
}

// Ollama is the exception to the credential rule — it is a local server with
// no key — so it must register on "enabled" alone. Requiring a key here would
// make the whole local-model path unreachable.
func TestSetupComponentsRegistersKeylessOllama(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Enabled: true, Model: "llama3.2"},
	}

	mp := multiProviderFrom(t, cfg)
	if !mp.HasProvider("ollama") {
		t.Fatalf("keyless ollama was not registered; registered: %v", mp.GetProviderNames())
	}
}

// Every enabled, credentialed provider in a mixed map has to end up in the
// chain. Registering only the first one is a plausible regression (each block
// is separately guarded) and would silently disable failover.
func TestSetupComponentsRegistersAllProvidersInAMixedConfig(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{}
	want := make([]string, 0, len(legacyProviderCases))
	for _, tc := range legacyProviderCases {
		cfg.Providers[tc.name] = tc.cfg
		want = append(want, tc.name)
	}
	cfg.ProviderDefaults.Default = "openrouter"
	cfg.ProviderDefaults.FallbackOrder = want

	mp := multiProviderFrom(t, cfg)
	for _, name := range want {
		if !mp.HasProvider(name) {
			t.Errorf("%s missing from a mixed config; registered: %v", name, mp.GetProviderNames())
		}
	}
}

// The model-centric config is a second, independent registration path in
// setupComponents. Its failure modes are the same shape as the legacy one and
// just as quiet: a model that is silently not registered is never chosen, and a
// disabled model that *is* registered is chosen again after the operator turned
// it off.
func TestSetupComponentsRegistersEveryModelInAModelCentricConfig(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = nil
	cfg.ModelsConfig.Models = []config.ModelConfig{
		{Name: "primary", Model: "openrouter/a", APIKey: "sk-a"},
		{Name: "backup", Model: "groq/b", APIKey: "sk-b"},
		{Name: "spare", Model: "nvidia/c", APIKey: "sk-c"},
	}
	cfg.ModelsConfig.Agent.Model = "primary"
	cfg.ModelsConfig.Agent.Fallback = []string{"backup"}

	if !cfg.UseModelsConfig() {
		t.Fatal("fixture did not select the model-centric path")
	}

	mp := multiProviderFrom(t, cfg)
	for _, name := range []string{"primary", "backup", "spare"} {
		if !mp.HasProvider(name) {
			t.Errorf("model %q was not registered; registered: %v", name, mp.GetProviderNames())
		}
	}
}

// "disabled": true must keep a model out of the chain entirely. Registering it
// anyway means the operator's opt-out only takes effect until a fallback is
// needed — the one moment they would not be watching.
func TestSetupComponentsSkipsADisabledModel(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = nil
	cfg.ModelsConfig.Models = []config.ModelConfig{
		{Name: "primary", Model: "openrouter/a", APIKey: "sk-a"},
		{Name: "retired", Model: "groq/b", APIKey: "sk-b", Disabled: true},
	}
	cfg.ModelsConfig.Agent.Model = "primary"

	mp := multiProviderFrom(t, cfg)
	if mp.HasProvider("retired") {
		t.Errorf("a disabled model was registered; registered: %v", mp.GetProviderNames())
	}
	if !mp.HasProvider("primary") {
		t.Errorf("the active model was not registered; registered: %v", mp.GetProviderNames())
	}
}

// A models block that resolves to nothing must be a startup error naming the
// problem. Returning an empty MultiProvider instead defers the failure to the
// first turn, where it surfaces as an opaque provider error.
func TestSetupComponentsModelCentricWithNoResolvableModelIsAStartupError(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = nil
	cfg.ModelsConfig.Models = []config.ModelConfig{
		{Name: "retired", Model: "groq/b", APIKey: "sk-b", Disabled: true},
	}
	cfg.ModelsConfig.Agent.Model = "retired"

	_, _, _, _, _, _, err := setupComponents(cfg)
	if err == nil {
		t.Fatal("a models config with nothing usable started up cleanly")
	}
	if !strings.Contains(err.Error(), "no models configured") {
		t.Errorf("error = %v, want it to name the empty models config", err)
	}
}
