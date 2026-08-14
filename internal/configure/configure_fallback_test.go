package configure

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

func fallbackTestConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"nvidia":   {APIKey: "k1", Enabled: true},
		"poolside": {APIKey: "k2", Enabled: true},
		"groq":     {APIKey: "k3", Enabled: false}, // configured but disabled
	}
	return cfg
}

func TestSetFallbackOrderWritesOrder(t *testing.T) {
	cfg := fallbackTestConfig()
	c := New(cfg)
	if err := c.SetFallbackOrder([]string{"nvidia", "poolside"}); err != nil {
		t.Fatalf("SetFallbackOrder = %v", err)
	}
	got := cfg.ProviderDefaults.FallbackOrder
	if len(got) != 2 || got[0] != "nvidia" || got[1] != "poolside" {
		t.Errorf("FallbackOrder = %v", got)
	}
}

func TestSetFallbackOrderRejectsUnknownAndDisabled(t *testing.T) {
	cfg := fallbackTestConfig()
	c := New(cfg)
	err := c.SetFallbackOrder([]string{"nvidia", "nosuch"})
	if err == nil || !strings.Contains(err.Error(), `"nosuch"`) {
		t.Fatalf("unknown provider must be rejected by name, got %v", err)
	}
	if !strings.Contains(err.Error(), "nvidia") || !strings.Contains(err.Error(), "poolside") {
		t.Errorf("error should list the configured providers, got %v", err)
	}
	if err := c.SetFallbackOrder([]string{"groq"}); err == nil {
		t.Fatal("a disabled provider must be rejected — it would vanish from the chain silently")
	}
	if cfg.ProviderDefaults.FallbackOrder != nil {
		t.Errorf("a rejected order must not be written, got %v", cfg.ProviderDefaults.FallbackOrder)
	}
}

func TestSetFallbackOrderRejectsDuplicates(t *testing.T) {
	c := New(fallbackTestConfig())
	if err := c.SetFallbackOrder([]string{"nvidia", "nvidia"}); err == nil {
		t.Fatal("duplicates must be rejected")
	}
}

func TestSetFallbackOrderEmptyClears(t *testing.T) {
	cfg := fallbackTestConfig()
	cfg.ProviderDefaults.FallbackOrder = []string{"nvidia"}
	c := New(cfg)
	if err := c.SetFallbackOrder(nil); err != nil {
		t.Fatalf("clear = %v", err)
	}
	if cfg.ProviderDefaults.FallbackOrder != nil {
		t.Errorf("clear left %v", cfg.ProviderDefaults.FallbackOrder)
	}
}
