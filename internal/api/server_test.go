package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
)

// fakeAgent stands in for the ReAct loop. It records the message it was given,
// so a test can assert on what the handler decided to send rather than only on
// what came back.
type fakeAgent struct {
	reply string
	err   error
	got   bus.InboundMessage
	// before runs inside Process, which is where a real agent would emit
	// stream and usage events.
	before func(ctx context.Context)
}

func (f *fakeAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	f.got = msg
	if f.before != nil {
		f.before(ctx)
	}
	return f.reply, f.err
}

func testServer(t *testing.T, a Processor, keys ...string) *Server {
	t.Helper()
	if len(keys) == 0 {
		keys = []string{"secret"}
	}
	s, err := New(a, Options{Listen: "127.0.0.1:0", APIKeys: keys})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func do(t *testing.T, s *Server, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

// TestNewRefusesWithoutAUsableKey pins the fail-closed construction. A config
// carrying only blank entries must not produce a server that accepts anything:
// this endpoint reaches the shell tool, so an unauthenticated one is remote code
// execution.
func TestNewRefusesWithoutAUsableKey(t *testing.T) {
	for name, keys := range map[string][]string{
		"no keys":     nil,
		"empty entry": {""},
		"whitespace":  {"   ", "\t"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(&fakeAgent{}, Options{APIKeys: keys}); err == nil {
				t.Fatal("New accepted a config with no usable API key")
			}
		})
	}
}

// TestAuthRejectsEverythingButAConfiguredKey covers the credential paths that
// would each be a silent auth bypass: no header, the wrong key, a blank
// credential (which must not match the blank entry a config might contain), and
// a prefix of a real key.
func TestAuthRejectsEverythingButAConfiguredKey(t *testing.T) {
	s := testServer(t, &fakeAgent{reply: "hi"}, "alpha", "", "beta")

	for name, key := range map[string]string{
		"missing":     "",
		"wrong":       "gamma",
		"prefix":      "alph",
		"extended":    "alphax",
		"blank entry": " ",
	} {
		t.Run("rejected/"+name, func(t *testing.T) {
			w := do(t, s, http.MethodGet, "/v1/models", key, "")
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("key %q got %d, want 401", key, w.Code)
			}
			if strings.Contains(w.Body.String(), "alpha") || strings.Contains(w.Body.String(), "beta") {
				t.Fatalf("401 body leaked a configured key: %s", w.Body.String())
			}
		})
	}

	// Both configured keys work: a server does not quietly honour only the
	// first entry.
	for _, key := range []string{"alpha", "beta"} {
		if w := do(t, s, http.MethodGet, "/v1/models", key, ""); w.Code != http.StatusOK {
			t.Fatalf("key %q got %d, want 200", key, w.Code)
		}
	}
}

// TestAuthSchemeIsCaseInsensitive guards the header parse. RFC 7235 makes the
// scheme case-insensitive and clients differ; a case-sensitive match would
// reject real callers with a message blaming their key.
func TestAuthSchemeIsCaseInsensitive(t *testing.T) {
	s := testServer(t, &fakeAgent{})
	for _, header := range []string{"Bearer secret", "bearer secret", "BEARER secret"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("header %q got %d, want 200", header, w.Code)
		}
	}
	// A different scheme carrying the right value is still not a bearer token.
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Basic secret")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Basic scheme got %d, want 401", w.Code)
	}
}

// TestChatCompletionsRequiresAuth is separate from the models case on purpose:
// the route table is where a handler gets accidentally registered unwrapped,
// and the chat route is the one that executes tools.
func TestChatCompletionsRequiresAuth(t *testing.T) {
	a := &fakeAgent{reply: "hi"}
	s := testServer(t, a)
	w := do(t, s, http.MethodPost, "/v1/chat/completions", "",
		`{"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
	if a.got.Content != "" {
		t.Fatalf("unauthenticated request reached the agent with %q", a.got.Content)
	}
}

func TestModelsListsTheSingleAgentModel(t *testing.T) {
	s := testServer(t, &fakeAgent{})
	w := do(t, s, http.MethodGet, "/v1/models", "secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var got modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data) != 1 || got.Data[0].ID != ModelID {
		t.Fatalf("got %+v, want a single %q entry", got.Data, ModelID)
	}
}

// TestErrorBodiesAreRedacted pins the boundary crossed by every error this
// server returns. A 502 carries the provider's error text verbatim, so an
// upstream credential or the operator's home path would otherwise be handed to
// an API caller, who is authenticated but is not the operator.
//
// It goes through the real handler rather than calling safeErrorMessage
// directly: the funnel is only a defence if every writer uses it, and a new
// handler that marshals its own envelope is exactly the regression to catch.
func TestErrorBodiesAreRedacted(t *testing.T) {
	// The home path is the operator's real one: redact.HomePath rewrites that
	// specific prefix, not any path that merely looks like a home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	a := &fakeAgent{err: errors.New(
		"provider call failed: Authorization: Bearer sk-live-abcdef0123456789 " +
			"reading " + filepath.Join(home, ".joshbot", "config.json"))}
	s := testServer(t, a)
	w := do(t, s, http.MethodPost, "/v1/chat/completions", "secret",
		`{"messages":[{"role":"user","content":"hi"}]}`)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("got %d, want 502", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "sk-live-abcdef0123456789") {
		t.Fatalf("502 body leaked an upstream credential: %s", body)
	}
	if strings.Contains(body, home) {
		t.Fatalf("502 body leaked an absolute home path: %s", body)
	}
	// The message must still be diagnosable — redacting it to nothing would
	// trade one failure mode for another.
	if !strings.Contains(body, "provider call failed") {
		t.Fatalf("502 body lost the diagnosis: %s", body)
	}
}

// TestHealthNeedsNoCredential pins the one deliberately unauthenticated route,
// and that it says nothing about the configuration.
func TestHealthNeedsNoCredential(t *testing.T) {
	s := testServer(t, &fakeAgent{}, "topsecret")
	w := do(t, s, http.MethodGet, "/healthz", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "topsecret") {
		t.Fatalf("health body leaked a key: %s", w.Body.String())
	}
}

// TestUnauthorizedBodyKeepsItsInstruction pins the other half of the boundary
// TestErrorBodiesAreRedacted guards. joshbot's own constants must reach the
// caller verbatim: running them through the redactor made the 401 read
// "Authorization: Bearer [REDACTED]", because the header rule ate the <key>
// placeholder that tells the caller what to send (#238). The 401 is the first
// thing anyone integrating against this server sees, and that body told them
// their key had been censored rather than how to present it.
func TestUnauthorizedBodyKeepsItsInstruction(t *testing.T) {
	s := testServer(t, &fakeAgent{})
	for _, key := range []string{"", "wrong"} {
		w := do(t, s, http.MethodGet, "/v1/models", key, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("key %q: got %d, want 401", key, w.Code)
		}
		// Decoded, not raw: encoding/json escapes the angle brackets, so a
		// substring check against the wire bytes would fail on correct output.
		var env errorEnvelope
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("key %q: decode 401 body: %v", key, err)
		}
		if strings.Contains(env.Error.Message, "REDACTED") {
			t.Fatalf("key %q: 401 body was redacted: %s", key, env.Error.Message)
		}
		if !strings.Contains(env.Error.Message, "Authorization: Bearer <key>") {
			t.Fatalf("key %q: 401 body lost the instruction: %s", key, env.Error.Message)
		}
	}
}

// TestMidStreamErrorFrameIsRedacted covers the one error path that does not go
// through writeError at all: once bytes are on the wire the status is already
// 200, so the failure is reported inside the stream. That frame is built by
// hand, and it carries the same provider text a 502 would — so it needs the
// same redaction, or a streaming client sees the upstream credential a
// non-streaming one is protected from.
func TestMidStreamErrorFrameIsRedacted(t *testing.T) {
	a := &fakeAgent{
		err: errors.New("provider call failed: Authorization: Bearer sk-live-abcdef0123456789"),
		before: func(ctx context.Context) {
			agent.StreamSinkFromContext(ctx)(agent.StreamEvent{Delta: "partial"})
		},
	}
	s := testServer(t, a)
	w := do(t, s, http.MethodPost, "/v1/chat/completions", "secret",
		`{"messages":[{"role":"user","content":"hi"}],"stream":true}`)

	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "partial") {
		t.Fatalf("expected a started stream, got %d: %s", w.Code, body)
	}
	if strings.Contains(body, "sk-live-abcdef0123456789") {
		t.Fatalf("mid-stream error frame leaked an upstream credential: %s", body)
	}
	if !strings.Contains(body, "provider call failed") {
		t.Fatalf("mid-stream error frame lost the diagnosis: %s", body)
	}
	if strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("a failed stream reported a clean finish: %s", body)
	}
}
