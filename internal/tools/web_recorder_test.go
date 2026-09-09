package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeTimeoutRecorder is a minimal tools.TimeoutRecorder test double that
// records every call it receives, guarded by a mutex since the web tool can
// be invoked concurrently across channels.
type fakeTimeoutRecorder struct {
	mu    sync.Mutex
	calls []struct {
		tool     string
		timedOut bool
	}
}

func (f *fakeTimeoutRecorder) Record(tool string, timedOut bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct {
		tool     string
		timedOut bool
	}{tool, timedOut})
}

func (f *fakeTimeoutRecorder) snapshot() []struct {
	tool     string
	timedOut bool
} {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]struct {
		tool     string
		timedOut bool
	}, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestWebToolWithTimeoutRecorderRecordsTimeoutOutcome proves the recorder
// hook fires from the right place -- when a per-call deadline actually
// expires -- without internal/tools importing internal/tuning (the fake here
// satisfies TimeoutRecorder structurally, exactly as internal/tuning.Tracker
// will in production).
func TestWebToolWithTimeoutRecorderRecordsTimeoutOutcome(t *testing.T) {
	// Every generic-search leg the fallback chain reaches after the native
	// leg times out must fail fast and without a real network dependency, so
	// the server refuses everything with 500s (exa-cli itself is missing
	// from PATH in the test environment, which fails instantly too).
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	rec := &fakeTimeoutRecorder{}
	tool := newTestWebTool(t, srv)
	tool.exaCLIAvailable = true
	tool.health = newWebBackendHealth()
	tool.recorder = rec

	// native blocks until its context is done, so it deterministically
	// observes a real DeadlineExceeded fired by webPerCallDeadline's floor
	// (minPerCallTimeout), rather than racing a real network call.
	native := func(ctx context.Context) ([]SearchResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	res := tool.specializedWithFallback(context.Background(), "web_code", "q", 1, 0, func(string) {}, native, "none")
	if res.Error == nil {
		t.Fatalf("expected an error result since every leg was made to fail, got %+v", res)
	}

	calls := rec.snapshot()
	found := false
	for _, c := range calls {
		if c.tool == "web_code" && c.timedOut {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a Record(\"web_code\", timedOut=true) call, got %+v", calls)
	}
}

// TestWebToolTimeoutRecorderNilIsNoOp proves a WebTool built with no recorder
// (the common case -- most WebTool construction paths, including every
// existing test, never set one) never panics when a fallback leg fails.
func TestWebToolTimeoutRecorderNilIsNoOp(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tool := newTestWebTool(t, srv)
	tool.exaCLIAvailable = true
	tool.health = newWebBackendHealth()
	native := func(ctx context.Context) ([]SearchResult, error) {
		return nil, errors.New("boom")
	}
	res := tool.specializedWithFallback(context.Background(), "web_code", "q", 1, time.Second, func(string) {}, native, "none")
	if res.Error == nil {
		t.Fatalf("expected an error result, got %+v", res)
	}
}
