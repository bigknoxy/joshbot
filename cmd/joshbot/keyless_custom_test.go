package main

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// A custom OpenAI-compatible endpoint may need no credential: a local proxy, a
// gateway that authenticates further upstream, a service with a per-IP
// allowance. The generic arm required a key, so such a provider was skipped
// entirely -- and because the skip is silent, joshbot answered from whatever
// provider it had registered instead and the operator never learned that their
// endpoint was not the one talking.
//
// Found by dogfooding: a keyless endpoint was configured, the agent started,
// and the first turn was answered by a different provider entirely.
func TestSetupComponentsRegistersAKeylessCustomEndpoint(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"custom": {
			Enabled: true,
			APIBase: "https://vireonix.ai/v1",
			Model:   "auto",
		},
	}

	mp := multiProviderFrom(t, cfg)
	if !mp.HasProvider("custom") {
		t.Fatalf("a keyless custom endpoint with a base URL was not registered; registered: %v",
			mp.GetProviderNames())
	}
}

// The permission is for an endpoint the operator pointed at, not a blanket
// exemption: without a base URL there is nowhere to send the request, and the
// custom provider's own factory refuses it. A keyless entry with no base is a
// configuration mistake, and skipping it is what lets the diagnostic name it.
func TestSetupComponentsSkipsAKeylessCustomEndpointWithoutABase(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"custom": {Enabled: true, Model: "auto"},
	}

	// With nothing else configured, the diagnostic is what the operator sees,
	// and it must name the address rather than a credential they may not need.
	_, _, _, _, _, _, err := setupComponents(cfg)
	if err == nil {
		t.Fatal("a keyless custom endpoint with no base URL was accepted")
	}
	if !strings.Contains(err.Error(), "api_base") {
		t.Fatalf("the diagnostic does not name the missing base URL: %v", err)
	}
	if strings.Contains(err.Error(), "missing \"api_key\"") {
		t.Fatalf("the diagnostic sends the operator after a credential they may not need: %v", err)
	}
}

// The hosted providers keep the credential requirement: without a key they
// answer 401, and that masks the real problem.
func TestKeylessCustomEndpointDoesNotOpenTheHostedProviders(t *testing.T) {
	for _, name := range []string{"openrouter", "nvidia", "groq", "poolside", "openai", "anthropic", "azure"} {
		p := config.ProviderConfig{Enabled: true, APIBase: "https://example.invalid/v1"}
		if keylessCustomEndpoint(name, p) {
			t.Errorf("%s may not be registered without a credential", name)
		}
	}
}
