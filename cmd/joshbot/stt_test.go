package main

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// buildTranscriber is a startup-or-never gate: every misconfiguration must
// fail at wiring time naming the key, never as a per-message error.
func TestBuildTranscriberValidation(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{
			STT: config.STTConfig{Provider: "groq"},
			Providers: map[string]config.ProviderConfig{
				"groq": {APIKey: "k", Enabled: true},
			},
		}
	}

	t.Run("happy path with per-provider default model", func(t *testing.T) {
		fn, err := buildTranscriber(base())
		if err != nil {
			t.Fatalf("buildTranscriber: %v", err)
		}
		if fn == nil {
			t.Fatal("buildTranscriber returned a nil transcriber with no error")
		}
	})

	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{"unknown provider", func(c *config.Config) { c.STT.Provider = "nope" }, "not a configured provider"},
		{"disabled provider", func(c *config.Config) {
			p := c.Providers["groq"]
			p.Enabled = false
			c.Providers["groq"] = p
		}, "not enabled"},
		{"missing key", func(c *config.Config) {
			p := c.Providers["groq"]
			p.APIKey = ""
			c.Providers["groq"] = p
		}, "no API key"},
		{"no default model for custom provider", func(c *config.Config) {
			c.STT.Provider = "custom"
			c.Providers["custom"] = config.ProviderConfig{APIKey: "k", Enabled: true, APIBase: "http://x"}
		}, "set stt.model"},
		{"no api base for custom provider", func(c *config.Config) {
			c.STT.Provider = "custom"
			c.STT.Model = "whisper-1"
			c.Providers["custom"] = config.ProviderConfig{APIKey: "k", Enabled: true}
		}, "api_base"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			_, err := buildTranscriber(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// The per-provider STT model defaults are a documented contract.
func TestDefaultSTTModels(t *testing.T) {
	if m := config.DefaultSTTModel("groq"); m != "whisper-large-v3-turbo" {
		t.Errorf("groq default = %q", m)
	}
	if m := config.DefaultSTTModel("openai"); m != "whisper-1" {
		t.Errorf("openai default = %q", m)
	}
	if m := config.DefaultSTTModel("poolside"); m != "" {
		t.Errorf("poolside default = %q, want none", m)
	}
}
