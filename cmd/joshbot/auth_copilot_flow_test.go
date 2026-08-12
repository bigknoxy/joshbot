package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/copilot"
	"github.com/urfave/cli/v2"
)

// `joshbot auth github-copilot` is the only way onto that provider, and every
// way it can go wrong is expensive: re-running the device flow over a token
// that already works makes the operator approve a browser prompt for nothing,
// and treating a failed model list as a failed authentication throws away a
// token that was just written to disk and sends them round the whole flow
// again. Neither branch had any coverage — the device flow needs a human, so
// the two calls that reach GitHub are package vars for exactly this.

// authCtx builds the cli.Context runAuthCopilot reads, with --force set or not.
func authCtx(t *testing.T, force bool) *cli.Context {
	t.Helper()
	fs := flag.NewFlagSet("github-copilot", flag.ContinueOnError)
	fs.Bool("force", force, "")
	return cli.NewContext(cli.NewApp(), fs, nil)
}

// withCopilotSeams substitutes the two network calls for the duration of a test.
func withCopilotSeams(t *testing.T, flow func(context.Context) (*copilot.TokenInfo, error), models func(string) ([]string, error)) {
	t.Helper()
	origFlow, origModels := copilotRunDeviceFlow, copilotListModels
	copilotRunDeviceFlow, copilotListModels = flow, models
	t.Cleanup(func() { copilotRunDeviceFlow, copilotListModels = origFlow, origModels })
}

// An operator who is already authenticated must not be pushed back through the
// browser. Re-running the flow here is not merely noisy: it replaces a working
// token, so an approval the operator abandons halfway leaves them worse off
// than before they ran the command.
func TestAuthCopilotShortCircuitsWhenAlreadyAuthenticated(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	writeCopilotToken(t, home, "gho-existing", 0)

	called := false
	withCopilotSeams(t,
		func(context.Context) (*copilot.TokenInfo, error) {
			called = true
			return nil, errors.New("the device flow must not run")
		},
		func(string) ([]string, error) { return nil, errors.New("unreachable") })

	var err error
	out := captureStdout(t, func() { err = runAuthCopilot(authCtx(t, false)) })

	if err != nil {
		t.Fatalf("runAuthCopilot() = %v, want nil", err)
	}
	if called {
		t.Error("the device flow ran even though a valid token was on disk")
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("the operator is not told how to re-authenticate:\n%s", out)
	}
}

// --force is the escape hatch for a token that is present but no longer works
// on GitHub's side. Honouring it only when there is no token makes the flag
// useless in exactly the situation it exists for.
func TestAuthCopilotForceReauthenticatesOverAnExistingToken(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	writeCopilotToken(t, home, "gho-stale", 0)

	withCopilotSeams(t,
		func(context.Context) (*copilot.TokenInfo, error) {
			return &copilot.TokenInfo{AccessToken: "gho-fresh"}, nil
		},
		func(string) ([]string, error) { return nil, nil })

	var err error
	captureStdout(t, func() { err = runAuthCopilot(authCtx(t, true)) })
	if err != nil {
		t.Fatalf("runAuthCopilot(--force) = %v, want nil", err)
	}

	tok, err := copilot.LoadToken(home)
	if err != nil || tok == nil {
		t.Fatalf("LoadToken after --force: %v, %v", tok, err)
	}
	if tok.AccessToken != "gho-fresh" {
		t.Errorf("the stale token survived --force: %q", tok.AccessToken)
	}
}

// The model list is a convenience on top of an authentication that has already
// succeeded and whose token is already saved. Returning its error makes the
// command exit non-zero over a transient API blip, and an operator reading that
// as "auth failed" runs the whole browser flow again for nothing.
func TestAuthCopilotKeepsTheTokenWhenTheModelListFails(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)

	withCopilotSeams(t,
		func(context.Context) (*copilot.TokenInfo, error) {
			return &copilot.TokenInfo{AccessToken: "gho-new"}, nil
		},
		func(string) ([]string, error) { return nil, errors.New("503 from the models endpoint") })

	var err error
	out := captureStdout(t, func() { err = runAuthCopilot(authCtx(t, false)) })
	if err != nil {
		t.Fatalf("runAuthCopilot() = %v, want nil: the token was saved before this point", err)
	}

	tok, lerr := copilot.LoadToken(home)
	if lerr != nil || tok == nil || tok.AccessToken != "gho-new" {
		t.Fatalf("the token from a successful device flow was not persisted: %v, %v", tok, lerr)
	}
	if !strings.Contains(out, "joshbot configure") {
		t.Errorf("the operator is not told how to pick a model later:\n%s", out)
	}
}

// A device flow that genuinely failed must be an error, and nothing may be
// written. Saving a nil or empty token here would make the *next* run take the
// already-authenticated short circuit above and never authenticate at all.
func TestAuthCopilotReportsAFailedDeviceFlow(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)

	withCopilotSeams(t,
		func(context.Context) (*copilot.TokenInfo, error) {
			return nil, errors.New("access_denied")
		},
		func(string) ([]string, error) { return nil, errors.New("unreachable") })

	var err error
	captureStdout(t, func() { err = runAuthCopilot(authCtx(t, false)) })
	if err == nil {
		t.Fatal("runAuthCopilot() = nil for a denied device flow")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("the error does not name the cause: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".joshbot", "auth.json")); statErr == nil {
		t.Error("an auth file was written despite the flow failing")
	}
}

// The end of a successful run picks a model and records it against the
// github-copilot provider, enabled. Skipping the enable leaves the provider
// configured but inert, since a provider without "enabled": true is silently
// ignored at startup.
func TestAuthCopilotSavesTheSelectedModelEnabled(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)

	withCopilotSeams(t,
		func(context.Context) (*copilot.TokenInfo, error) {
			return &copilot.TokenInfo{AccessToken: "gho-new"}, nil
		},
		func(string) ([]string, error) { return []string{"gpt-4o", "claude-sonnet-4"}, nil })

	// "2" selects the second model from the list the picker was given.
	withStdinInput(t, "2\n")

	var err error
	captureStdout(t, func() { err = runAuthCopilot(authCtx(t, false)) })
	if err != nil {
		t.Fatalf("runAuthCopilot() = %v, want nil", err)
	}

	cfg, lerr := config.LoadStrict()
	if lerr != nil {
		t.Fatalf("LoadStrict: %v", lerr)
	}
	pc, ok := cfg.Providers["github-copilot"]
	if !ok {
		t.Fatalf("github-copilot was not written to the config: %v", cfg.Providers)
	}
	if pc.Model != "claude-sonnet-4" {
		t.Errorf("Model = %q, want the model the operator picked", pc.Model)
	}
	if !pc.Enabled {
		t.Error("the provider was saved disabled, so it is ignored at startup")
	}
}
