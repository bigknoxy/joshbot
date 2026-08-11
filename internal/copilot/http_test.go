package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

func TestAttemptTokenExchange_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "test-access-token",
			"refresh_token": "test-refresh-token",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 5 * time.Second}
	token, interval, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err != nil {
		t.Fatalf("attemptTokenExchange() error = %v", err)
	}
	if token == nil {
		t.Fatal("expected non-nil token")
	}
	if token.AccessToken != "test-access-token" {
		t.Errorf("AccessToken = %q, want 'test-access-token'", token.AccessToken)
	}
	if token.RefreshToken != "test-refresh-token" {
		t.Errorf("RefreshToken = %q, want 'test-refresh-token'", token.RefreshToken)
	}
	if interval != 0 {
		t.Errorf("interval = %d, want 0", interval)
	}
}

func TestAttemptTokenExchange_AuthorizationPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"error": "authorization_pending",
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 5 * time.Second}
	token, interval, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err != nil {
		t.Fatalf("attemptTokenExchange() error = %v", err)
	}
	if token != nil {
		t.Error("expected nil token for authorization_pending")
	}
	if interval != 0 {
		t.Errorf("interval = %d, want 0", interval)
	}
}

func TestAttemptTokenExchange_SlowDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"error":    "slow_down",
			"interval": 15,
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 5 * time.Second}
	token, interval, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err != nil {
		t.Fatalf("attemptTokenExchange() error = %v", err)
	}
	if token != nil {
		t.Error("expected nil token for slow_down")
	}
	if interval != 15 {
		t.Errorf("interval = %d, want 15", interval)
	}
}

func TestAttemptTokenExchange_ExpiredToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"error": "expired_token",
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 5 * time.Second}
	token, _, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err == nil {
		t.Fatal("expected error for expired_token")
	}
	if token != nil {
		t.Error("expected nil token for expired_token")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error = %v, want 'expired'", err)
	}
}

func TestAttemptTokenExchange_AccessDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"error": "access_denied",
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 5 * time.Second}
	token, _, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err == nil {
		t.Fatal("expected error for access_denied")
	}
	if token != nil {
		t.Error("expected nil token for access_denied")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %v, want 'denied'", err)
	}
}

func TestAttemptTokenExchange_UnknownError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"error":             "unknown_error",
			"error_description": "something went wrong",
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 5 * time.Second}
	token, _, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err == nil {
		t.Fatal("expected error for unknown_error")
	}
	if token != nil {
		t.Error("expected nil token for unknown_error")
	}
	if !strings.Contains(err.Error(), "unknown_error") {
		t.Errorf("error = %v, want 'unknown_error'", err)
	}
}

func TestAttemptTokenExchange_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 5 * time.Second}
	_, _, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error = %v, want 'decode'", err)
	}
}

func TestAttemptTokenExchange_EmptyAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "",
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 5 * time.Second}
	token, _, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err != nil {
		t.Fatalf("attemptTokenExchange() error = %v", err)
	}
	if token != nil {
		t.Error("expected nil token for empty access_token")
	}
}

func TestInitiateDeviceFlow_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "test-device-code",
			"user_code":        "ABCD-1234",
			"verification_uri": "https://github.com/login/device",
			"interval":         5,
			"expires_in":       900,
		})
	}))
	defer srv.Close()

	oldURL := DeviceCodeURL
	DeviceCodeURL = srv.URL
	defer func() { DeviceCodeURL = oldURL }()

	resp, err := InitiateDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("InitiateDeviceFlow() error = %v", err)
	}
	if resp.DeviceCode != "test-device-code" {
		t.Errorf("DeviceCode = %q, want 'test-device-code'", resp.DeviceCode)
	}
	if resp.UserCode != "ABCD-1234" {
		t.Errorf("UserCode = %q, want 'ABCD-1234'", resp.UserCode)
	}
	if resp.Interval != 5 {
		t.Errorf("Interval = %d, want 5", resp.Interval)
	}
}

func TestInitiateDeviceFlow_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	oldURL := DeviceCodeURL
	DeviceCodeURL = srv.URL
	defer func() { DeviceCodeURL = oldURL }()

	_, err := InitiateDeviceFlow(context.Background())
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %v, want '503'", err)
	}
}

func TestInitiateDeviceFlow_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	oldURL := DeviceCodeURL
	DeviceCodeURL = srv.URL
	defer func() { DeviceCodeURL = oldURL }()

	_, err := InitiateDeviceFlow(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestPollForToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "poll-access-token",
			"refresh_token": "poll-refresh-token",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	token, err := PollForToken(ctx, "test-device-code", 1)
	if err != nil {
		t.Fatalf("PollForToken() error = %v", err)
	}
	if token == nil {
		t.Fatal("expected non-nil token")
	}
	if token.AccessToken != "poll-access-token" {
		t.Errorf("AccessToken = %q, want 'poll-access-token'", token.AccessToken)
	}
}

func TestPollForToken_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return authorization_pending so the poll continues
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"error": "authorization_pending",
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := PollForToken(ctx, "test-device-code", 1)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestPollForToken_IntervalClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	oldURL := AccessTokenURL
	AccessTokenURL = srv.URL
	defer func() { AccessTokenURL = oldURL }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// interval < 5 should be clamped to 5
	token, err := PollForToken(ctx, "test-device-code", 1)
	if err != nil {
		t.Fatalf("PollForToken() error = %v", err)
	}
	if token == nil {
		t.Fatal("expected non-nil token")
	}
}

func TestCopilotProvider_Chat_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-access-token" {
			t.Errorf("Authorization = %q, want 'Bearer test-access-token'", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "gpt-4o",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "Hello from Copilot!",
					},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer srv.Close()

	oldURL := CopilotAPIURL
	CopilotAPIURL = srv.URL
	defer func() { CopilotAPIURL = oldURL }()

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "test-access-token")
	resp, err := p.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello from Copilot!" {
		t.Errorf("Content = %q, want 'Hello from Copilot!'", resp.Choices[0].Message.Content)
	}
}

func TestCopilotProvider_Chat_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()

	oldURL := CopilotAPIURL
	CopilotAPIURL = srv.URL
	defer func() { CopilotAPIURL = oldURL }()

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "bad-token")
	_, err := p.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error = %v, want 'Invalid API key'", err)
	}
}

func TestCopilotProvider_Chat_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	oldURL := CopilotAPIURL
	CopilotAPIURL = srv.URL
	defer func() { CopilotAPIURL = oldURL }()

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "expired-token")
	_, err := p.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !strings.Contains(err.Error(), "authentication expired") {
		t.Errorf("error = %v, want 'authentication expired'", err)
	}
}

func TestCopilotProvider_Chat_InvalidResponseJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	oldURL := CopilotAPIURL
	CopilotAPIURL = srv.URL
	defer func() { CopilotAPIURL = oldURL }()

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "token")
	_, err := p.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestListModels_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("Authorization = %q, want 'Bearer test-token'", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "gpt-4o", "name": "GPT-4o"},
			{"id": "gpt-4", "name": "GPT-4"},
		})
	}))
	defer srv.Close()

	oldURL := copilotCatalogURL
	copilotCatalogURL = srv.URL
	defer func() { copilotCatalogURL = oldURL }()

	models, err := ListModels("test-token")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0] != "gpt-4o" {
		t.Errorf("models[0] = %q, want 'gpt-4o'", models[0])
	}
	if models[1] != "gpt-4" {
		t.Errorf("models[1] = %q, want 'gpt-4'", models[1])
	}
}

func TestListModels_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	oldURL := copilotCatalogURL
	copilotCatalogURL = srv.URL
	defer func() { copilotCatalogURL = oldURL }()

	_, err := ListModels("bad-token")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want '401'", err)
	}
}

func TestListModels_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	oldURL := copilotCatalogURL
	copilotCatalogURL = srv.URL
	defer func() { copilotCatalogURL = oldURL }()

	_, err := ListModels("token")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSaveToken_OverwritesExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Save initial token
	token1 := &TokenInfo{
		AccessToken:  "token-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    1000,
	}
	if err := SaveToken(tmpDir, token1); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	// Save new token (should overwrite)
	token2 := &TokenInfo{
		AccessToken:  "token-2",
		RefreshToken: "refresh-2",
		ExpiresAt:    0, // No expiry
	}
	if err := SaveToken(tmpDir, token2); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	// Load and verify new token
	loaded, err := LoadToken(tmpDir)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if loaded.AccessToken != "token-2" {
		t.Errorf("AccessToken = %q, want 'token-2'", loaded.AccessToken)
	}
}

func TestSaveToken_MissingTokenField(t *testing.T) {
	tmpDir := t.TempDir()

	// Save token with only access token (no refresh token)
	token := &TokenInfo{
		AccessToken: "test-token",
	}
	if err := SaveToken(tmpDir, token); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	// Load and verify
	loaded, err := LoadToken(tmpDir)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if loaded.AccessToken != "test-token" {
		t.Errorf("AccessToken = %q, want 'test-token'", loaded.AccessToken)
	}
}

func TestLoadToken_MissingGitHubCopilotKey(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := tmpDir + "/.joshbot"
	if err := osMkdirAll(authDir, 0700); err != nil {
		t.Fatalf("mkdir error = %v", err)
	}

	// Write auth data with a different key
	authFile := authDir + "/auth.json"
	if err := osWriteFile(authFile, []byte(`{"some-other-key":{"access_token":"test"}}`), 0600); err != nil {
		t.Fatalf("writeFile error = %v", err)
	}

	token, err := LoadToken(tmpDir)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if token != nil {
		t.Error("expected nil token for missing github-copilot key")
	}
}

func TestAttemptTokenExchange_NetworkError(t *testing.T) {
	// Use a URL that will fail to connect
	oldURL := AccessTokenURL
	AccessTokenURL = "http://127.0.0.1:1/nonexistent"
	defer func() { AccessTokenURL = oldURL }()

	client := &http.Client{Timeout: 1 * time.Second}
	_, _, err := attemptTokenExchange(context.Background(), client, "test-device-code")
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

func TestInitiateDeviceFlow_RequestCreationError(t *testing.T) {
	// Use an invalid URL to trigger request creation error
	oldURL := DeviceCodeURL
	DeviceCodeURL = "http://[::1]:namedport"
	defer func() { DeviceCodeURL = oldURL }()

	_, err := InitiateDeviceFlow(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestCopilotProvider_Chat_RequestCreationError(t *testing.T) {
	// Use an invalid URL to trigger request creation error
	oldURL := CopilotAPIURL
	CopilotAPIURL = "http://[::1]:namedport"
	defer func() { CopilotAPIURL = oldURL }()

	p := NewCopilotProvider(providers.Config{Model: "gpt-4o"}, "token")
	_, err := p.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestListModels_RequestCreationError(t *testing.T) {
	oldURL := copilotCatalogURL
	copilotCatalogURL = "http://[::1]:namedport"
	defer func() { copilotCatalogURL = oldURL }()

	_, err := ListModels("token")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

// Helper functions
func osMkdirAll(path string, perm uint32) error {
	return os.MkdirAll(path, os.FileMode(perm))
}

func osWriteFile(path string, data []byte, perm uint32) error {
	return os.WriteFile(path, data, os.FileMode(perm))
}
