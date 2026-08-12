package main

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// selectModel and promptProviderAPIKey are the two onboarding prompts whose
// answer is written straight into the config. Both have a priority order that
// is invisible in the output — the operator sees one default in brackets and
// presses Enter — so a regression in either produces a config that looks
// plausible and talks to the wrong model, or none at all.

// selectModel resolves its default as: --model flag, then the model already in
// the config, then the provider's built-in default. Getting that order wrong is
// silent: re-running onboarding on a working install would quietly move it onto
// the provider default and abandon whatever model the operator had chosen.
func TestSelectModelDefaultPriorityFlagThenConfigThenProvider(t *testing.T) {
	withTempHome(t)

	providerDefault := providers.GetDefaultModel("nvidia")
	if providerDefault == "" {
		t.Fatal("nvidia has no built-in default model; the fixture assumes one")
	}

	withExisting := config.Defaults()
	withExisting.Agents.Defaults.Model = "config-model"

	cases := []struct {
		name     string
		cfg      *config.Config
		flag     string
		want     string
		wantNot  string
		whyWrong string
	}{
		{
			name:     "flag beats the stored model",
			cfg:      withExisting,
			flag:     "flag-model",
			want:     "flag-model",
			wantNot:  "config-model",
			whyWrong: "--model was ignored in favour of the stored model",
		},
		{
			name:     "stored model beats the provider default",
			cfg:      withExisting,
			want:     "config-model",
			wantNot:  providerDefault,
			whyWrong: "onboarding silently reset the configured model to the provider default",
		},
		{
			name:     "provider default when there is nothing else",
			cfg:      nil,
			want:     providerDefault,
			whyWrong: "a fresh install did not fall back to the provider's default model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A bare Enter accepts whatever default was computed, which is
			// exactly the value under test.
			withStdinInput(t, "\n")

			var got string
			out := captureStdout(t, func() { got = selectModel(tc.cfg, "nvidia", tc.flag) })

			if got != tc.want {
				t.Errorf("selectModel = %q, want %q — %s", got, tc.want, tc.whyWrong)
			}
			if tc.wantNot != "" && got == tc.wantNot {
				t.Errorf("selectModel returned the lower-priority %q — %s", tc.wantNot, tc.whyWrong)
			}
			// The bracketed default is what the operator is agreeing to, so it
			// has to match what pressing Enter actually stores.
			if !strings.Contains(out, "["+tc.want+"]") {
				t.Errorf("prompt did not offer %q as the default:\n%s", tc.want, out)
			}
		})
	}
}

// A typed model always wins, including over the --model flag: the flag only
// seeds the default.
func TestSelectModelTypedAnswerOverridesEveryDefault(t *testing.T) {
	withTempHome(t)
	withStdinInput(t, "typed/model\n")

	cfg := config.Defaults()
	cfg.Agents.Defaults.Model = "config-model"

	var got string
	captureStdout(t, func() { got = selectModel(cfg, "nvidia", "flag-model") })

	if got != "typed/model" {
		t.Errorf("selectModel = %q, want the typed answer", got)
	}
}

// Without an OAuth token on disk, the Copilot branch must not try to fetch the
// catalogue — it has no credential to fetch it with — and must fall through to
// the ordinary typed prompt rather than returning empty.
func TestSelectModelCopilotWithoutATokenFallsBackToThePrompt(t *testing.T) {
	withTempHome(t)
	withStdinInput(t, "\n")

	var got string
	out := captureStdout(t, func() { got = selectModel(nil, "github-copilot", "") })

	if got == "" {
		t.Error("selectModel returned an empty model; onboarding would save a config with no model")
	}
	if !strings.Contains(out, "Not authenticated") {
		t.Errorf("the unauthenticated fallback was not reported to the operator:\n%s", out)
	}
}

// Ollama is local and has no credential. Prompting for one would consume a line
// of the operator's input and shift every later answer by one prompt.
func TestPromptProviderAPIKeyOllamaNeedsNoKeyAndAsksForNothing(t *testing.T) {
	var key string
	var err error
	out := captureStdout(t, func() { key, err = promptProviderAPIKey("ollama", nil) })

	if err != nil {
		t.Fatalf("promptProviderAPIKey(ollama): %v", err)
	}
	if key != "" {
		t.Errorf("ollama produced an API key %q; it has no credential", key)
	}
	if strings.Contains(out, "Enter your") || strings.Contains(out, "Enter new API key") {
		t.Errorf("ollama prompted for a key, which shifts every later answer:\n%s", out)
	}
}

// Pressing Enter at the key prompt means "keep the current key". Returning ""
// here is the field bug documented in onboard_test.go: runOnboard reads an
// empty key as "no provider configured" and drops a working provider.
func TestPromptProviderAPIKeyEnterKeepsTheExistingKey(t *testing.T) {
	withStdinInput(t, "\n")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"nvidia": {Enabled: true, APIKey: "nvapi-keepme"},
	}

	var key string
	var err error
	out := captureStdout(t, func() { key, err = promptProviderAPIKey("nvidia", cfg) })

	if err != nil {
		t.Fatalf("promptProviderAPIKey: %v", err)
	}
	if key != "nvapi-keepme" {
		t.Errorf("key = %q, want the existing key kept", key)
	}
	// The existing key must be shown masked; printing it in full puts a live
	// credential on the terminal and into any captured onboarding transcript.
	if strings.Contains(out, "nvapi-keepme") {
		t.Errorf("the stored API key was printed unmasked:\n%s", out)
	}
}

// A typed key replaces the stored one, and is trimmed — a trailing space in a
// pasted key produces a 401 that names nothing.
func TestPromptProviderAPIKeyTypedKeyReplacesTheStoredOne(t *testing.T) {
	withStdinInput(t, "  nvapi-new  \n")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"nvidia": {Enabled: true, APIKey: "nvapi-old"},
	}

	var key string
	captureStdout(t, func() { key, _ = promptProviderAPIKey("nvidia", cfg) })

	if key != "nvapi-new" {
		t.Errorf("key = %q, want the typed key, trimmed", key)
	}
}
