package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/output"
)

// GitHub Copilot is the one provider whose readiness is not an API key in the
// config file: it is a token on disk put there by the device flow. So its line
// in `configure --list` is the only place an operator can find out whether
// `joshbot auth github-copilot` still needs running. Reporting "configured"
// off the config entry alone would say ready for a provider that will fail on
// its first request, and reporting oauth_required for an authenticated install
// sends the operator through the device flow for nothing.

func listedStatus(t *testing.T, out, name string) string {
	t.Helper()
	var doc output.Providers
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("--list --output json did not produce a document: %v\n%s", err, out)
	}
	for _, p := range doc.Providers {
		if p.Name == name {
			return p.Status
		}
	}
	t.Fatalf("provider %q missing from the document:\n%s", name, out)
	return ""
}

func TestConfigureListReportsCopilotAuthState(t *testing.T) {
	cases := []struct {
		name  string
		token string // empty means no token file at all
		want  string
	}{
		{name: "no token on disk", token: "", want: output.ProviderOAuthRequired},
		{name: "device flow completed", token: "gho_test_token", want: output.ProviderAuthenticated},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := withTempHome(t)
			// copilotAuthenticated resolves the token path from $HOME.
			t.Setenv("HOME", home)
			if tc.token != "" {
				writeCopilotToken(t, home, tc.token, 0)
			}

			// Enabling it is what puts it in play; without this it is simply
			// not configured and the auth state is never consulted. The key
			// is a placeholder: Copilot's real credential is the token on
			// disk, and the listing must consult that, not this.
			if _, err := runConfigureCmd(t, "--provider", "github-copilot", "--api-key", "placeholder"); err != nil {
				t.Fatalf("seed: %v", err)
			}

			out, err := runConfigureCmd(t, "--list", "--output", "json")
			if err != nil {
				t.Fatalf("configure --list: %v", err)
			}
			if got := listedStatus(t, out, "github-copilot"); got != tc.want {
				t.Errorf("github-copilot status = %q, want %q", got, tc.want)
			}
			// The token is a credential like any other and must not ride out
			// in a document made to be pasted into an issue.
			if tc.token != "" && strings.Contains(out, tc.token) {
				t.Errorf("--list leaked the Copilot token:\n%s", out)
			}
		})
	}
}

// The text renderer is the default and the one a human actually reads, so the
// same two facts have to hold there: no credential, and a visible provider list.
func TestConfigureListTextDoesNotLeakTheAPIKey(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)

	const key = "sk-secret-0123456789"
	if _, err := runConfigureCmd(t, "--provider", "openrouter", "--api-key", key); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := runConfigureCmd(t, "--list")
	if err != nil {
		t.Fatalf("configure --list: %v", err)
	}
	if strings.Contains(out, key) {
		t.Errorf("the text listing printed the API key:\n%s", out)
	}
	if !strings.Contains(out, "openrouter") {
		t.Errorf("the configured provider is not in the listing at all:\n%s", out)
	}
}
