package main

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// The configure tool can change providers at runtime, and the reload it triggers
// Clear()s the MultiProvider before rebuilding it. That makes the rebuild the
// only thing standing between an operator edit and an empty provider chain, and
// every way it can go wrong is silent: nothing errors, the affected provider
// simply stops taking turns until the process is restarted.
//
// It had its own hand-written copy of the registration blocks that knew about
// openrouter and nvidia only, so a reload dropped groq, poolside, ollama,
// github-copilot and custom outright. Both paths now go through
// registerProviders; these tests are what keeps them from drifting apart again.

// reloaded rebuilds mp from cfg exactly as the configure tool's hot reload does.
func reloaded(t *testing.T, mp *providers.MultiProvider, cfg *config.Config) []string {
	t.Helper()
	mp.Clear()
	if err := registerProviders(cfg, mp); err != nil {
		t.Fatalf("registerProviders after Clear: %v", err)
	}
	return mp.GetProviderNames()
}

// A reload that changes nothing must leave the chain exactly as it was. Losing a
// provider here costs failover with no diagnostic anywhere — the agent answers
// normally right up until the primary is down.
func TestProviderReloadKeepsEveryLegacyProvider(t *testing.T) {
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
	before := mp.GetProviderNames()

	after := reloaded(t, mp, cfg)
	for _, name := range want {
		if !mp.HasProvider(name) {
			t.Errorf("%s was dropped by the reload; before=%v after=%v", name, before, after)
		}
	}
	if len(after) != len(before) {
		t.Errorf("reload changed the provider count: before=%v after=%v", before, after)
	}
}

// The reload exists to apply edits, so an added provider must appear and a
// removed one must go. A reload that only ever adds leaves a provider the
// operator disabled still serving traffic.
func TestProviderReloadAppliesConfigEdits(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test", APIBase: "https://example.invalid/v1"},
	}

	mp := multiProviderFrom(t, cfg)
	if !mp.HasProvider("openrouter") {
		t.Fatalf("fixture did not register openrouter: %v", mp.GetProviderNames())
	}

	// The operator switches provider entirely.
	cfg.Providers = map[string]config.ProviderConfig{
		"groq": {Enabled: true, APIKey: "gsk-test"},
	}
	after := reloaded(t, mp, cfg)

	if !mp.HasProvider("groq") {
		t.Errorf("the newly configured provider was not picked up: %v", after)
	}
	if mp.HasProvider("openrouter") {
		t.Errorf("the removed provider survived the reload: %v", after)
	}
}

// A reload that empties the chain must report it. Returning nil leaves the
// process running with no provider at all, and the first thing the operator sees
// is the next turn failing with no reference to the edit that caused it.
func TestProviderReloadReportsAnEmptiedChain(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test", APIBase: "https://example.invalid/v1"},
	}
	mp := multiProviderFrom(t, cfg)

	// Every provider present but none enabled — the shape a botched edit takes.
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: false, APIKey: "sk-test"},
	}
	mp.Clear()
	err := registerProviders(cfg, mp)
	if err == nil {
		t.Fatalf("a reload that registered nothing returned nil; providers: %v", mp.GetProviderNames())
	}
}

// The model-centric path reloads too, and it is the one an operator using the
// newer config format is on. Rebuilding only the legacy map would leave them
// with nothing after any configure call.
func TestProviderReloadKeepsModelCentricModels(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = nil
	cfg.ModelsConfig.Models = []config.ModelConfig{
		{Name: "primary", Model: "openrouter/a", APIKey: "sk-a"},
		{Name: "backup", Model: "groq/b", APIKey: "sk-b"},
	}
	cfg.ModelsConfig.Agent.Model = "primary"

	mp := multiProviderFrom(t, cfg)
	after := reloaded(t, mp, cfg)
	for _, name := range []string{"primary", "backup"} {
		if !mp.HasProvider(name) {
			t.Errorf("model %q was dropped by the reload; registered: %v", name, after)
		}
	}
}
