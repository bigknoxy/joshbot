package config

import "testing"

// NVIDIA is the exception to prefix stripping: every model ID in the NVIDIA
// NIM catalog genuinely begins with "nvidia/" — that is part of the ID the
// API expects, not a routing prefix joshbot invented. Verified live against
// https://integrate.api.nvidia.com/v1/chat/completions on 2026-11-14, which
// returns 200 for "nvidia/nemotron-3.5-lightning-30b-a3b" and 404 for the
// stripped "nemotron-3.5-lightning-30b-a3b".
func TestStripProviderPrefix_KeepsNvidiaModelID(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"nvidia/nemotron-3.5-lightning-30b-a3b", "nvidia/nemotron-3.5-lightning-30b-a3b"},
		{"nvidia/llama-3.1-nemotron-70b-instruct", "nvidia/llama-3.1-nemotron-70b-instruct"},
		{"nvidia/nemotron-3-ultra-550b-a55b", "nvidia/nemotron-3-ultra-550b-a55b"},
	}
	for _, tt := range tests {
		if got := StripProviderPrefix(tt.model); got != tt.want {
			t.Errorf("StripProviderPrefix(%q) = %q, want %q — nvidia 404s on the stripped name",
				tt.model, got, tt.want)
		}
	}
}

// The other half of the NVIDIA case: when the configured name is joshbot's
// own routing form — the provider name in front of a model another vendor
// publishes — the prefix must still come off, or the wire name is wrong.
// "nvidia/z-ai/glm-5.2" routes to NVIDIA with the model "z-ai/glm-5.2".
func TestStripProviderPrefix_StripsNvidiaRoutingForm(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"nvidia/z-ai/glm-5.2", "z-ai/glm-5.2"},
		{"nvidia/meta/llama-3.3-70b-instruct", "meta/llama-3.3-70b-instruct"},
		{"nvidia/mistralai/mistral-large-2407", "mistralai/mistral-large-2407"},
	}
	for _, tt := range tests {
		if got := StripProviderPrefix(tt.model); got != tt.want {
			t.Errorf("StripProviderPrefix(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

// Stripping must still happen for every other provider. An OpenRouter model
// that happens to be an NVIDIA model keeps the openrouter prefix stripped
// and the nvidia part intact, because on OpenRouter the full ID is the
// vendor-qualified one.
func TestStripProviderPrefix_StillStripsOtherProviders(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"anthropic/claude-sonnet-4", "claude-sonnet-4"},
		{"groq/llama-3.3-70b", "llama-3.3-70b"},
		{"openai/gpt-4", "gpt-4"},
		{"openrouter/nvidia/nemotron-3.5-lightning:free", "nvidia/nemotron-3.5-lightning:free"},
		{"no-prefix-model", "no-prefix-model"},
	}
	for _, tt := range tests {
		if got := StripProviderPrefix(tt.model); got != tt.want {
			t.Errorf("StripProviderPrefix(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

// Routing must be unaffected: an nvidia model still resolves to nvidia.
func TestDetectProvider_NvidiaUnaffected(t *testing.T) {
	info := DetectProvider("nvidia/nemotron-3.5-lightning-30b-a3b")
	if info.Name != "nvidia" {
		t.Errorf("DetectProvider name = %q, want %q", info.Name, "nvidia")
	}
	if info.BaseURL != "https://integrate.api.nvidia.com/v1" {
		t.Errorf("DetectProvider BaseURL = %q", info.BaseURL)
	}
}

// The provider's own registered default must be a model the API accepts.
func TestNvidiaDefaultModelSurvivesStripping(t *testing.T) {
	const registeredDefault = "nvidia/nemotron-3.5-lightning-30b-a3b"
	if got := StripProviderPrefix(registeredDefault); got != registeredDefault {
		t.Errorf("the nvidia default model is mangled to %q before it is sent, so the "+
			"out-of-the-box configuration cannot work", got)
	}
}
