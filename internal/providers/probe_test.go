package providers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// probeServer answers /chat/completions with the given status, and /models
// with 200 regardless — the exact behaviour (OpenRouter's) that made
// ListModels-based validation a false positive.
func probeServer(t *testing.T, chatStatus int, sawAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
			return
		}
		if sawAuth != nil {
			*sawAuth = r.Header.Get("Authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		if chatStatus != http.StatusOK {
			w.WriteHeader(chatStatus)
			fmt.Fprintf(w, `{"error":{"message":"status %d"}}`, chatStatus)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"x"}}]}`)
	}))
}

func TestProbeCredentialAcceptsValidKey(t *testing.T) {
	var auth string
	srv := probeServer(t, http.StatusOK, &auth)
	defer srv.Close()
	if err := ProbeCredential(Config{APIBase: srv.URL, APIKey: "good", Model: "m"}); err != nil {
		t.Fatalf("ProbeCredential = %v", err)
	}
	if auth != "Bearer good" {
		t.Errorf("probe did not send the credential: %q", auth)
	}
}

func TestProbeCredentialRejects401(t *testing.T) {
	srv := probeServer(t, http.StatusUnauthorized, nil)
	defer srv.Close()
	err := ProbeCredential(Config{APIBase: srv.URL, APIKey: "typo", Model: "m"})
	if !errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("a 401 must be ErrCredentialRejected, got %v", err)
	}
}

func TestProbeCredentialRejects403(t *testing.T) {
	srv := probeServer(t, http.StatusForbidden, nil)
	defer srv.Close()
	if err := ProbeCredential(Config{APIBase: srv.URL, APIKey: "k", Model: "m"}); !errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("a 403 must be ErrCredentialRejected, got %v", err)
	}
}

func TestProbeCredentialTreatsRateLimitAsAuthenticated(t *testing.T) {
	srv := probeServer(t, http.StatusTooManyRequests, nil)
	defer srv.Close()
	if err := ProbeCredential(Config{APIBase: srv.URL, APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("a 429 is issued to an authenticated key, got %v", err)
	}
}

func TestProbeCredentialIndeterminateOnOtherErrors(t *testing.T) {
	srv := probeServer(t, http.StatusNotFound, nil)
	defer srv.Close()
	err := ProbeCredential(Config{APIBase: srv.URL, APIKey: "k", Model: "wrong"})
	if err == nil {
		t.Fatal("a 404 must not print a checkmark")
	}
	if errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("a 404 is not a rejected credential: %v", err)
	}
	if !strings.Contains(err.Error(), "could not verify") {
		t.Errorf("indeterminate error should say could not verify: %v", err)
	}
}

func TestProbeCredentialRefusesEmptyBase(t *testing.T) {
	if err := ProbeCredential(Config{APIKey: "k", Model: "m"}); err == nil {
		t.Fatal("empty API base must refuse, never dial a default endpoint")
	}
}
