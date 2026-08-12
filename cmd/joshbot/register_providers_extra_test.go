package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// Two arms of registerProviders wire something the operator cannot see from
// config alone: multi-key rotation, and the credential github-copilot keeps
// outside config.json. Both fail silently. An unwrapped key pool means the
// second key is never tried, so a rate-limited primary looks like a dead
// assistant while a paid-for spare sits idle. And a copilot registration that
// picks the wrong model name 404s on the first real turn, naming a model the
// operator never configured.

// A model carrying more than one api_key must be wrapped in the rotating
// provider, and the wrap is only observable by making the first key fail: the
// pool rotates on 401 and retries once. Without the wrap the 401 is the final
// answer and the extra keys are decoration.
func TestRegisterProvidersRotatesPastARejectedKey(t *testing.T) {
	var mu sync.Mutex
	var seen []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth)
		mu.Unlock()

		if auth != "Bearer key-good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"rotated"}}]}`))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.ModelsConfig = config.ModelsConfig{
		Agent: config.AgentModelConfig{Model: "pooled"},
		Models: []config.ModelConfig{{
			Name:  "pooled",
			Model: "openrouter/model-a",
			// api_key is required for the provider to resolve at all; the
			// pool is api_key followed by api_keys, so this is a 2-key model.
			APIKey:  "key-bad",
			APIKeys: []string{"key-good"},
			APIBase: srv.URL,
		}},
	}

	m := mp("pooled")
	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}

	resp, err := m.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("chat failed; the rejected first key was never rotated past: %v", err)
	}
	if got := chatText(resp); got != "rotated" {
		t.Errorf("reply = %q, want the answer from the second key", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("saw %d request(s) %v, want a retry with the second key", len(seen), seen)
	}
	if seen[len(seen)-1] != "Bearer key-good" {
		t.Errorf("final request used %q, want the second key", seen[len(seen)-1])
	}
}

// authedCopilotHome plants an unexpired github-copilot token where
// registerProviders looks for it. ExpiresAt 0 means no expiry: GitHub's
// OAuth-App device flow returns no expires_in at all, so this is the shape a
// real auth run leaves behind.
func authedCopilotHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCopilotToken(t, home, "gho_stored_token", 0)
}

// With a token on disk copilot registers, and the provider's own configured
// model wins over the agent default. Falling back to the agent default here
// sends an openrouter-shaped model id to api.githubcopilot.com, which rejects
// it on the first turn.
func TestRegisterProvidersCopilotPrefersItsOwnModel(t *testing.T) {
	authedCopilotHome(t)

	cfg := legacyCfg(t, map[string]config.ProviderConfig{
		"openrouter":     {APIKey: "sk-live", Enabled: true},
		"github-copilot": {Enabled: true, Model: "claude-sonnet-4"},
	})
	cfg.ProviderDefaults.FallbackOrder = []string{"openrouter", "github-copilot"}
	cfg.ProviderDefaults.Default = "github-copilot"

	m := mp("github-copilot")
	if err := registerProviders(cfg, m); err != nil {
		t.Fatalf("registerProviders: %v", err)
	}
	if !m.HasProvider("github-copilot") {
		t.Fatal("an authenticated github-copilot was not registered")
	}
	// Config() reports the default provider's config, and the default here is
	// copilot, so this is the model id its requests would carry.
	if got := m.Config().Model; got != "claude-sonnet-4" {
		t.Errorf("copilot model = %q, want the provider's own configured model", got)
	}
}
