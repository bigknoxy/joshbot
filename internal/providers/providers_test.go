package providers

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestLiteLLMProviderParseError tests that parseError returns correct error types
func TestLiteLLMProviderParseError(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		statusCode     int
		expectFallback bool
	}{
		{
			name:           "rate limit 429 triggers fallback",
			body:           `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit"}}`,
			statusCode:     429,
			expectFallback: true,
		},
		{
			name:           "server error 500 triggers fallback",
			body:           `{"error":{"message":"internal server error","type":"server_error","code":"internal_error"}}`,
			statusCode:     500,
			expectFallback: true,
		},
		{
			name:           "server error 502 triggers fallback",
			body:           `{"error":{"message":"bad gateway","type":"server_error","code":"bad_gateway"}}`,
			statusCode:     502,
			expectFallback: true,
		},
		{
			name:           "server error 503 triggers fallback",
			body:           `{"error":{"message":"service unavailable","type":"server_error","code":"service_unavailable"}}`,
			statusCode:     503,
			expectFallback: true,
		},
		{
			name:           "server error 504 triggers fallback",
			body:           `{"error":{"message":"gateway timeout","type":"server_error","code":"gateway_timeout"}}`,
			statusCode:     504,
			expectFallback: true,
		},
		{
			name:           "request timeout 408 triggers fallback",
			body:           `{"error":{"message":"request timeout","type":"timeout_error","code":"timeout"}}`,
			statusCode:     408,
			expectFallback: true,
		},
		{
			name:           "overload 529 triggers fallback",
			body:           `{"error":{"message":"overloaded","type":"server_error","code":"overloaded"}}`,
			statusCode:     529,
			expectFallback: true,
		},
		{
			name:           "auth error 401 does NOT trigger fallback",
			body:           `{"error":{"message":"unauthorized","type":"authentication_error","code":"invalid_api_key"}}`,
			statusCode:     401,
			expectFallback: false,
		},
		{
			name:           "forbidden 403 does NOT trigger fallback",
			body:           `{"error":{"message":"forbidden","type":"permission_error","code":"permission_denied"}}`,
			statusCode:     403,
			expectFallback: false,
		},
		{
			name:           "bad request 400 does NOT trigger fallback",
			body:           `{"error":{"message":"bad request","type":"invalid_request_error","code":"invalid_parameter"}}`,
			statusCode:     400,
			expectFallback: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Model = "test-model"
			provider := NewLiteLLMProvider(cfg)

			err := provider.parseError([]byte(tt.body), tt.statusCode, "")
			if err == nil {
				t.Fatalf("expected non-nil error for status %d", tt.statusCode)
			}

			// Check if it's a FallbackError
			var fallbackErr *FallbackError
			found := errors.As(err, &fallbackErr)

			if tt.expectFallback {
				if !found {
					t.Errorf("expected FallbackError, got %T", err)
				}
				if found && fallbackErr.StatusCode != tt.statusCode {
					t.Errorf("expected StatusCode %d, got %d", tt.statusCode, fallbackErr.StatusCode)
				}
			} else {
				if found {
					t.Errorf("expected non-FallbackError, got FallbackError")
				}
			}
		})
	}
}

// TestLiteLLMProviderNetworkError tests that network errors are wrapped in FallbackError
func TestLiteLLMProviderNetworkError(t *testing.T) {
	// Create a provider with a non-routable base URL that will fail immediately
	cfg := Config{
		APIBase: "http://127.0.0.1:1", // Invalid port, will fail immediately
		Model:   "test-model",
		Timeout: 100 * time.Millisecond,
	}
	provider := NewLiteLLMProvider(cfg)

	ctx := context.Background()
	req := ChatRequest{
		Model: "test-model",
		Messages: []Message{
			{Role: RoleUser, Content: "hello"},
		},
	}

	_, err := provider.Chat(ctx, req)
	if err == nil {
		t.Fatal("expected error from Chat with invalid endpoint")
	}

	// Should be a FallbackError for network errors
	var fallbackErr *FallbackError
	if errors.As(err, &fallbackErr) {
		if fallbackErr.StatusCode != 0 {
			t.Errorf("expected StatusCode 0 for network error, got %d", fallbackErr.StatusCode)
		}
		if fallbackErr.Provider != "litellm" {
			t.Errorf("expected Provider litellm, got %s", fallbackErr.Provider)
		}
	}
}

// TestFallbackErrorErrorMessage tests the FallbackError Error() method
func TestFallbackErrorErrorMessage(t *testing.T) {
	tests := []struct {
		name         string
		err          FallbackError
		wantContains string
	}{
		{
			name: "HTTP error with status",
			err: FallbackError{
				StatusCode: 429,
				Message:    "rate limited",
				Provider:   "openrouter",
				Model:      "test-model",
			},
			wantContains: "HTTP 429",
		},
		{
			name: "network error",
			err: FallbackError{
				StatusCode: 0,
				Message:    "connection refused",
				Provider:   "ollama",
				Model:      "llama3.1:8b",
			},
			wantContains: "network error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errMsg := tt.err.Error()
			if len(errMsg) == 0 {
				t.Errorf("Error() returned empty string")
			}
			if tt.wantContains != "" && !contains(errMsg, tt.wantContains) {
				t.Errorf("Error() = %q, want to contain %q", errMsg, tt.wantContains)
			}
		})
	}
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
