package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// stubExchange points CopilotTokenURL at a test server that hands out issued as
// the Copilot API token, and records the GitHub token it was asked with.
func stubExchange(t *testing.T, issued string) *string {
	t.Helper()
	seen := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      issued,
			"expires_at": 0,
			"refresh_in": 1500,
		})
	}))
	t.Cleanup(srv.Close)

	old := CopilotTokenURL
	CopilotTokenURL = srv.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	return seen
}

// The device flow returns no expires_in. Stamping now+0 marked the token
// expired the moment it was written, so LoadToken rejected it immediately.
func TestAttemptTokenExchange_NoExpiresInMeansNoExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "gho_test",
		})
	}))
	defer srv.Close()

	old := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = old }()

	tok, _, err := attemptTokenExchange(context.Background(), srv.Client(), "device-code")
	if err != nil {
		t.Fatalf("attemptTokenExchange() error = %v", err)
	}
	if tok == nil {
		t.Fatal("expected a token")
	}
	if tok.ExpiresAt != 0 {
		t.Fatalf("ExpiresAt = %d, want 0 (no expiry)", tok.ExpiresAt)
	}

	// And the stored token must survive a round trip through LoadToken.
	dir := t.TempDir()
	if err := SaveToken(dir, tok); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}
	if _, err := LoadToken(dir); err != nil {
		t.Fatalf("LoadToken() error = %v, want the freshly saved token", err)
	}
}

// The Copilot chat API lives at <base>/chat/completions — a "/v1" segment 404s.
func TestCopilotAPIURL_HasNoV1Segment(t *testing.T) {
	if strings.Contains(CopilotAPIURL, "/v1") {
		t.Fatalf("CopilotAPIURL = %q, must not contain /v1", CopilotAPIURL)
	}
}

// The raw GitHub OAuth token is not a Copilot credential. Chat must exchange it
// first and send the exchanged token, with the Copilot integration headers.
func TestCopilotProvider_Chat_ExchangesTokenAndSendsCopilotHeaders(t *testing.T) {
	seen := stubExchange(t, "copilot-api-token")

	var gotAuth, gotIntegration, gotEditor, gotAPIVersion, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotIntegration = r.Header.Get("Copilot-Integration-Id")
		gotEditor = r.Header.Get("Editor-Version")
		gotAPIVersion = r.Header.Get("X-Github-Api-Version")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"index": 0, "message": map[string]any{"role": "assistant", "content": "hi"}},
			},
		})
	}))
	defer srv.Close()

	oldURL := CopilotAPIURL
	CopilotAPIURL = srv.URL
	defer func() { CopilotAPIURL = oldURL }()

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "gho_github_token")
	if _, err := p.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: "user", Content: "Hello"}},
	}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if *seen != "token gho_github_token" {
		t.Errorf("exchange Authorization = %q, want 'token gho_github_token'", *seen)
	}
	if gotAuth != "Bearer copilot-api-token" {
		t.Errorf("chat Authorization = %q, want the exchanged Copilot token", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("chat path = %q, want '/chat/completions'", gotPath)
	}
	if gotIntegration != "vscode-chat" {
		t.Errorf("Copilot-Integration-Id = %q, want 'vscode-chat'", gotIntegration)
	}
	if !strings.HasPrefix(gotEditor, "vscode/") {
		t.Errorf("Editor-Version = %q, want a vscode/<ver> value", gotEditor)
	}
	if gotAPIVersion == "" {
		t.Error("X-Github-Api-Version not set")
	}
}

// The exchanged token is cached across calls rather than re-fetched every turn.
func TestCopilotProvider_Chat_CachesExchangedToken(t *testing.T) {
	exchanges := 0
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": 0})
	}))
	defer tokenSrv.Close()
	oldTok := CopilotTokenURL
	CopilotTokenURL = tokenSrv.URL
	defer func() { CopilotTokenURL = oldTok }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{}})
	}))
	defer srv.Close()
	oldURL := CopilotAPIURL
	CopilotAPIURL = srv.URL
	defer func() { CopilotAPIURL = oldURL }()

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "gho_x")
	req := providers.ChatRequest{Messages: []providers.Message{{Role: "user", Content: "x"}}}
	for i := 0; i < 3; i++ {
		if _, err := p.Chat(context.Background(), req); err != nil {
			t.Fatalf("Chat() error = %v", err)
		}
	}
	if exchanges != 1 {
		t.Fatalf("token exchanges = %d, want 1", exchanges)
	}
}

// ListModels must ask the Copilot API, not the GitHub Models catalog.
func TestListModels_UsesCopilotModelsEndpoint(t *testing.T) {
	stubExchange(t, "copilot-api-token")

	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": "gpt-4o"}, {"id": "claude-sonnet-4"}},
		})
	}))
	defer srv.Close()

	oldURL := CopilotAPIURL
	CopilotAPIURL = srv.URL
	defer func() { CopilotAPIURL = oldURL }()

	models, err := ListModels("gho_github_token")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, want '/models'", gotPath)
	}
	if gotAuth != "Bearer copilot-api-token" {
		t.Errorf("Authorization = %q, want the exchanged Copilot token", gotAuth)
	}
	if len(models) != 2 || models[0] != "gpt-4o" {
		t.Errorf("models = %v, want [gpt-4o claude-sonnet-4]", models)
	}
}

func TestExchangeCopilotToken_RejectedCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL
	defer func() { CopilotTokenURL = old }()

	_, err := ExchangeCopilotToken(context.Background(), "gho_bad")
	if err == nil {
		t.Fatal("expected an error for a rejected token")
	}
	if !strings.Contains(err.Error(), "joshbot auth github-copilot") {
		t.Errorf("error = %v, want it to name the re-auth command", err)
	}
}
