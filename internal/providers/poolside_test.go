package providers

import (
	"strings"
	"testing"
)

// Verified live against https://inference.poolside.ai/v1/models on 2026-07-26:
//
//	poolside/laguna-m.1     deprecation_date 2026-07-28
//	poolside/laguna-xs-2.1  no deprecation
//	poolside/laguna-s-2.1   no deprecation
//
// The registered default must not be a model that is about to be withdrawn.
func TestPoolsideDefaultModelIsNotDeprecated(t *testing.T) {
	got := GetDefaultModel("poolside")
	if got == "poolside/laguna-m.1" {
		t.Errorf("default model %q is deprecated 2026-07-28; anyone relying on the default breaks", got)
	}
	if !strings.HasPrefix(got, "poolside/") {
		t.Errorf("default model %q must carry the poolside/ prefix — the API 404s without it", got)
	}
}

// The API base must be discoverable from the registry rather than duplicated at
// each call site. api.poolside.ai does not resolve; inference.poolside.ai does.
func TestPoolsideDefaultAPIBase(t *testing.T) {
	const want = "https://inference.poolside.ai/v1"
	got := GetDefaultAPIBaseFor("poolside")
	if got != want {
		t.Errorf("GetDefaultAPIBaseFor(%q) = %q, want %q", "poolside", got, want)
	}
}

// Every provider that has a fixed endpoint should expose it, so a wizard or
// docs cannot drift from what the factory actually dials.
func TestDefaultAPIBaseForKnownProviders(t *testing.T) {
	tests := map[string]string{
		"poolside":   "https://inference.poolside.ai/v1",
		"openrouter": "https://openrouter.ai/api/v1",
		"groq":       "https://api.groq.com/openai/v1",
	}
	for provider, want := range tests {
		if got := GetDefaultAPIBaseFor(provider); got != want {
			t.Errorf("GetDefaultAPIBaseFor(%q) = %q, want %q", provider, got, want)
		}
	}
}

// An unknown provider has no fixed endpoint.
func TestDefaultAPIBaseForUnknown(t *testing.T) {
	if got := GetDefaultAPIBaseFor("nope"); got != "" {
		t.Errorf("GetDefaultAPIBaseFor(unknown) = %q, want empty", got)
	}
}

// The endpoint recorded in ProviderInfo must be the one the factory actually
// dials. They are separate literals, so nothing but a test keeps them equal —
// and a stale copy in the configure wizard is exactly what broke poolside setup.
func TestDefaultAPIBaseMatchesWhatFactoryDials(t *testing.T) {
	for _, name := range []string{"poolside", "openrouter", "openai", "nvidia", "groq"} {
		declared := GetDefaultAPIBaseFor(name)
		if declared == "" {
			t.Errorf("%s declares no DefaultAPIBase", name)
			continue
		}
		// Build the provider with an empty APIBase: the factory fills in its own.
		p, err := GetProvider(name, Config{APIKey: "x"})
		if err != nil {
			t.Errorf("GetProvider(%q): %v", name, err)
			continue
		}
		lp, ok := p.(*LiteLLMProvider)
		if !ok {
			continue // provider does not expose its config this way
		}
		if lp.cfg.APIBase != declared {
			t.Errorf("%s: factory dials %q but ProviderInfo declares %q",
				name, lp.cfg.APIBase, declared)
		}
	}
}
