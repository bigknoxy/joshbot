package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// This file hardens the DuckDuckGo scraping fallback (duckDuckGoSearch /
// doSearch) against adversarial and malformed responses: unbounded bodies,
// non-UTF8 bytes, hanging connections, redirect loops, and a single stuck
// engine starving the others of the per-op budget. See CLAUDE.md section C
// of the deadline-aware web hardening design.

// A malicious or broken search-engine response could stream an unbounded
// body; doSearch must cap what it reads rather than buffering the whole
// thing via a bare io.ReadAll, mirroring the existing 100KB cap already
// applied to webFetch.
func TestWebTool_DoSearchCapsResponseBodySize(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("test server ResponseWriter does not support flushing")
		}
		chunk := strings.Repeat("x", 64*1024)
		for {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			default:
			}
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 0

	// The handler streams forever; if doSearch read it unbounded this would
	// hang for the full context timeout. A capped read returns promptly once
	// the limit is hit, well before the deadline below.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	res := tool.doSearch(ctx, "https://example.test/search?q=x", 5)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("doSearch took %v against an endlessly-streaming body; response size is not capped", elapsed)
	}
	// An uncapped body with no recognizable result markup degrades to an
	// honest empty result, not a panic or a giant payload back to the model.
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
}

// A response containing invalid UTF-8 bytes must never panic doSearch, and
// the output handed back to the model must be valid UTF-8 — otherwise it
// corrupts whatever encodes it downstream (session JSON, the tool-result
// message sent to the provider).
func TestWebTool_DoSearchSanitizesNonUTF8Bytes(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`<div class="result">`)
		b.WriteString("\n")
		b.WriteString(`<a class="result__a" href="https://example.com/x">Title `)
		// Invalid UTF-8: a lone continuation byte and an overlong sequence.
		b.Write([]byte{0xff, 0xfe, 0x80})
		b.WriteString(` end</a>`)
		b.WriteString("\n")
		b.WriteString(`<div class="result__snippet">snippet `)
		b.Write([]byte{0xc0, 0xaf})
		b.WriteString(` end</div>`)
		b.WriteString("\n")
		b.WriteString(`</div><!-- result__body -->`)
		w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 0

	res := tool.doSearch(context.Background(), "https://example.test/search?q=x", 5)
	if res.Error != nil {
		t.Fatalf("unexpected error on malformed-but-non-empty body: %v", res.Error)
	}
	if !utf8.ValidString(res.Output) {
		t.Fatalf("doSearch returned invalid UTF-8 output: %q", res.Output)
	}
}

// A server that accepts the connection but never writes anything must not
// hang doSearch past the caller's context deadline — ctx cancellation has to
// actually be honored on the read path, not merely assumed to work because
// http.Client is "supposed to" respect it.
func TestWebTool_DoSearchHonorsCtxOnHangingResponse(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 0

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	res := tool.doSearch(ctx, "https://example.test/search?q=x", 5)
	elapsed := time.Since(start)

	if res.Error == nil {
		t.Fatalf("expected an error on a hanging response, got output %q", res.Output)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("doSearch took %v against a hanging server with a 300ms ctx deadline; ctx is not honored on the read path", elapsed)
	}
}

// A server that always redirects to itself must not hang doSearch forever —
// the retry loop bounds the number of redirects it will follow, so this must
// terminate with a typed error rather than looping.
func TestWebTool_DoSearchRedirectLoopTerminates(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", srv.URL+"/search?q=loop")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 3
	tool.baseDelay = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	res := tool.doSearch(ctx, "https://example.test/search?q=x", 5)
	elapsed := time.Since(start)

	if res.Error == nil {
		t.Fatalf("expected a bounded error out of an infinite redirect loop, got output %q", res.Output)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("doSearch took %v to give up on a redirect loop; the retry bound is not being enforced", elapsed)
	}
}

// A non-200 status must never panic parseSearchResults or doSearch when the
// body itself is malformed/truncated HTML (e.g. a dangling open tag with no
// matching close, or an unterminated attribute).
func TestWebTool_ParseSearchResultsMalformedHTMLDoesNotPanic(t *testing.T) {
	tool := &WebTool{}
	inputs := []string{
		`<div class="result"><a class="result__a" href="https://example.com/x`,
		`<div class="result"><a class="result__a" href="no-closing-quote>Title</a>`,
		`<div class="result">` + strings.Repeat("<", 1000),
		"<div class=\"result\">\xff\xfe\x00binary garbage\x80",
		``,
	}
	for i, in := range inputs {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("parseSearchResults panicked on malformed input: %v", r)
				}
			}()
			_ = tool.parseSearchResults(in, 5)
		})
	}
}

// duckDuckGoSearch's engine loop must try the next engine after a retryable
// status (202/429/5xx) rather than giving up on the first one — pinning the
// documented behavior at web.go's searchEngines loop.
func TestWebTool_DuckDuckGoSearchTriesNextEngineOnRetryableStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"202 without Location", http.StatusAccepted},
		{"429 too many requests", http.StatusTooManyRequests},
		{"503 service unavailable", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int64
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := atomic.AddInt64(&calls, 1)
				if n == 1 {
					w.WriteHeader(tt.status)
					return
				}
				fmt.Fprint(w, `<div class="result">`+"\n"+
					`<a class="result__a" href="https://example.com/x">A Result</a>`+"\n"+
					`</div><!-- result__body -->`)
			}))
			defer srv.Close()

			tool := newTestWebTool(t, srv)
			tool.maxRetries = 0 // exhaust the first engine's own retries fast; the loop must still move on

			res := tool.duckDuckGoSearch(context.Background(), "some query", 3)
			if res.Error != nil {
				t.Fatalf("expected the second engine to succeed after a %s from the first, got error: %v", tt.name, res.Error)
			}
			if !strings.Contains(res.Output, "A Result") {
				t.Errorf("output = %q, want the second engine's result", res.Output)
			}
			if atomic.LoadInt64(&calls) < 2 {
				t.Errorf("only %d request(s) were made; the loop did not try the next engine", calls)
			}
		})
	}
}

// duckDuckGoSearch must stop walking engines the instant ctx.Err() is set,
// even mid-loop after a retryable status from the first engine — it must not
// try a second engine on a dead budget.
func TestWebTool_DuckDuckGoSearchStopsOnRetryableStatusWhenCtxDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		cancel() // the caller's turn ends right after the first engine answers
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 0

	res := tool.duckDuckGoSearch(ctx, "some query", 3)
	if res.Error == nil {
		t.Fatalf("expected an abort error once ctx is done, got output %q", res.Output)
	}
	if !strings.Contains(res.Error.Error(), "search aborted") {
		t.Errorf("error = %q, want the abort message rather than grinding into the next engine", res.Error)
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Errorf("got %d requests, want exactly 1 — the loop must not try a second engine on a dead ctx", calls)
	}
}

// A single engine that hangs past its own share of the budget must not
// starve the remaining engines of the whole per-op deadline: each engine
// gets its own bounded sub-timeout carved out of what is left, so a stuck
// first engine still leaves room for the second to be tried.
func TestWebTool_DuckDuckGoSearchPerEngineTimeoutLeavesBudgetForNextEngine(t *testing.T) {
	var calls int64
	block := make(chan struct{})
	defer close(block)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		if n == 1 {
			// Hang well past any reasonable per-engine share of the budget.
			select {
			case <-block:
			case <-r.Context().Done():
			}
			return
		}
		fmt.Fprint(w, `<div class="result">`+"\n"+
			`<a class="result__a" href="https://example.com/x">A Result</a>`+"\n"+
			`</div><!-- result__body -->`)
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.maxRetries = 0

	// A generous overall per-op budget, but the first engine hangs
	// indefinitely — without a per-engine sub-timeout it would eat the whole
	// thing and the second engine would never be tried.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	start := time.Now()
	res := tool.duckDuckGoSearch(ctx, "some query", 3)
	elapsed := time.Since(start)

	if res.Error != nil {
		t.Fatalf("expected the second engine to be tried and succeed, got error: %v", res.Error)
	}
	if !strings.Contains(res.Output, "A Result") {
		t.Errorf("output = %q, want the second engine's result", res.Output)
	}
	if elapsed >= 6*time.Second {
		t.Errorf("duckDuckGoSearch took %v — the first engine's hang consumed the whole per-op budget instead of its own share", elapsed)
	}
}
