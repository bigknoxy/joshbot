package providers

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"
)

func TestIsFallbackError_NilError(t *testing.T) {
	if IsFallbackError(nil, "test") {
		t.Error("nil error should not trigger fallback")
	}
}

func TestIsFallbackError_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ctx.Err()
	if IsFallbackError(err, "test") {
		t.Error("context canceled should not trigger fallback")
	}
}

func TestIsFallbackError_ContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	err := ctx.Err()
	if IsFallbackError(err, "test") {
		t.Error("context deadline exceeded should not trigger fallback")
	}
}

func TestIsFallbackError_FallbackError(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		provider       string
		expectFallback bool
	}{
		{"rate_limit_429", 429, "openrouter", true},
		{"server_500", 500, "openrouter", true},
		{"server_502", 502, "openrouter", true},
		{"server_503", 503, "openrouter", true},
		{"server_504", 504, "openrouter", true},
		{"timeout_408", 408, "openrouter", true},
		{"overloaded_529", 529, "openrouter", true},
		{"auth_401", 401, "openrouter", false},
		{"forbidden_403", 403, "openrouter", false},
		{"bad_request_400", 400, "openrouter", false},
		// Ollama special case: 404 should NOT fallback
		{"ollama_404", 404, "ollama", false},
		{"ollama_429", 429, "ollama", true},
		{"ollama_500", 500, "ollama", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &FallbackError{
				StatusCode: tt.statusCode,
				Message:    "test error",
				Provider:   tt.provider,
			}
			result := IsFallbackError(err, tt.provider)
			if result != tt.expectFallback {
				t.Errorf("IsFallbackError() = %v, want %v for status %d on %s",
					result, tt.expectFallback, tt.statusCode, tt.provider)
			}
		})
	}
}

func TestIsFallbackError_NetworkErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// net.OpError with DeadlineExceeded should NOT fallback - context deadline takes precedence
		{"net_timeout_deadline_exceeded", &net.OpError{Err: context.DeadlineExceeded}, false},
		{"net_temp", &net.OpError{Err: errors.New("temporary")}, true},
		// url.Error with DeadlineExceeded should NOT fallback - context deadline takes precedence
		{"url_timeout_deadline_exceeded", &url.Error{Op: "dial", Err: context.DeadlineExceeded}, false},
		{"url_temporary", &url.Error{Op: "dial", Err: errors.New("temporary")}, true},
		{"connection_refused", errors.New("connection refused"), true},
		{"connection_reset", errors.New("connection reset by peer"), true},
		{"timeout_error", errors.New("i/o timeout"), true},
		{"no_such_host", errors.New("no such host"), true},
		{"eof_error", errors.New("EOF"), true},
		{"dial_tcp", errors.New("dial tcp"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsFallbackError(tt.err, "test")
			if got != tt.want {
				t.Errorf("IsFallbackError(%T) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestShouldFallback(t *testing.T) {
	// Test the ShouldFallback function directly
	tests := []struct {
		provider   string
		statusCode int
		message    string
		expectTrue bool
	}{
		{"openrouter", 429, "", true},
		{"openrouter", 500, "", true},
		{"openrouter", 502, "", true},
		{"openrouter", 503, "", true},
		{"openrouter", 504, "", true},
		{"openrouter", 408, "", true},
		{"openrouter", 529, "", true},
		{"openrouter", 401, "", false},
		{"openrouter", 403, "", false},
		{"openrouter", 400, "", false},
		// Ollama exceptions
		{"ollama", 404, "", false},
		{"ollama", 429, "", true},
		{"ollama", 500, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"_"+tostring(tt.statusCode), func(t *testing.T) {
			got := ShouldFallback(tt.provider, tt.statusCode, tt.message)
			if got != tt.expectTrue {
				t.Errorf("ShouldFallback(%s, %d) = %v, want %v",
					tt.provider, tt.statusCode, got, tt.expectTrue)
			}
		})
	}
}

func TestExtractStatusCode(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		wantCode int
	}{
		{"openai_style", "API error (429): rate limited", 429},
		{"status_word", "status 503: service unavailable", 503},
		{"http_word", "HTTP 500: internal error", 500},
		{"api_failed", "API request failed with status 404", 404},
		{"no_match", "some random error", 0},
		{"empty", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStatusCode(tt.errMsg)
			if got != tt.wantCode {
				t.Errorf("extractStatusCode(%q) = %d, want %d", tt.errMsg, got, tt.wantCode)
			}
		})
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "none"},
		{"rate_limit", &FallbackError{StatusCode: 429, Message: "rate limited"}, "rate_limit"},
		{"server_500", &FallbackError{StatusCode: 500, Message: "error"}, "server_error"},
		{"server_502", &FallbackError{StatusCode: 502, Message: "error"}, "server_error"},
		{"server_503", &FallbackError{StatusCode: 503, Message: "error"}, "server_error"},
		{"server_504", &FallbackError{StatusCode: 504, Message: "error"}, "server_error"},
		{"timeout", &FallbackError{StatusCode: 408, Message: "timeout"}, "timeout"},
		{"network", errors.New("connection refused"), "network_error"},
		{"unknown", &FallbackError{StatusCode: 418, Message: "teapot"}, "http_418"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err)
			if got != tt.want {
				t.Errorf("ClassifyError(%T) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestFallbackError_Error(t *testing.T) {
	tests := []struct {
		name          string
		err           FallbackError
		expectHasHTTP bool
	}{
		{"with_status", FallbackError{StatusCode: 429, Provider: "test", Model: "model", Message: "rate limited"}, true},
		{"network_error", FallbackError{StatusCode: 0, Provider: "test", Model: "model", Message: "connection refused"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errStr := tt.err.Error()
			if tt.expectHasHTTP {
				if errStr == "" || errStr == "[test/model] network error: " {
					t.Errorf("Error() = %q, expected HTTP status", errStr)
				}
			} else {
				if errStr == "" {
					t.Errorf("Error() = empty")
				}
			}
		})
	}
}

func TestFallbackError_Unwrap(t *testing.T) {
	original := errors.New("original error")
	err := &FallbackError{
		StatusCode: 500,
		Provider:   "test",
		Model:      "model",
		Message:    "server error",
		Cause:      original,
	}

	got := errors.Unwrap(err)
	if got != original {
		t.Errorf("Unwrap() = %v, want %v", got, original)
	}
}

func tostring(n int) string {
	if n == 0 {
		return "zero"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
