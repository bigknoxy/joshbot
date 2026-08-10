package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// replyAgent returns a fixed reply with a nil error, the way the ReAct loop
// reports an in-band failure to a chat channel.
type replyAgent struct{ reply string }

func (a *replyAgent) Process(context.Context, bus.InboundMessage) (string, error) {
	return a.reply, nil
}

// A non-interactive run must never exit 0 over a failed turn: the agent
// reports provider failures as reply text, so `agent -m` has to translate that
// back into a non-zero exit for the script calling it.
func TestRunAgentSingleMessage_ExitCodeContract(t *testing.T) {
	tests := []struct {
		name     string
		reply    string
		wantCode int
		wantOut  string
	}{
		{"answer", "the answer", exitOK, "the answer\n"},
		{"provider failure", "Error processing request: dial tcp: connection refused", exitGeneral, ""},
		{"failure with leading blank line", "\nError processing request: 401 unauthorized\n", exitGeneral, ""},
		{"mentions the phrase mid-reply", "Here is how to handle Error processing request: ...", exitOK,
			"Here is how to handle Error processing request: ...\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runAgentSingleMessage(context.Background(), &replyAgent{reply: tt.reply}, "hi", "", &out, nil)
			if got := codeForError(err); got != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (err=%v)", got, tt.wantCode, err)
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantOut)
			}
			if tt.wantCode != exitOK && !strings.Contains(err.Error(), "failed to process message") {
				t.Errorf("error should name the failed turn, got %v", err)
			}
		})
	}
}

// The JSON surface has the same contract: is_error must never be false over a
// failed turn, and the process must exit non-zero.
func TestRunAgentJSON_ExitCodeContract(t *testing.T) {
	tests := []struct {
		name        string
		reply       string
		wantCode    int
		wantIsError bool
	}{
		{"answer", "the answer", exitOK, false},
		{"provider failure", "Error processing request: dial tcp: connection refused", exitGeneral, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// runAgentJSON sets the package global jsonErrorEmitted on the
			// failure path. Leaving it set leaks into every later test in this
			// package (and into the "answer" case below), so each subtest owns
			// a known value and restores it.
			jsonErrorEmitted = false
			t.Cleanup(func() { jsonErrorEmitted = false })

			var stdout, stderr bytes.Buffer
			err := runAgentJSON(context.Background(), &replyAgent{reply: tt.reply}, "hi", "json", "", &stdout, &stderr, nil)
			if got := codeForError(err); got != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (err=%v)", got, tt.wantCode, err)
			}
			var res jsonResult
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
				t.Fatalf("stdout not a result document: %v\n%s", err, stdout.String())
			}
			if res.IsError != tt.wantIsError {
				t.Errorf("is_error = %v, want %v", res.IsError, tt.wantIsError)
			}
			if res.Type != "result" || res.SessionID == "" {
				t.Errorf("schema changed: %+v", res)
			}
			if tt.wantIsError && !strings.Contains(res.Result, "connection refused") {
				t.Errorf("result should carry the failure text, got %q", res.Result)
			}
			if tt.wantIsError && !strings.Contains(stderr.String(), `"type":"error"`) {
				t.Errorf("stderr should carry the JSON error doc, got %q", stderr.String())
			}
			// main() uses this to decide whether to also print a plain-text
			// error; it must track whether the JSON doc was actually written.
			if jsonErrorEmitted != tt.wantIsError {
				t.Errorf("jsonErrorEmitted = %v, want %v", jsonErrorEmitted, tt.wantIsError)
			}
		})
	}
}

// An unknown --provider produced "✓ credentials validated" and "Setup
// complete!" over a config nothing can dial.
func TestRunOnboard_ProviderFlagValidated(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantErr  string
	}{
		{"unknown provider", "notaprovider", "unknown provider"},
		{"typo of a real one", "openrouterr", "unknown provider"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := withTempHome(t)
			c := onboardContext(t, map[string]string{
				"provider": tt.provider,
				"api-key":  "xx",
			}, map[string]bool{"force": true})

			err := runOnboard(c)
			if err == nil {
				t.Fatal("expected an error for an unconfigurable provider")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "openrouter") {
				t.Errorf("error should list the supported providers, got %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(home, "config.json")); statErr == nil {
				t.Error("an unusable provider name must not write a config")
			}
		})
	}
}

// The model must follow the selected provider: config.DefaultModel is an
// OpenRouter id, so --force --provider ollama used to write a model ollama
// cannot serve.
func TestRunOnboard_ForceModelFollowsProvider(t *testing.T) {
	withTempHome(t)

	c := onboardContext(t, map[string]string{"provider": "ollama"}, map[string]bool{"force": true})
	if err := runOnboard(c); err != nil {
		t.Fatalf("runOnboard: %v", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := providers.GetDefaultModel("ollama")
	if want == "" {
		t.Fatal("ollama has no registry default model; test needs updating")
	}
	if loaded.Agents.Defaults.Model != want {
		t.Errorf("model = %q, want %q", loaded.Agents.Defaults.Model, want)
	}
}

// `joshbot onboard </dev/null` takes the default at every prompt and used to
// report success over a config with no provider at all.
func TestRunOnboard_InteractiveEOFFails(t *testing.T) {
	home := withTempHome(t)
	withStdinInput(t, "")
	for _, k := range []string{
		"JOSHBOT_PROVIDERS__NVIDIA__API_KEY", "JOSHBOT_NVIDIA_API_KEY",
	} {
		t.Setenv(k, "")
	}

	err := runOnboard(onboardContext(t, nil, nil))
	if err == nil {
		t.Fatal("expected an error when every prompt reads EOF")
	}
	if !strings.Contains(err.Error(), "did not configure any provider") {
		t.Fatalf("error missing actionable guidance: %v", err)
	}
	if strings.Contains(err.Error(), "--force") {
		t.Errorf("interactive failure must not claim --force was used: %v", err)
	}
	// Same contract as --force: the scaffold is still written, only the exit
	// status reports the failure.
	if _, statErr := os.Stat(filepath.Join(home, "config.json")); statErr != nil {
		t.Errorf("config.json should still be written: %v", statErr)
	}
}

// An enabled provider whose name is not a provider at all must not be told to
// add "enabled": true — it already has it, and the name is the real fault.
func TestNoProvidersRegisteredError_EnabledUnknownProvider(t *testing.T) {
	err := noProvidersRegisteredError(map[string]config.ProviderConfig{
		"notaprovider": {Enabled: true, APIKey: "sk-x"},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), `"enabled": true`) {
		t.Errorf("error blames the enabled flag on an already-enabled provider: %v", err)
	}
	if !strings.Contains(err.Error(), "notaprovider") {
		t.Errorf("error should name the unknown provider, got %v", err)
	}
	if !strings.Contains(err.Error(), "openrouter") {
		t.Errorf("error should list the supported providers, got %v", err)
	}
}
