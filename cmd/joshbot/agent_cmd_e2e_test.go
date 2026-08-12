package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

// End-to-end runs of `joshbot agent -m ...` against a fake OpenAI-compatible
// endpoint. This is the command every script and cron wrapper calls, and its
// contract is an exit code plus what lands on stdout — neither of which any
// unit test of the pieces can pin.
//
// The rule under test throughout: agent.Process reports LLM failures *in band*,
// as reply text with a nil error, because a chat channel has to show the user
// something. Every non-interactive entry point has to translate that back into
// a non-zero exit (and, in JSON mode, is_error) or a completely dead provider
// exits 0 with an apology on stdout.

// chatServer serves /chat/completions with a fixed assistant reply. status
// selects the HTTP status: a non-200 makes the provider fail, which is how the
// error paths are driven without touching the network.
func chatServer(t *testing.T, reply string, status int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream is down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"model":   "test-model",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": reply}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// agentEnv writes a config pointed at srv and returns its path.
func agentEnv(t *testing.T, apiBase string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".joshbot"), 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"workspace": filepath.Join(home, "workspace"),
				"model":     "openrouter/test-model",
				// Streaming is on by default; the fake endpoint speaks plain
				// chat completions, so keep this turn non-streaming.
				"streaming": false,
			},
		},
		"providers": map[string]any{
			"openrouter": map[string]any{
				"enabled":  true,
				"api_key":  "sk-test",
				"api_base": apiBase,
			},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	path := filepath.Join(home, ".joshbot", "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// runCLI runs the real CLI surface and returns stdout plus the exit code.
func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()

	app := newApp()
	app.Writer = io.Discard
	app.ErrWriter = io.Discard
	app.ExitErrHandler = func(*cli.Context, error) {} // never os.Exit from a test

	var code int
	out := captureStdout(t, func() {
		if err := app.Run(append([]string{"joshbot"}, args...)); err != nil {
			// codeForError is what main uses to pick the process exit code, so
			// the tests assert on the same mapping the shell would see.
			code = codeForError(err)
			if code == 0 {
				code = 1
			}
		}
	})
	return out, code
}

func TestAgentSingleMessagePrintsTheReplyAndExitsZero(t *testing.T) {
	srv := chatServer(t, "the answer is 42", http.StatusOK)
	cfg := agentEnv(t, srv.URL+"/v1")

	out, code := runCLI(t, "--config", cfg, "agent", "-m", "what is the answer")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0. Output:\n%s", code, out)
	}
	if !strings.Contains(out, "the answer is 42") {
		t.Errorf("reply not printed to stdout:\n%s", out)
	}
}

// The core regression: a provider that fails must exit non-zero. Process
// returns the failure as reply *text* with a nil error, so without
// agentReplyError this exits 0 and a cron wrapper believes it succeeded.
func TestAgentSingleMessageProviderFailureExitsNonZero(t *testing.T) {
	srv := chatServer(t, "", http.StatusInternalServerError)
	cfg := agentEnv(t, srv.URL+"/v1")

	out, code := runCLI(t, "--config", cfg, "agent", "-m", "hello")
	if code == 0 {
		t.Fatalf("a failing provider exited 0; the in-band error was treated as an answer. Output:\n%s", out)
	}
}

// JSON mode: stdout carries exactly one result document, and a provider failure
// sets is_error on it rather than reading as a successful answer.
func TestAgentJSONOutputMarksProviderFailure(t *testing.T) {
	srv := chatServer(t, "", http.StatusInternalServerError)
	cfg := agentEnv(t, srv.URL+"/v1")

	out, code := runCLI(t, "--config", cfg, "agent", "--output-format", "json", "-m", "hello")
	if code == 0 {
		t.Error("JSON mode exited 0 over a dead provider")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout is not a single JSON document (%v):\n%s", err, out)
	}
	if isErr, _ := doc["is_error"].(bool); !isErr {
		t.Errorf("is_error is not set on a failed turn: %v", doc)
	}
}

func TestAgentJSONOutputIsMachineReadableOnSuccess(t *testing.T) {
	srv := chatServer(t, "hi there", http.StatusOK)
	cfg := agentEnv(t, srv.URL+"/v1")

	out, code := runCLI(t, "--config", cfg, "agent", "--output-format", "json", "-m", "hello")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0. Output:\n%s", code, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("stdout is not a single JSON document (%v):\n%s", err, out)
	}
	if isErr, _ := doc["is_error"].(bool); isErr {
		t.Errorf("is_error set on a successful turn: %v", doc)
	}
	// The API key lives in the config this run loaded; it must not be echoed
	// into a document users paste into issues.
	if strings.Contains(out, "sk-test") {
		t.Errorf("JSON output leaked the API key:\n%s", out)
	}
}

// A typo in --output-format must fail before any model setup, with the
// validation exit code rather than a generic 1.
func TestAgentRejectsUnknownOutputFormat(t *testing.T) {
	cfg := agentEnv(t, "http://127.0.0.1:1/v1")

	_, code := runCLI(t, "--config", cfg, "agent", "--output-format", "yaml", "-m", "hi")
	if code != exitValidation {
		t.Errorf("exit code = %d, want exitValidation (%d)", code, exitValidation)
	}
}

// JSON modes are non-interactive: without -m there is nothing to answer, and
// blocking on a terminal that is not there would hang a script.
func TestAgentJSONWithoutMessageIsAValidationError(t *testing.T) {
	cfg := agentEnv(t, "http://127.0.0.1:1/v1")

	_, code := runCLI(t, "--config", cfg, "agent", "--output-format", "json")
	if code != exitValidation {
		t.Errorf("exit code = %d, want exitValidation (%d)", code, exitValidation)
	}
}

// A config with no providers at all points at onboarding and uses the auth exit
// code, so a wrapper can tell "not configured" from "provider is down".
func TestAgentWithNoProvidersUsesTheAuthExitCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".joshbot"), 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(home, ".joshbot", "config.json")
	if err := os.WriteFile(path, []byte(`{"agents":{"defaults":{"workspace":"`+home+`/workspace"}}}`), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, code := runCLI(t, "--config", path, "agent", "-m", "hi")
	if code != exitAuth {
		t.Errorf("exit code = %d, want exitAuth (%d)", code, exitAuth)
	}
}

// --image is refused before the turn starts when the path is not a real image:
// containment exists to bound the model, not the operator, so what is enforced
// here is that the bytes really are an image.
func TestAgentRejectsANonImageAttachment(t *testing.T) {
	srv := chatServer(t, "ok", http.StatusOK)
	cfg := agentEnv(t, srv.URL+"/v1")

	notAnImage := filepath.Join(t.TempDir(), "prose.png")
	if err := os.WriteFile(notAnImage, []byte("this is plain text, not a PNG"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, code := runCLI(t, "--config", cfg, "agent", "-m", "look", "--image", notAnImage)
	if code != exitValidation {
		t.Errorf("exit code = %d, want exitValidation (%d); a .png full of prose was accepted", code, exitValidation)
	}
}
