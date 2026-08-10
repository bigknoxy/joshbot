package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/copilot"
	"github.com/bigknoxy/joshbot/internal/output"
)

// writeCopilotToken plants a copilot auth.json under home. expiresAt of 0
// means "no expiry recorded"; a past value exercises the expired branch.
func writeCopilotToken(t *testing.T, home, accessToken string, expiresAt int64) {
	t.Helper()
	dir := filepath.Join(home, ".joshbot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := json.Marshal(copilot.AuthData{
		"github-copilot": {AccessToken: accessToken, ExpiresAt: expiresAt},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), body, 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
}

// `auth status --output json` is a script surface, and it is the only way to
// find out whether the device flow needs re-running. Two things must hold: the
// document shape, and that a token file whose access_token is empty reports
// NOT authenticated — a presence-only check ("the file exists") would send the
// operator away believing Copilot works while every request 401s.
func TestAuthStatusJSONReportsRealTokenState(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		expiresAt int64
		write     bool
		want      bool
	}{
		{name: "no token file", want: false},
		{name: "token file with empty access_token", write: true, token: "", want: false},
		{name: "expired token", write: true, token: "gho_expired", expiresAt: 1, want: false},
		{name: "usable token", write: true, token: "gho_abcdefghijklmnop", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if tt.write {
				writeCopilotToken(t, home, tt.token, tt.expiresAt)
			}

			out, err := runReportCmd(t, runAuthStatus, "--output", "json")
			if err != nil {
				t.Fatalf("auth status: %v", err)
			}

			var doc output.Auth
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
				t.Fatalf("--output json did not produce a document: %v\n%s", err, out)
			}
			if doc.SchemaVersion != output.SchemaVersion {
				t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, output.SchemaVersion)
			}
			if len(doc.Providers) != 1 || doc.Providers[0].Name != "github-copilot" {
				t.Fatalf("providers = %+v, want exactly github-copilot", doc.Providers)
			}
			if doc.Providers[0].Authenticated != tt.want {
				t.Errorf("authenticated = %v, want %v", doc.Providers[0].Authenticated, tt.want)
			}
			if tt.token != "" && strings.Contains(out, tt.token) {
				t.Errorf("auth status leaked the access token:\n%s", out)
			}
		})
	}
}

// A typo in --output must be a validation failure (exit 2) rather than being
// reported as an authentication problem.
func TestAuthStatusInvalidOutputExitsValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := runReportCmd(t, runAuthStatus, "--output", "xml")
	if err == nil {
		t.Fatal("an unknown --output value was accepted")
	}
	if got := exitCodeOf(t, err); got != exitValidation {
		t.Errorf("exit code = %d, want %d (exitValidation)", got, exitValidation)
	}
}

// The credential check behind the configure wizard must not print "validated"
// over a rejected key or a server error. Each branch has a distinct message
// because the operator's next action differs: fix the key, retry, or check the
// endpoint.
func TestValidateProviderCredentials_FailureBranches(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		provider string
		wantErr  string
	}{
		{"rejected key", http.StatusUnauthorized, "openrouter", "invalid API key"},
		{"server error", http.StatusInternalServerError, "openrouter", "unexpected status code: 500"},
		{"forbidden is not 'invalid key'", http.StatusForbidden, "groq", "unexpected status code: 403"},
		{"ollama server unhappy", http.StatusInternalServerError, "ollama", "unexpected status code: 500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			err := validateProviderCredentials(tt.provider, "sk-whatever", srv.URL)
			if err == nil {
				t.Fatalf("a %d response was reported as valid credentials", tt.status)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// A 200 is the only thing that counts as validated, and the key must actually
// be sent — a check that never sets Authorization would "validate" any key
// against an endpoint that ignores it.
func TestValidateProviderCredentials_SendsBearerAndAcceptsOK(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := validateProviderCredentials("openrouter", "sk-live-key", srv.URL); err != nil {
		t.Fatalf("a 200 was not accepted: %v", err)
	}
	if gotAuth != "Bearer sk-live-key" {
		t.Errorf("Authorization = %q, want the bearer key", gotAuth)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, want /models", gotPath)
	}
}

// An unreachable Ollama must say so by address; "unexpected status code: 0" or
// a nil error would both send the operator looking in the wrong place.
func TestValidateProviderCredentials_OllamaUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close() // nothing is listening now

	err := validateProviderCredentials("ollama", "", addr)
	if err == nil {
		t.Fatal("an unreachable Ollama was reported as valid")
	}
	if !strings.Contains(err.Error(), "cannot connect to Ollama") || !strings.Contains(err.Error(), addr) {
		t.Errorf("error should name the address it could not reach, got %v", err)
	}
}

// A provider with no validation endpoint must not be reported as validated by
// dialling somebody else's API. providers.ListModels was changed to refuse an
// empty APIBase for the same reason; this pins that the switch has no
// accidental fallthrough that hits the network for an unknown provider.
func TestValidateProviderCredentials_UnknownProviderMakesNoRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer srv.Close()

	if err := validateProviderCredentials("custom", "sk-x", srv.URL); err != nil {
		t.Fatalf("unknown provider should be a no-op, got %v", err)
	}
	if called {
		t.Error("an unvalidatable provider issued an HTTP request anyway")
	}
}
