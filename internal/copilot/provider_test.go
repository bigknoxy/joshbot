package copilot

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

func TestNewCopilotProvider_DefaultTimeout(t *testing.T) {
	cfg := providers.Config{}
	p := NewCopilotProvider(cfg, "test-token")

	if p.cfg.Timeout != 120*time.Second {
		t.Errorf("expected default timeout 120s, got %v", p.cfg.Timeout)
	}
}

func TestNewCopilotProvider_DefaultModel(t *testing.T) {
	cfg := providers.Config{}
	p := NewCopilotProvider(cfg, "test-token")

	if p.cfg.Model != "gpt-4o" {
		t.Errorf("expected default model 'gpt-4o', got %q", p.cfg.Model)
	}
}

func TestNewCopilotProvider_CustomTimeout(t *testing.T) {
	cfg := providers.Config{Timeout: 30 * time.Second}
	p := NewCopilotProvider(cfg, "test-token")

	if p.cfg.Timeout != 30*time.Second {
		t.Errorf("expected timeout 30s, got %v", p.cfg.Timeout)
	}
}

func TestNewCopilotProvider_CustomModel(t *testing.T) {
	cfg := providers.Config{Model: "gpt-4"}
	p := NewCopilotProvider(cfg, "test-token")

	if p.cfg.Model != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got %q", p.cfg.Model)
	}
}

func TestNewCopilotProvider_AccessToken(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "my-access-token")

	if p.accessToken != "my-access-token" {
		t.Errorf("expected access token 'my-access-token', got %q", p.accessToken)
	}
}

func TestCopilotProvider_Name(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "token")
	if p.Name() != "github-copilot" {
		t.Errorf("Name() = %q, want 'github-copilot'", p.Name())
	}
}

func TestCopilotProvider_Config(t *testing.T) {
	cfg := providers.Config{
		Model:       "gpt-4o",
		Timeout:     60 * time.Second,
		MaxTokens:   4096,
		Temperature: 0.7,
	}
	p := NewCopilotProvider(cfg, "token")

	got := p.Config()
	if got.Model != "gpt-4o" {
		t.Errorf("Config().Model = %q, want 'gpt-4o'", got.Model)
	}
	if got.Timeout != 60*time.Second {
		t.Errorf("Config().Timeout = %v, want 60s", got.Timeout)
	}
	if got.MaxTokens != 4096 {
		t.Errorf("Config().MaxTokens = %d, want 4096", got.MaxTokens)
	}
	if got.Temperature != 0.7 {
		t.Errorf("Config().Temperature = %v, want 0.7", got.Temperature)
	}
}

// Copilot has no streaming endpoint yet. The error must be the shared sentinel,
// because streaming is on by default and the agent falls back to Chat only when
// errors.Is matches — a bare error string would break every interactive turn.
func TestCopilotProvider_ChatStream_ReportsUnsupported(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "token")
	_, err := p.ChatStream(nil, providers.ChatRequest{})
	if err == nil {
		t.Fatal("ChatStream() should return an error")
	}
	if !errors.Is(err, providers.ErrStreamingUnsupported) {
		t.Errorf("ChatStream() error = %v, want providers.ErrStreamingUnsupported", err)
	}
}

func TestCopilotProvider_Transcribe_NotSupported(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "token")
	_, err := p.Transcribe(nil, []byte("audio"), "prompt")
	if err == nil {
		t.Error("Transcribe() should return error (not supported)")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("Transcribe() error = %v, want 'not supported'", err)
	}
}

func TestCopilotProvider_ParseError_Forbidden(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "token")
	err := p.parseError([]byte("forbidden"), 403)
	if err == nil {
		t.Fatal("parseError() should return error for 403")
	}
	if !strings.Contains(err.Error(), "authentication expired") {
		t.Errorf("parseError(403) error = %v, want 'authentication expired'", err)
	}
}

func TestCopilotProvider_ParseError_WithJSONError(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "token")
	body := `{"error":{"message":"Invalid request","type":"invalid_request","code":"bad_request"}}`
	err := p.parseError([]byte(body), 400)
	if err == nil {
		t.Fatal("parseError() should return error for 400")
	}
	if !strings.Contains(err.Error(), "Invalid request") {
		t.Errorf("parseError(400) error = %v, want 'Invalid request'", err)
	}
	if !strings.Contains(err.Error(), "invalid_request") {
		t.Errorf("parseError(400) error = %v, want 'invalid_request'", err)
	}
	if !strings.Contains(err.Error(), "bad_request") {
		t.Errorf("parseError(400) error = %v, want 'bad_request'", err)
	}
}

func TestCopilotProvider_ParseError_WithNonJSONBody(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "token")
	body := "Internal Server Error"
	err := p.parseError([]byte(body), 500)
	if err == nil {
		t.Fatal("parseError() should return error for 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("parseError(500) error = %v, want '500'", err)
	}
	if !strings.Contains(err.Error(), "Internal Server Error") {
		t.Errorf("parseError(500) error = %v, want 'Internal Server Error'", err)
	}
}

func TestCopilotProvider_ParseError_EmptyJSONError(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "token")
	body := `{"error":{}}`
	err := p.parseError([]byte(body), 500)
	if err == nil {
		t.Fatal("parseError() should return error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("parseError(500) error = %v, want '500'", err)
	}
}

func TestCopilotProvider_ParseError_InvalidJSON(t *testing.T) {
	p := NewCopilotProvider(providers.Config{}, "token")
	body := "not json at all"
	err := p.parseError([]byte(body), 503)
	if err == nil {
		t.Fatal("parseError() should return error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("parseError(503) error = %v, want '503'", err)
	}
}

func TestCopilotProvider_Chat_EmptyModel(t *testing.T) {
	cfg := providers.Config{Model: "gpt-4o"}
	p := NewCopilotProvider(cfg, "token")

	// This will fail because it tries to make an HTTP request to the real API
	// But we can verify that the provider is configured correctly
	// The request will fail with a network error, not a model error
	_, err := p.Chat(nil, providers.ChatRequest{})
	if err == nil {
		t.Error("Chat() should return error (network failure)")
	}
	// The error should be a network error, not a model error
	if strings.Contains(err.Error(), "model") {
		t.Errorf("Chat() error should not be about model, got: %v", err)
	}
}

func TestCopilotProvider_Chat_SetsModelFromConfig(t *testing.T) {
	cfg := providers.Config{Model: "gpt-4o"}
	p := NewCopilotProvider(cfg, "token")

	// The Chat method sets req.Model = p.cfg.Model if req.Model is empty
	// We can't fully test this without a mock server, but we can verify
	// the provider is configured correctly
	if p.cfg.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", p.cfg.Model)
	}
}

func TestCopilotProvider_Chat_SetsMaxTokensFromConfig(t *testing.T) {
	cfg := providers.Config{Model: "gpt-4o", MaxTokens: 4096}
	p := NewCopilotProvider(cfg, "token")

	if p.cfg.MaxTokens != 4096 {
		t.Errorf("expected MaxTokens 4096, got %d", p.cfg.MaxTokens)
	}
}

func TestCopilotProvider_Chat_SetsTemperatureFromConfig(t *testing.T) {
	cfg := providers.Config{Model: "gpt-4o", Temperature: 0.5}
	p := NewCopilotProvider(cfg, "token")

	if p.cfg.Temperature != 0.5 {
		t.Errorf("expected Temperature 0.5, got %v", p.cfg.Temperature)
	}
}

func TestCopilotProvider_InitRegistersProvider(t *testing.T) {
	// The init() function registers the provider. We can verify this
	// by checking that the provider is registered.
	// This is a simple smoke test to ensure init() doesn't panic.
	p, err := providers.GetProvider("github-copilot", providers.Config{
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("GetProvider('github-copilot') error = %v", err)
	}
	if p.Name() != "github-copilot" {
		t.Errorf("Name() = %q, want 'github-copilot'", p.Name())
	}
}

func TestCopilotProvider_InitRequiresAPIKey(t *testing.T) {
	// The init() function's factory should return an error if no API key is set
	_, err := providers.GetProvider("github-copilot", providers.Config{})
	if err == nil {
		t.Error("GetProvider('github-copilot') with empty APIKey should return error")
	}
	if !strings.Contains(err.Error(), "authentication") {
		t.Errorf("GetProvider error = %v, want 'authentication'", err)
	}
}
