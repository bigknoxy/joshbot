package config

import "testing"

// Poolside is the exception to prefix stripping: its model IDs genuinely begin
// with "poolside/" — that is part of the ID the API expects, not a routing
// prefix joshbot invented. Verified against https://inference.poolside.ai/v1/models,
// which lists "poolside/laguna-s-2.1", and against the chat endpoint, which
// returns 200 for the full ID and 404 ("please check the model you provided")
// for the stripped one.
func TestStripProviderPrefix_KeepsPoolsideModelID(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"poolside/laguna-s-2.1", "poolside/laguna-s-2.1"},
		{"poolside/laguna-m.1", "poolside/laguna-m.1"},
		{"poolside/laguna-xs-2.1", "poolside/laguna-xs-2.1"},
	}
	for _, tt := range tests {
		if got := StripProviderPrefix(tt.model); got != tt.want {
			t.Errorf("StripProviderPrefix(%q) = %q, want %q — poolside 404s on the stripped name",
				tt.model, got, tt.want)
		}
	}
}

// Stripping must still happen for every other provider.
func TestStripProviderPrefix_StillStripsOthers(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"anthropic/claude-sonnet-4", "claude-sonnet-4"},
		{"groq/llama-3.3-70b", "llama-3.3-70b"},
		{"openai/gpt-4", "gpt-4"},
		{"openrouter/anthropic/claude-sonnet-4", "anthropic/claude-sonnet-4"},
		{"no-prefix-model", "no-prefix-model"},
	}
	for _, tt := range tests {
		if got := StripProviderPrefix(tt.model); got != tt.want {
			t.Errorf("StripProviderPrefix(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

// Routing must be unaffected: a poolside model still resolves to poolside.
func TestDetectProvider_PoolsideUnaffected(t *testing.T) {
	info := DetectProvider("poolside/laguna-s-2.1")
	if info.Name != "poolside" {
		t.Errorf("DetectProvider name = %q, want %q", info.Name, "poolside")
	}
	if info.BaseURL != "https://inference.poolside.ai/v1" {
		t.Errorf("DetectProvider BaseURL = %q", info.BaseURL)
	}
}

// The provider's own registered default must be a model the API accepts.
func TestPoolsideDefaultModelSurvivesStripping(t *testing.T) {
	const registeredDefault = "poolside/laguna-m.1"
	if got := StripProviderPrefix(registeredDefault); got != registeredDefault {
		t.Errorf("the poolside default model is mangled to %q before it is sent, so the "+
			"out-of-the-box configuration cannot work", got)
	}
}
