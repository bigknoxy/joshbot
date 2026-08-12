package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The `agent` flags that change *where the turn goes* or *whether it happens at
// all* are checked here. Each of these fails silently when it regresses: a
// dropped --model answers from the configured model instead of the requested
// one, an unknown --profile that is not caught up front surfaces as a provider
// error mid-conversation, and an --image with no message is either ignored or
// sent as an empty turn.

// recordingChatServer answers chat completions and records every request body.
type recordingChatServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []map[string]any
}

func (s *recordingChatServer) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.reqs...)
}

func newRecordingChatServer(t *testing.T, reply string) *recordingChatServer {
	t.Helper()
	rec := &recordingChatServer{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, parsed)
		rec.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{
			"id":     "chatcmpl-test",
			"object": "chat.completion",
			"model":  "test-model",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": reply},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(rec.Close)
	return rec
}

// --model must reach the provider. The flag is applied to cfg before
// setupComponents builds anything, and nothing downstream re-reads it, so a
// regression here answers from the configured model while reporting the
// requested one nowhere.
func TestAgentModelFlagIsWhatGetsSentToTheProvider(t *testing.T) {
	srv := newRecordingChatServer(t, "ok")
	cfg := agentEnv(t, srv.URL+"/v1")

	_, code := runCLI(t, "--config", cfg, "agent", "--model", "openrouter/flag-model", "-m", "hi")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	reqs := srv.requests()
	if len(reqs) == 0 {
		t.Fatal("the provider was never called")
	}
	got, _ := reqs[0]["model"].(string)
	// The openrouter prefix is stripped before the request is sent; what must
	// not happen is the configured model being used instead.
	if got != "flag-model" {
		t.Errorf("provider was asked for model %q, want the --model value (flag-model)", got)
	}
	if got == "test-model" {
		t.Error("--model was ignored and the configured model was used")
	}
}

// An unknown --profile is a startup error, on purpose: it must be refused
// before any component is built, so the provider is never dialled. Deferring it
// turns a typo into a confusing mid-conversation provider failure.
func TestAgentUnknownProfileFailsBeforeDiallingTheProvider(t *testing.T) {
	srv := newRecordingChatServer(t, "ok")
	cfg := agentEnv(t, srv.URL+"/v1")

	_, code := runCLI(t, "--config", cfg, "agent", "--profile", "no-such-profile", "-m", "hi")
	if code == 0 {
		t.Fatal("an unknown --profile exited 0")
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("the provider was called %d time(s) despite an unknown profile; the check ran too late", n)
	}
}

// --image with no --message is a validation error, not an empty turn. It is
// also checked before the provider is called, so a mistake costs nothing.
func TestAgentImageWithoutMessageIsAValidationErrorAndSendsNothing(t *testing.T) {
	srv := newRecordingChatServer(t, "ok")
	cfg := agentEnv(t, srv.URL+"/v1")

	img := filepath.Join(t.TempDir(), "a.png")
	writePNG(t, img)

	_, code := runCLI(t, "--config", cfg, "agent", "--image", img)
	if code != exitValidation {
		t.Errorf("exit code = %d, want exitValidation (%d)", code, exitValidation)
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("the provider was called %d time(s) for a turn with no message", n)
	}
}

// A bad --image path in JSON mode must be a well-formed error document on
// stderr, not a plain line: stdout carries data only, and a wrapper parsing
// stderr as JSON is the whole reason the mode exists.
func TestAgentJSONBadImagePathEmitsAnErrorDocument(t *testing.T) {
	srv := newRecordingChatServer(t, "ok")
	cfg := agentEnv(t, srv.URL+"/v1")

	missing := filepath.Join(t.TempDir(), "nope.png")

	stderr := captureStderr(t, func() {
		_, code := runCLI(t, "--config", cfg, "agent", "--output-format", "json",
			"-m", "what is this", "--image", missing)
		if code != exitValidation {
			t.Errorf("exit code = %d, want exitValidation (%d)", code, exitValidation)
		}
	})

	line := strings.TrimSpace(stderr)
	if line == "" {
		t.Fatal("json mode reported nothing on stderr")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(lastLine(line)), &doc); err != nil {
		t.Fatalf("stderr is not a JSON document (%v):\n%s", err, stderr)
	}
	if doc["type"] != "error" {
		t.Errorf(`stderr document type = %v, want "error": %s`, doc["type"], stderr)
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("the provider was called %d time(s)", n)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// captureStderr mirrors captureStdout for the JSON error channel.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = pw

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(pr)
		done <- string(b)
	}()

	defer func() {
		os.Stderr = prev
		_ = pw.Close()
	}()
	fn()
	os.Stderr = prev
	_ = pw.Close()
	out := <-done
	_ = pr.Close()
	return out
}
