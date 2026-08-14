package providers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ParseRetryAfter reads a Retry-After header value: either delay-seconds
// ("120") or an HTTP-date. Returns zero for anything unparseable, absent, or
// in the past — a zero simply means "no upstream guidance".
func ParseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// ErrStreamingUnsupported is returned by ChatStream on a provider that has no
// streaming endpoint. Streaming is on by default, so a provider that simply
// errors here kills every interactive turn; callers detect this sentinel with
// errors.Is and fall back to Chat instead.
var ErrStreamingUnsupported = errors.New("streaming not supported by this provider")

// extractStatusCode parses HTTP status code from error message.
// Avoids regex compilation overhead by using simple string scanning.
func extractStatusCode(errMsg string) int {
	// Try specific patterns first (most reliable)
	prefixes := []string{
		"API request failed with status ",
		"API error (",
		"status ",
		"HTTP ",
	}
	for _, prefix := range prefixes {
		if idx := strings.Index(errMsg, prefix); idx != -1 {
			rest := errMsg[idx+len(prefix):]
			code := scanStatusCode(rest)
			if code > 0 {
				return code
			}
		}
	}

	// Fallback: scan for any 3-digit number that looks like an HTTP status code
	return scanAnyStatusCode(errMsg)
}

// scanStatusCode reads a 3-digit status code from the start of s.
func scanStatusCode(s string) int {
	if len(s) < 3 {
		return 0
	}
	code, err := strconv.Atoi(s[:3])
	if err == nil && code >= 100 && code < 600 {
		return code
	}
	return 0
}

// scanAnyStatusCode scans for any 3-digit HTTP status code in the string.
// Only considers codes near HTTP-related keywords to avoid false matches
// on port numbers, version numbers, etc.
func scanAnyStatusCode(s string) int {
	indicators := []string{"status", "http", "error", "code", "got", "returned", "response", "failed", "received"}
	lower := strings.ToLower(s)

	for _, ind := range indicators {
		idx := strings.Index(lower, ind)
		if idx == -1 {
			continue
		}
		// Search before and after the indicator keyword
		searchStart := max(0, idx-10)
		searchEnd := min(idx+len(ind)+30, len(s))
		for i := searchStart; i+2 < searchEnd; i++ {
			if s[i] >= '1' && s[i] <= '5' {
				code, err := strconv.Atoi(s[i : i+3])
				if err == nil && code >= 100 && code < 600 {
					return code
				}
			}
		}
	}
	return 0
}

// FallbackError wraps an error with context for fallback decisions
type FallbackError struct {
	StatusCode int    // HTTP status code (0 for network errors)
	Message    string // Error message
	Provider   string // Provider that returned the error
	Model      string // Model that was being used
	Cause      error  // Underlying error
	// RetryAfter carries the upstream Retry-After header when the provider
	// sent one (zero otherwise). It seeds both the in-turn retry delay and
	// the provider cooldown, so a rate limit with a known reset is waited
	// out rather than guessed at.
	RetryAfter time.Duration
}

func (e *FallbackError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("[%s/%s] HTTP %d: %s", e.Provider, e.Model, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("[%s/%s] network error: %s", e.Provider, e.Model, e.Message)
}

func (e *FallbackError) Unwrap() error {
	return e.Cause
}

// IsFallbackError returns true if the error should trigger a fallback to another provider.
// Non-fallback errors (400, 401, 403, context cancelled) are returned immediately.
// The providerName parameter is used to determine provider-specific behavior (e.g., Ollama 404).
func IsFallbackError(err error, providerName string) bool {
	if err == nil {
		return false
	}

	// Context cancellation - never fallback
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Check for FallbackError type
	var fallbackErr *FallbackError
	if errors.As(err, &fallbackErr) {
		return ShouldFallback(fallbackErr.Provider, fallbackErr.StatusCode, fallbackErr.Message)
	}

	// Parse HTTP status from error message
	statusCode := extractStatusCode(err.Error())
	if statusCode > 0 {
		return ShouldFallback(providerName, statusCode, err.Error())
	}

	// Network errors (no status code) - fallback
	if isNetworkError(err) {
		return true
	}

	return false
}

// StatusCodeFromError exposes the HTTP status carried by (or parsed out of)
// an error chain, or 0 when there is none. A FallbackError's structured code
// wins over text scanning.
func StatusCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var fallbackErr *FallbackError
	if errors.As(err, &fallbackErr) && fallbackErr.StatusCode > 0 {
		return fallbackErr.StatusCode
	}
	return extractStatusCode(err.Error())
}

// ProviderFromError names the provider an error chain came from, or "" when
// the error carries no structured provider (plain non-fallback errors).
func ProviderFromError(err error) string {
	var fallbackErr *FallbackError
	if errors.As(err, &fallbackErr) {
		return fallbackErr.Provider
	}
	return ""
}

// RetryAfterFromError extracts the upstream Retry-After hint from an error
// chain, or zero when there is none.
func RetryAfterFromError(err error) time.Duration {
	var fallbackErr *FallbackError
	if errors.As(err, &fallbackErr) {
		return fallbackErr.RetryAfter
	}
	return 0
}

// isFallbackStatusCode returns true for status codes that should trigger fallback.
func isFallbackStatusCode(statusCode int) bool {
	switch statusCode {
	case 410: // Gone (deprecated/removed model)
		return true
	case 429: // Rate limit
		return true
	case 500, 502, 503, 504: // Server errors
		return true
	case 408: // Request timeout
		return true
	case 529: // Overloaded
		return true
	default:
		return false
	}
}

// ShouldFallback determines if an error should trigger fallback to another provider.
// For Ollama, 404 (model not found) should NOT trigger fallback - the user needs to pull the model.
func ShouldFallback(provider string, statusCode int, errMsg string) bool {
	// For Ollama, don't fallback on 404 (model not found)
	// User needs to run: ollama pull <model>
	if provider == "ollama" && statusCode == 404 {
		return false
	}

	return isFallbackStatusCode(statusCode)
}

// isNetworkError checks if the error is a network-level failure.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Check for net.OpError
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Check for URL errors
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Timeout() || urlErr.Temporary()
	}

	// Check error message for network patterns
	errMsg := strings.ToLower(err.Error())
	networkPatterns := []string{
		"connection refused",
		"connection reset",
		"timeout",
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"eof",
		"dial tcp",
	}

	for _, pattern := range networkPatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}

	return false
}

// ClassifyError returns a human-readable error classification.
func ClassifyError(err error) string {
	if err == nil {
		return "none"
	}

	statusCode := extractStatusCode(err.Error())

	switch statusCode {
	case 429:
		return "rate_limit"
	case 500, 502, 503, 504:
		return "server_error"
	case 408:
		return "timeout"
	case 0:
		if isNetworkError(err) {
			return "network_error"
		}
		return "unknown"
	default:
		return fmt.Sprintf("http_%d", statusCode)
	}
}
