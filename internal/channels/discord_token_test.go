package channels

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateDiscordTokenAcceptsAWorkingToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/users/@me" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := validateDiscordTokenWith("a-token", srv.URL, srv.Client()); err != nil {
		t.Fatalf("a 200 must be accepted, got: %v", err)
	}
	if gotAuth != "Bot a-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bot a-token")
	}
}

// An operator who copied the token out of the portal may have the prefix
// already; the check must not turn that into a rejection.
func TestValidateDiscordTokenAcceptsAPrefixedToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := validateDiscordTokenWith("Bot a-token", srv.URL, srv.Client()); err != nil {
		t.Fatalf("a prefixed token must be accepted, got: %v", err)
	}
	if gotAuth != "Bot a-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bot a-token")
	}
}

func TestValidateDiscordTokenRejectsARejectedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := validateDiscordTokenWith("a-token", srv.URL, srv.Client())
	if err == nil {
		t.Fatal("expected a 401 to be reported")
	}
	if !errors.Is(err, ErrDiscordTokenInvalid) {
		t.Errorf("error %q is not an invalid-token error", err)
	}
}

// A rate-limited request proves nothing about the token. Reporting it as
// invalid would send the operator hunting for a typo that is not there.
func TestValidateDiscordTokenReportsARateLimitAsUnverified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	err := validateDiscordTokenWith("a-token", srv.URL, srv.Client())
	if err == nil {
		t.Fatal("expected a 429 to be reported")
	}
	if !errors.Is(err, ErrDiscordTokenUnverified) {
		t.Errorf("error %q is not an unverified-token error", err)
	}
	if errors.Is(err, ErrDiscordTokenInvalid) {
		t.Error("a rate limit must not be reported as an invalid token")
	}
}

func TestValidateDiscordTokenReportsAnEmptyTokenAsInvalid(t *testing.T) {
	err := validateDiscordTokenWith("  ", "https://example.invalid", http.DefaultClient)
	if err == nil {
		t.Fatal("expected an empty token to be reported")
	}
	if !errors.Is(err, ErrDiscordTokenInvalid) {
		t.Errorf("error %q is not an invalid-token error", err)
	}
}
