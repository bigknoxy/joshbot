package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/copilot"
)

// The github-copilot branch of the wizard is the only one that authenticates
// instead of asking for a key, and it is the only one that can abandon the
// wizard half way. Both of its failure modes are silent: prompting for an API
// key stores a credential the provider never sends, and writing an enabled
// entry after a failed OAuth produces an install that reports "configured" and
// cannot authenticate a single turn.

// A token already on disk means the operator is done — the wizard must not
// push them back through a browser approval to change their model.
func TestConfigureProviderCopilotUsesTheExistingTokenAndAsksForNoAPIKey(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	writeCopilotToken(t, home, "gho-existing", 0)

	flowRan := false
	withCopilotSeams(t,
		func(context.Context) (*copilot.TokenInfo, error) {
			flowRan = true
			return nil, errors.New("the device flow must not run")
		},
		// No models offered, so the wizard falls through to its typed prompt.
		func(string) ([]string, error) { return nil, nil })

	withStdinInput(t, "copilot-model\n")

	cfg := config.Defaults()
	cfg.Providers = nil
	cfg.ProviderDefaults.Default = ""

	var got *config.Config
	out := captureStdout(t, func() { got = configureProvider(cfg, "github-copilot") })

	if flowRan {
		t.Error("the device flow ran even though a token was already on disk")
	}
	if strings.Contains(out, "API key") {
		t.Errorf("the wizard asked for an API key on an OAuth provider:\n%s", out)
	}

	p, ok := got.Providers["github-copilot"]
	if !ok {
		t.Fatalf("github-copilot was not written: %+v", got.Providers)
	}
	if p.APIKey != "" {
		t.Errorf("api_key = %q; github-copilot authenticates by OAuth", p.APIKey)
	}
	if !p.Enabled {
		t.Error(`written without "enabled": true, so it is inert at runtime`)
	}
	if p.Model != "copilot-model" {
		t.Errorf("model = %q, want the model that was typed", p.Model)
	}
	if got.ProviderDefaults.Default != "github-copilot" {
		t.Errorf("default provider = %q, want github-copilot", got.ProviderDefaults.Default)
	}
}

// An OAuth the operator declined must abandon the branch. Falling through
// writes an enabled provider with no token behind it: every later turn fails
// authentication with nothing pointing back at this moment.
func TestConfigureProviderCopilotWritesNothingWhenTheDeviceFlowFails(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)

	withCopilotSeams(t,
		func(context.Context) (*copilot.TokenInfo, error) {
			return nil, errors.New("access_denied")
		},
		func(string) ([]string, error) { return nil, errors.New("unreachable") })

	cfg := config.Defaults()
	cfg.Providers = nil
	cfg.ProviderDefaults.Default = ""

	var got *config.Config
	out := captureStdout(t, func() { got = configureProvider(cfg, "github-copilot") })

	if _, ok := got.Providers["github-copilot"]; ok {
		t.Errorf("a failed OAuth still wrote a provider entry: %+v", got.Providers["github-copilot"])
	}
	if got.ProviderDefaults.Default != "" {
		t.Errorf("default provider was set to %q despite the OAuth failing", got.ProviderDefaults.Default)
	}
	if !strings.Contains(out, "access_denied") {
		t.Errorf("the operator is not told why it stopped:\n%s", out)
	}
}

// Re-running the wizard and pressing Enter must keep the configured model.
// Clearing it here leaves the provider enabled with no model, which the agent
// reports as a provider fault rather than as the wizard having wiped it.
func TestConfigureProviderCopilotEmptyAnswerKeepsTheExistingModel(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	writeCopilotToken(t, home, "gho-existing", 0)

	withCopilotSeams(t,
		func(context.Context) (*copilot.TokenInfo, error) {
			return nil, errors.New("the device flow must not run")
		},
		func(string) ([]string, error) { return nil, nil })

	withStdinInput(t, "\n")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"github-copilot": {Enabled: true, Model: "claude-sonnet-4"},
	}
	cfg.ProviderDefaults.Default = "github-copilot"

	var got *config.Config
	captureStdout(t, func() { got = configureProvider(cfg, "github-copilot") })

	if p := got.Providers["github-copilot"]; p.Model != "claude-sonnet-4" {
		t.Errorf("model = %q; pressing Enter must not clear the configured model", p.Model)
	}
}
