package providers

import (
	"context"
	"testing"
)

// TestMultiProvider_StandInProviderKeepsItsOwnModel pins the behaviour that a
// provider which stands in for an absent one answers with its own model.
//
// The failure it guards: a fresh install whose only credential is an NVIDIA
// key in the environment registers nvidia and no openrouter, while
// agents.defaults.model still holds the global default "openrouter/free".
// The request names a provider that does not exist, so the chain falls to
// nvidia — but the requested model id, which belongs to the absent provider,
// used to travel with it, and NVIDIA answered 404 "page not found" for a model
// it has never heard of. The user saw "The model was not found — check the
// model name" on an install whose model name was never wrong.
func TestMultiProvider_StandInProviderKeepsItsOwnModel(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{Logger: &mockLogger{}})
	// The default names a provider that was never registered — the fresh
	// install's shape, where the default provider has no credential.
	mp.SetDefault("openrouter")
	mp.Register("nvidia", &mockProvider{
		name:         "nvidia",
		config:       Config{Model: "nvidia/nemotron-3.5-lightning-30b-a3b"},
		chatResponse: &ChatResponse{},
	}, "nvidia/nemotron-3.5-lightning-30b-a3b", 0, true)

	resp, err := mp.Chat(context.Background(), ChatRequest{Model: "openrouter/free"})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Model != "nvidia/nemotron-3.5-lightning-30b-a3b" {
		t.Errorf("the stand-in provider was sent %q, want its own model %q",
			resp.Model, "nvidia/nemotron-3.5-lightning-30b-a3b")
	}
}

// TestMultiProvider_AddressedProviderStillHonoursTheRequestedModel is the
// other half of the same contract: when the provider the request names IS
// registered, its model spec is what goes on the wire, unchanged.
func TestMultiProvider_AddressedProviderStillHonoursTheRequestedModel(t *testing.T) {
	mp := NewMultiProvider(MultiProviderConfig{Logger: &mockLogger{}})
	mp.Register("nvidia", &mockProvider{
		name:         "nvidia",
		config:       Config{Model: "nvidia/nemotron-3.5-lightning-30b-a3b"},
		chatResponse: &ChatResponse{},
	}, "nvidia/nemotron-3.5-lightning-30b-a3b", 0, true)
	mp.SetDefault("nvidia")

	resp, err := mp.Chat(context.Background(), ChatRequest{Model: "nvidia/some-other-model"})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Model != "nvidia/some-other-model" {
		t.Errorf("the addressed provider was sent %q, want the requested model %q",
			resp.Model, "nvidia/some-other-model")
	}
}
