package main

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// TestProviderModelFor pins the rule that a provider answers with its own
// model, and only the default provider inherits the global one.
//
// The failure it guards: a fresh install whose only credential is an NVIDIA
// key in the environment registers nvidia and no openrouter, while
// agents.defaults.model still holds the global default "openrouter/free".
// NVIDIA answered 404 "page not found" for a model it does not serve, and
// the user was told to check a model name that was never wrong.
func TestProviderModelFor(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *config.Config
		provider string
		pcfg     config.ProviderConfig
		want     string
		why      string
	}{
		{
			name:     "a provider's own model wins",
			cfg:      config.Defaults(),
			provider: "nvidia",
			pcfg:     config.ProviderConfig{Model: "nvidia/some-model"},
			want:     "nvidia/some-model",
			why:      "an explicit provider model is the operator's choice",
		},
		{
			name:     "the default provider inherits the global model",
			cfg:      config.Defaults(),
			provider: "openrouter",
			pcfg:     config.ProviderConfig{},
			want:     "openrouter/free",
			why:      "openrouter is the effective default, so the global model is its model",
		},
		{
			name: "a provider does not inherit a model id another provider owns",
			cfg: func() *config.Config {
				c := config.Defaults()
				c.ProviderDefaults.Default = "nvidia"
				return c
			}(),
			provider: "nvidia",
			pcfg:     config.ProviderConfig{},
			want:     "nvidia/nemotron-3.5-lightning-30b-a3b",
			why:      "the global default names openrouter's model; nvidia serves its own",
		},
		{
			name: "a model id no provider claims is the operator's own",
			cfg: func() *config.Config {
				c := config.Defaults()
				c.Agents.Defaults.Model = "some-private-deployment-model"
				c.ProviderDefaults.Default = "custom"
				return c
			}(),
			provider: "custom",
			pcfg:     config.ProviderConfig{},
			want:     "some-private-deployment-model",
			why:      "a bespoke deployment's model id belongs to the provider it was configured on",
		},
		{
			name: "a fallback provider does not inherit the global model",
			cfg: func() *config.Config {
				c := config.Defaults()
				c.ProviderDefaults.Default = "openrouter"
				return c
			}(),
			provider: "nvidia",
			pcfg:     config.ProviderConfig{},
			want:     "nvidia/nemotron-3.5-lightning-30b-a3b",
			why:      "a fallback answers with its own model, not the primary's",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerModelFor(tc.cfg, tc.provider, tc.pcfg)
			if got != tc.want {
				t.Errorf("providerModelFor(%q) = %q, want %q — %s", tc.provider, got, tc.want, tc.why)
			}
		})
	}
}
