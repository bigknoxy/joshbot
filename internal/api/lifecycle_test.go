package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// freePort returns a port nothing is listening on. Serve is given a concrete
// address rather than ":0" because Addr() reports what was configured, not what
// the kernel assigned — a test that asked for port 0 would then dial port 0.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// servable builds a Server bound to a free port.
func servable(t *testing.T) *Server {
	t.Helper()
	s, err := New(&fakeAgent{reply: "ok"}, Options{Listen: freePort(t), APIKeys: []string{"secret"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// serveOnAFreePort starts a Server on an OS-assigned port and returns its base
// URL plus a channel carrying Serve's return value.
//
// A real listener is used rather than httptest: Serve owns the net.Listen, the
// goroutine handshake and the shutdown, and none of that runs when a test drives
// s.routes() directly — which is why every one of those lines was at 0%.
func serveOnAFreePort(t *testing.T, s *Server) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx) }()

	// Poll rather than sleep: Serve binds asynchronously, and a fixed sleep is
	// either flaky or slow.
	base := "http://" + s.Addr()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server never accepted a connection on %s", s.Addr())
		}
		c, err := net.DialTimeout("tcp", s.Addr(), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return base, cancel, errCh
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestServeRunsAndShutsDownCleanly covers the whole lifecycle: bind, serve a
// real request, cancel, and return.
//
// The last part is the one that matters. Serve maps http.ErrServerClosed to nil,
// so a shutdown path that returned early — or never returned at all — would look
// identical to a clean run from the outside. joshbot's own history has three
// bugs of exactly this shape (the bus, Discord's stopCh, mcp.Client's doneCh),
// and each shipped because nothing started the object for real.
func TestServeRunsAndShutsDownCleanly(t *testing.T) {
	s := servable(t)
	base, cancel, errCh := serveOnAFreePort(t, s)

	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok"`) {
		t.Fatalf("healthz: %d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Serve returned %v on a clean shutdown, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}

	// The port must actually be released. A Shutdown that returns while the
	// listener is still open makes a restart fail with EADDRINUSE.
	ln, err := net.Listen("tcp", s.Addr())
	if err != nil {
		t.Fatalf("port still held after Serve returned: %v", err)
	}
	_ = ln.Close()
}

// TestServeRefusesASecondRun pins the one-shot latch. http.Server cannot be
// restarted after Shutdown: a second Serve binds the port, takes
// ErrServerClosed straight back, and — because that error is mapped to nil —
// reports a clean successful run while serving nothing. A supervisor loop or a
// reload would then no-op in silence, which is worse than a crash.
func TestServeRefusesASecondRun(t *testing.T) {
	s := servable(t)
	_, cancel, errCh := serveOnAFreePort(t, s)
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("first Serve: %v", err)
	}

	err := s.Serve(context.Background())
	if !errors.Is(err, ErrServerReused) {
		t.Fatalf("second Serve returned %v, want ErrServerReused", err)
	}
}

// TestServeReportsABindFailure covers the other exit: the address is taken, so
// no goroutine should be started and the error must name the address. Returning
// nil here would leave joshbot serve reporting success with nothing listening.
func TestServeReportsABindFailure(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = held.Close() }()

	s, err := New(&fakeAgent{}, Options{Listen: held.Addr().String(), APIKeys: []string{"secret"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = s.Serve(context.Background())
	if err == nil {
		t.Fatal("Serve returned nil on an address already in use")
	}
	if !strings.Contains(err.Error(), held.Addr().String()) {
		t.Fatalf("bind error %q does not name the address", err)
	}
}

// TestNewRequiresAListenAddress pins that the package chooses no default. The
// default lives in config.DefaultAPIListen and is resolved by runServe; a second
// copy here is a value nothing keeps in agreement with the first, and the
// symptom is a server not on the port the docs name.
func TestNewRequiresAListenAddress(t *testing.T) {
	for _, listen := range []string{"", "   "} {
		if _, err := New(&fakeAgent{}, Options{Listen: listen, APIKeys: []string{"k"}}); err == nil {
			t.Fatalf("New accepted listen %q instead of requiring the caller to resolve it", listen)
		}
	}
}

// TestStreamingUsageAccumulates is the streaming twin of the non-streaming
// accumulation test. The sink fires once per provider call and an agentic turn
// makes several, so overwriting instead of adding under-reports every turn that
// used a tool — silently, in the direction that flatters the bill.
func TestStreamingUsageAccumulates(t *testing.T) {
	a := &fakeAgent{
		reply: "done",
		before: func(ctx context.Context) {
			usage := agent.UsageFromContext(ctx)
			sink := agent.StreamSinkFromContext(ctx)
			usage(providers.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
			sink(agent.StreamEvent{Delta: "done"})
			usage(providers.Usage{PromptTokens: 30, CompletionTokens: 5, TotalTokens: 35})
		},
	}
	s := testServer(t, a)
	w := do(t, s, http.MethodPost, "/v1/chat/completions", "secret",
		`{"messages":[{"role":"user","content":"hi"}],"stream":true}`)

	cs := frames(t, w.Body.String())
	final := cs[len(cs)-1]
	if final.Usage == nil {
		t.Fatal("final frame carried no usage")
	}
	if final.Usage.PromptTokens != 40 || final.Usage.CompletionTokens != 7 || final.Usage.TotalTokens != 47 {
		t.Fatalf("usage %+v; want the sum of both provider calls, not the last one", final.Usage)
	}
}

// TestModelsRejectsNonGet mirrors the method check the chat route already has.
// Without it a POST to /v1/models answers 200, which makes a client's write to
// the wrong path look like it worked.
func TestModelsRejectsNonGet(t *testing.T) {
	s := testServer(t, &fakeAgent{})
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := do(t, s, m, "/v1/models", "secret", "")
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /v1/models returned %d, want 405", m, w.Code)
		}
	}
}

// TestJSONResponsesCarryTheirContentType covers both writers. A JSON body served
// without the header is parsed by strict clients as text, so an error envelope
// arrives as an unreadable string exactly when the caller needs to read it.
func TestJSONResponsesCarryTheirContentType(t *testing.T) {
	s := testServer(t, &fakeAgent{reply: "ok"})
	for name, w := range map[string]*http.Response{
		"ok":    do(t, s, http.MethodGet, "/v1/models", "secret", "").Result(),
		"error": do(t, s, http.MethodGet, "/v1/models", "wrong", "").Result(),
	} {
		if ct := w.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("%s response content-type %q, want application/json", name, ct)
		}
	}
}

// TestStreamHeadersDefeatProxyBuffering pins the two headers whose absence
// turns a stream into a single delayed blob. Nothing in the response body
// changes when they are dropped, so only an explicit assertion catches it.
func TestStreamHeadersDefeatProxyBuffering(t *testing.T) {
	s := testServer(t, &fakeAgent{reply: "hi"})
	w := do(t, s, http.MethodPost, "/v1/chat/completions", "secret",
		`{"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	for h, want := range map[string]string{
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	} {
		if got := w.Header().Get(h); got != want {
			t.Fatalf("%s is %q, want %q; a buffering proxy would defeat streaming", h, got, want)
		}
	}
}

// TestCompletionIDsAreUnique pins the claim newID's own comment makes. A
// constant id makes every completion in a client's logs indistinguishable, and
// the prefix is what OpenAI clients match on.
func TestCompletionIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		id := newID()
		if !strings.HasPrefix(id, "chatcmpl-") {
			t.Fatalf("id %q lacks the chatcmpl- prefix clients match on", id)
		}
		if seen[id] {
			t.Fatalf("newID repeated %q after %d calls", id, i)
		}
		seen[id] = true
	}
}

// TestRejectionLogIsThrottled pins the 401 log limiter. Unauthenticated
// requests are the one thing an attacker can generate without a credential, so
// a line per request is a disk-fill primitive on any install that redirects the
// log to a file. The dropped count must still be reported: a silent limiter
// would make a password spray look like one stray request.
func TestRejectionLogIsThrottled(t *testing.T) {
	var l limiter
	n, ok := l.note()
	if !ok || n != 1 {
		t.Fatalf("first event: (%d, %v), want (1, true)", n, ok)
	}
	for i := 0; i < 500; i++ {
		if _, ok := l.note(); ok {
			t.Fatalf("event %d logged inside the %s window", i, rejectionLogWindow)
		}
	}
	// Pretend the window elapsed. The next line must cover everything dropped.
	l.last = time.Now().Add(-2 * rejectionLogWindow)
	n, ok = l.note()
	if !ok || n != 501 {
		t.Fatalf("after the window: (%d, %v), want (501, true) — the count must cover the burst", n, ok)
	}
}
