package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// A config file that exists but cannot be parsed must be left exactly as it is.
// config.Load answers an unparseable file with Defaults() and a nil error, so
// the earlier version of this path saved defaults over the operator's real
// providers and API keys to record a model preference.
func TestSaveCopilotModel_LeavesUnreadableConfigAlone(t *testing.T) {
	home := withTempHome(t)
	path := filepath.Join(home, "config.json")
	original := "{ this is not json"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if saved := saveCopilotModel("gpt-4o"); saved {
		t.Error("saveCopilotModel() saved over a config it could not read")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("config was rewritten:\n got: %s\nwant: %s", got, original)
	}
}

// No config file at all is not a destructive case — there is nothing to lose,
// so the model preference should be written.
func TestSaveCopilotModel_CreatesConfigWhenNoneExists(t *testing.T) {
	withTempHome(t)

	if saved := saveCopilotModel("gpt-4o"); !saved {
		t.Fatal("saveCopilotModel() should create a config when none exists")
	}

	cfg, err := config.LoadStrict()
	if err != nil {
		t.Fatalf("LoadStrict() after save: %v", err)
	}
	pc := cfg.Providers["github-copilot"]
	if !pc.Enabled || pc.Model != "gpt-4o" {
		t.Errorf("github-copilot config = %+v, want enabled with model gpt-4o", pc)
	}
}

// The provider entry is updated in place: replacing the struct would drop any
// other field the operator had already set on it.
func TestSaveCopilotModel_PreservesOtherProviders(t *testing.T) {
	withTempHome(t)

	cfg := config.Defaults()
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}
	cfg.Providers["openrouter"] = config.ProviderConfig{Enabled: true, APIKey: "sk-keep-me", Model: "some-model"}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	if saved := saveCopilotModel("gpt-4o"); !saved {
		t.Fatal("saveCopilotModel() should have saved")
	}

	got, err := config.LoadStrict()
	if err != nil {
		t.Fatal(err)
	}
	if got.Providers["openrouter"].APIKey != "sk-keep-me" {
		t.Errorf("openrouter api key = %q, want it preserved", got.Providers["openrouter"].APIKey)
	}
	if got.Providers["github-copilot"].Model != "gpt-4o" {
		t.Errorf("copilot model = %q, want gpt-4o", got.Providers["github-copilot"].Model)
	}
}
