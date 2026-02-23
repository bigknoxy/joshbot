package providers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// FallbackError wraps an error with context for fallback decisions
type FallbackError struct {
	StatusCode int    // HTTP status code (0 for network errors)
	Message    string // Error message
	Provider   string // Provider that returned the error
	Model      string // Model that was being used
	Cause      error  // Underlying error
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
func IsFallbackError(err error) bool {
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
		return isFallbackStatusCode(fallbackErr.StatusCode)
	}

	// Parse HTTP status from error message
	statusCode := extractStatusCode(err.Error())
	if statusCode > 0 {
		return isFallbackStatusCode(statusCode)
	}

	// Network errors (no status code) - fallback
	if isNetworkError(err) {
		return true
	}

	return false
}

// isFallbackStatusCode returns true for status codes that should trigger fallback.
func isFallbackStatusCode(statusCode int) bool {
	switch statusCode {
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

// extractStatusCode parses HTTP status code from error message.
func extractStatusCode(errMsg string) int {
	patterns := []string{
		`API error \((\d{3})\)`,
		`status (\d{3})`,
		`HTTP (\d{3})`,
		`API request failed with status (\d{3})`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(errMsg); len(matches) > 1 {
			code, _ := strconv.Atoi(matches[1])
			return code
		}
	}

	return 0
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
