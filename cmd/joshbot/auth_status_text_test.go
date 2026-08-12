package main

import (
	"strings"
	"testing"
)

// The text rendering is the default, so it is what an operator sees when they
// run `joshbot auth status` after a device flow they are not sure completed.
// It has to distinguish the two states in words a human can act on, and it must
// not print the token itself: this is the first command anyone pastes into an
// issue when Copilot stops answering.

func TestAuthStatusTextDistinguishesTheTwoStates(t *testing.T) {
	const token = "gho_abcdefghijklmnop"

	t.Run("authenticated", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeCopilotToken(t, home, token, 0)

		out, err := runReportCmd(t, runAuthStatus)
		if err != nil {
			t.Fatalf("auth status: %v", err)
		}
		if strings.Contains(out, token) {
			t.Errorf("auth status printed the access token:\n%s", out)
		}
		if !strings.Contains(out, "GitHub Copilot") {
			t.Errorf("the provider is not named in the output:\n%s", out)
		}
		authed := out
		if !strings.Contains(strings.ToLower(out), "authenticated") || strings.Contains(strings.ToLower(out), "not authenticated") {
			t.Errorf("a usable token was reported as not authenticated:\n%s", out)
		}

		// And the unauthenticated run must not render identically, or the
		// command tells the operator nothing.
		home2 := t.TempDir()
		t.Setenv("HOME", home2)
		out2, err := runReportCmd(t, runAuthStatus)
		if err != nil {
			t.Fatalf("auth status: %v", err)
		}
		if out2 == authed {
			t.Errorf("authenticated and unauthenticated render the same text:\n%s", out2)
		}
	})
}
