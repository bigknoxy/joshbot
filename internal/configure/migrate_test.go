package configure

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

func legacyConfig() *config.Config {
	cfg := config.Defaults()
	cfg.ModelsConfig = config.ModelsConfig{}
	cfg.Providers = map[string]config.ProviderConfig{
		"nvidia":   {APIKey: "k1", Enabled: true, Model: "z-ai/glm-5.2", APIBase: "https://integrate.api.nvidia.com/v1"},
		"poolside": {APIKey: "k2", Enabled: true, Model: "poolside/laguna-s-2.1"},
		"groq":     {APIKey: "k3", Enabled: false},
	}
	cfg.ProviderDefaults = config.ProviderDefaults{Default: "nvidia", FallbackOrder: []string{"nvidia", "poolside"}}
	return cfg
}

func TestMigrateToModelsConfig(t *testing.T) {
	cfg := legacyConfig()
	report, err := New(cfg).MigrateToModelsConfig()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !cfg.UseModelsConfig() {
		t.Fatal("config does not use models_config after migration")
	}
	if cfg.Providers != nil {
		t.Error("legacy providers block was not cleared")
	}
	if cfg.ModelsConfig.Agent.Model != "nvidia" {
		t.Errorf("agent.model = %q, want nvidia", cfg.ModelsConfig.Agent.Model)
	}
	if len(cfg.ModelsConfig.Agent.Fallback) != 1 || cfg.ModelsConfig.Agent.Fallback[0] != "poolside" {
		t.Errorf("agent.fallback = %v", cfg.ModelsConfig.Agent.Fallback)
	}
	// The wire behaviour must round-trip: nvidia's model gains the routing
	// prefix and strips back to the exact legacy wire model; poolside's
	// prefix is part of the real ID and is kept whole.
	nv, err := cfg.ResolveModelConfig("nvidia")
	if err != nil || nv.ModelID != "z-ai/glm-5.2" || nv.Provider != "nvidia" || nv.APIKey != "k1" {
		t.Errorf("nvidia resolved = %+v, err=%v", nv, err)
	}
	ps, err := cfg.ResolveModelConfig("poolside")
	if err != nil || ps.ModelID != "poolside/laguna-s-2.1" || ps.Provider != "poolside" {
		t.Errorf("poolside resolved = %+v, err=%v", ps, err)
	}
	joined := strings.Join(report, "\n")
	if !strings.Contains(joined, "skipped groq") {
		t.Errorf("report should note the disabled provider: %v", report)
	}
}

func TestMigrateRefusesLossyConversions(t *testing.T) {
	for name, mutate := range map[string]func(*config.Config){
		"timeout": func(c *config.Config) {
			p := c.Providers["nvidia"]
			p.Timeout = config.Duration(600e9)
			c.Providers["nvidia"] = p
		},
		"max_retries": func(c *config.Config) {
			p := c.Providers["nvidia"]
			n := 5
			p.MaxRetries = &n
			c.Providers["nvidia"] = p
		},
		"github-copilot": func(c *config.Config) {
			c.Providers["github-copilot"] = config.ProviderConfig{Enabled: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := legacyConfig()
			mutate(cfg)
			if _, err := New(cfg).MigrateToModelsConfig(); err == nil {
				t.Fatal("a lossy migration must refuse, not drop the setting")
			}
			if cfg.UseModelsConfig() || cfg.Providers == nil {
				t.Error("a refused migration must leave the config untouched")
			}
		})
	}
}

func TestMigrateRefusesWhenAlreadyMigrated(t *testing.T) {
	cfg := legacyConfig()
	if _, err := New(cfg).MigrateToModelsConfig(); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if _, err := New(cfg).MigrateToModelsConfig(); err == nil {
		t.Fatal("second migrate must refuse")
	}
}
