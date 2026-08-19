package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
)

// buildEmbedder is a startup-or-never gate, like buildTranscriber: every
// misconfiguration must fail at wiring time naming the key, never as a
// per-request error the operator reads as a missing feature.
func TestBuildEmbedderValidation(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{
			Embeddings: config.EmbeddingsConfig{Provider: "ollama"},
			Providers: map[string]config.ProviderConfig{
				// Deliberately keyless: ollama needs no credential and is the
				// main local use case, so requiring one here would make the
				// supported configuration impossible to express.
				"ollama": {Enabled: true, APIBase: "http://localhost:11434/v1"},
			},
		}
	}

	t.Run("keyless provider with a per-provider default model", func(t *testing.T) {
		fn, err := buildEmbedder(base())
		if err != nil {
			t.Fatalf("buildEmbedder rejected a keyless ollama, which is the main use case: %v", err)
		}
		if fn == nil {
			t.Fatal("buildEmbedder returned a nil embedder with no error")
		}
	})

	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{"unknown provider", func(c *config.Config) { c.Embeddings.Provider = "nope" }, "not a configured provider"},
		{"disabled provider", func(c *config.Config) {
			p := c.Providers["ollama"]
			p.Enabled = false
			c.Providers["ollama"] = p
		}, "not enabled"},
		{"no default model for custom provider", func(c *config.Config) {
			c.Embeddings.Provider = "custom"
			c.Providers["custom"] = config.ProviderConfig{Enabled: true, APIBase: "http://x"}
		}, "set embeddings.model"},
		{"no api base for custom provider", func(c *config.Config) {
			c.Embeddings.Provider = "custom"
			c.Embeddings.Model = "e5"
			c.Providers["custom"] = config.ProviderConfig{Enabled: true}
		}, "api_base"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			if _, err := buildEmbedder(cfg); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// The per-provider embedding model defaults are a documented contract; an
// unknown provider must return "" so the operator is told to set the key rather
// than handed a model id that 404s at the first request.
func TestDefaultEmbeddingModels(t *testing.T) {
	if m := config.DefaultEmbeddingModel("ollama"); m != "nomic-embed-text" {
		t.Errorf("ollama default = %q", m)
	}
	if m := config.DefaultEmbeddingModel("openai"); m != "text-embedding-3-small" {
		t.Errorf("openai default = %q", m)
	}
	if m := config.DefaultEmbeddingModel("poolside"); m != "" {
		t.Errorf("poolside default = %q, want none", m)
	}
}

// A broken `embeddings` block must stop `joshbot serve` at startup, naming the
// key. Otherwise the misconfiguration is noticed nowhere, the embedder stays
// nil, and POST /v1/embeddings answers 501 "not configured" to an operator who
// did configure it — which reads as a missing feature, not a typo.
func TestRunServeRefusesABrokenEmbeddingsBlock(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {APIKey: "k", Model: "test-model", Enabled: true},
	}
	cfg.API.APIKeys = []string{"secret"}
	cfg.Embeddings = config.EmbeddingsConfig{Provider: "nope"}

	path := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.String("config", path, "")
	fs.String("listen", "127.0.0.1:0", "")
	fs.String("profile", "", "")

	err = runServe(cli.NewContext(cli.NewApp(), fs, nil))
	if err == nil {
		t.Fatal("runServe returned nil for an unusable embeddings block; it must refuse to start")
	}
	if !strings.Contains(err.Error(), "embeddings config") {
		t.Fatalf("error = %q, want it to name the embeddings config", err)
	}
}

// An embeddings timeout is a config.Duration, so a human-written "600s" and a
// bare 600 both mean ten minutes — not 600 nanoseconds (#240) — and a value
// under a second is a fatal config error rather than a provider that appears
// dead.
func TestEmbeddingsTimeoutIsSecondsNotNanoseconds(t *testing.T) {
	var cfg config.Config
	if err := json.Unmarshal([]byte(`{"embeddings":{"provider":"ollama","timeout":600}}`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := cfg.Embeddings.Timeout.Duration().Seconds(); got != 600 {
		t.Fatalf("timeout = %v seconds, want 600 (a bare number must be seconds)", got)
	}

	bad := config.Defaults()
	bad.Embeddings = config.EmbeddingsConfig{Provider: "ollama", Timeout: config.Duration(500 * 1e6)}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "embeddings.timeout") {
		t.Fatalf("Validate err = %v, want it to reject a sub-second embeddings.timeout by name", err)
	}
}
