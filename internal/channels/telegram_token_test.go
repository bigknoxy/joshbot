package channels

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// validTestToken is a token that passes validateTokenFormat; it is never sent
// over the wire by these tests because every HTTP path goes through an
// httptest server.
const validTestToken = "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZabcde"

func TestValidateTokenFormat(t *testing.T) {
	cases := []struct {
		name  string
		token string
		ok    bool
	}{
		{"empty", "", false},
		{"no colon", "invalid_token", false},
		{"secret too short", "1234:ABC", false},
		{"non-numeric id", "abc123:ABCDEFGHIJKLMNOPQRSTUVWXYZabcde", false},
		{"forbidden chars", "1234:ABCDEFGHIJKLMNOPQRSTUVWXYZabcde!", false},
		{"url-breaking slash", "1234:ABCDEFGHIJKLMNOPQRSTUVWXYZ/abcde", false},
		{"leading space", " 1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZabcde", false},
		{"valid numeric id and secret", validTestToken, true},
		{"valid with dash", "1234:ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcd", true},
		{"valid with underscore", "1234:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcd", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTokenFormat(tc.token)
			if tc.ok && err != nil {
				t.Errorf("validateTokenFormat(%q) = %v, want nil", tc.token, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("validateTokenFormat(%q) = nil, want error", tc.token)
			}
		})
	}
}

// A real getMe success must be reported as valid.
func TestValidateToken_Valid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/bot"+validTestToken+"/getMe") {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":1234567890,"is_bot":true,"username":"joshbot7_bot"}}`)
	}))
	defer srv.Close()

	if err := validateTokenWith(validTestToken, srv.URL, &http.Client{Timeout: 2 * time.Second}); err != nil {
		t.Errorf("validateTokenWith = %v, want nil", err)
	}
}

// A definite rejection (bad token) must not be retried: the API answered, so
// the token is bad and another attempt cannot change that.
func TestValidateToken_APIRejection_IsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	}))
	defer srv.Close()

	err := validateTokenWith(validTestToken, srv.URL, &http.Client{Timeout: 2 * time.Second})
	if err == nil {
		t.Fatal("expected the API rejection to surface as an error")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error should carry the API description, got %v", err)
	}
	if calls != 1 {
		t.Errorf("API rejection hit the server %d times, want 1", calls)
	}
}

// A connectivity failure is retried: this is the failure mode from the field —
// a valid token rejected by onboard because one getMe attempt hit a TLS
// handshake timeout. The second attempt succeeding must be reported as valid.
func TestValidateToken_TransportFailure_RetriesUntilSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// First attempt: the connection dies mid-flight, simulating a
			// dropped TLS handshake. Hijack so the client sees a transport
			// error rather than a 5xx response.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("httptest server cannot hijack")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":1234567890,"is_bot":true}}`)
	}))
	defer srv.Close()

	start := time.Now()
	err := validateTokenWith(validTestToken, srv.URL, &http.Client{Timeout: 2 * time.Second})
	if err != nil {
		t.Errorf("validateTokenWith = %v, want nil after retry", err)
	}
	if calls < 2 {
		t.Errorf("expected at least 2 attempts, got %d", calls)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("retry backoff was not applied (elapsed %v)", elapsed)
	}
}

// When the network is down for all attempts, the error must never contain the
// token. The old implementation embedded the full request URL — including the
// credential — in the transport error, which ended up printed in the setup
// output users paste into issues.
func TestValidateToken_TransportFailure_DoesNotLeakToken(t *testing.T) {
	// A closed server's port refuses connections: a dial error, no chance of
	// a response, and every attempt fails fast.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	err := validateTokenWith(validTestToken, closedURL, &http.Client{Timeout: 2 * time.Second})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), validTestToken) {
		t.Errorf("error leaks the token: %v", err)
	}
	if !IsNetworkError(err) {
		t.Errorf("transport failure should report IsNetworkError, got %v", err)
	}
}

// A result that is ok but not a bot is a definite answer, not a network issue.
func TestValidateToken_NonBotResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":123,"is_bot":false}}`)
	}))
	defer srv.Close()

	err := validateTokenWith(validTestToken, srv.URL, &http.Client{Timeout: 2 * time.Second})
	if err == nil {
		t.Fatal("expected an error for a non-bot result")
	}
	if !strings.Contains(err.Error(), "not belong to a bot") {
		t.Errorf("unexpected error: %v", err)
	}
	if IsNetworkError(err) {
		t.Errorf("a non-bot result is a definite answer, not a network error")
	}
}

// IsNetworkError must only be true for transport failures, not for offline
// format rejections or API rejections.
func TestIsNetworkError_Classification(t *testing.T) {
	rejectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":false,"description":"Not Found"}`)
	}))
	defer rejectSrv.Close()

	if err := validateTokenWith(validTestToken, rejectSrv.URL, &http.Client{Timeout: 2 * time.Second}); err != nil {
		if IsNetworkError(err) {
			t.Errorf("API rejection misclassified as network error: %v", err)
		}
	}

	if err := validateTokenFormat("invalid_token"); err != nil {
		if IsNetworkError(err) {
			t.Errorf("format rejection misclassified as network error: %v", err)
		}
	}
}
