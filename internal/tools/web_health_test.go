package tools

import (
	"sync"
	"testing"
	"time"
)

// TestWebHealthDeprioritizesOnFailure verifies markFailure moves a backend to
// the back of orderedBackends while leaving the relative order of the other
// backends untouched — deprioritized, never dropped, mirroring
// internal/providers/health.go's shape.
func TestWebHealthDeprioritizesOnFailure(t *testing.T) {
	h := newWebBackendHealth()
	names := []string{"exa-cli", "exa-mcp", "duckduckgo"}

	h.markFailure("web_search", "exa-cli")

	got := h.orderedBackends("web_search", names)
	want := []string{"exa-mcp", "duckduckgo", "exa-cli"}
	if !equalStrings(got, want) {
		t.Fatalf("orderedBackends after one failure = %v, want %v", got, want)
	}
}

// TestWebHealthResetsOnSuccess verifies markSuccess on a previously-failed
// backend restores it to the front, matching providers.markSuccess's
// "any success clears the whole failure history" rule.
func TestWebHealthResetsOnSuccess(t *testing.T) {
	h := newWebBackendHealth()
	names := []string{"exa-cli", "exa-mcp", "duckduckgo"}

	h.markFailure("web_search", "exa-cli")
	h.markFailure("web_search", "exa-cli")
	h.markSuccess("web_search", "exa-cli")

	got := h.orderedBackends("web_search", names)
	want := []string{"exa-cli", "exa-mcp", "duckduckgo"}
	if !equalStrings(got, want) {
		t.Fatalf("orderedBackends after reset = %v, want %v", got, want)
	}
	if h.inCooldown("web_search", "exa-cli") {
		t.Fatal("exa-cli should not be in cooldown after markSuccess")
	}
}

// TestWebHealthIsPerOperation verifies a failure recorded for one operation
// (e.g. "web_code") does not deprioritize the same backend name for a
// different operation (e.g. "web_search") — each web_* operation gets its
// own independent cooldown state.
func TestWebHealthIsPerOperation(t *testing.T) {
	h := newWebBackendHealth()
	names := []string{"exa-cli", "exa-mcp", "duckduckgo"}

	h.markFailure("web_code", "exa-cli")

	gotCode := h.orderedBackends("web_code", names)
	wantCode := []string{"exa-mcp", "duckduckgo", "exa-cli"}
	if !equalStrings(gotCode, wantCode) {
		t.Fatalf("orderedBackends(web_code) = %v, want %v", gotCode, wantCode)
	}

	gotSearch := h.orderedBackends("web_search", names)
	if !equalStrings(gotSearch, names) {
		t.Fatalf("orderedBackends(web_search) = %v, want unaffected %v", gotSearch, names)
	}
}

// TestWebHealthConcurrentAccess hammers markFailure/markSuccess/
// orderedBackends from many goroutines on one shared *webBackendHealth. Run
// under `go test -race`; it proves no data race in the cooldown map, per
// requirement A.4.
func TestWebHealthConcurrentAccess(t *testing.T) {
	h := newWebBackendHealth()
	names := []string{"exa-cli", "exa-mcp", "duckduckgo"}
	ops := []string{"web_search", "web_code", "web_company", "web_research"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			h.markFailure(ops[i%len(ops)], names[i%len(names)])
		}(i)
		go func(i int) {
			defer wg.Done()
			h.markSuccess(ops[i%len(ops)], names[i%len(names)])
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = h.orderedBackends(ops[i%len(ops)], names)
			_ = h.inCooldown(ops[i%len(ops)], names[i%len(names)])
		}(i)
	}
	wg.Wait()
}

// TestWebHealthOrderedBackendsIsStable verifies orderedBackends does not
// mutate the caller's input slice — it must return a new slice, since
// callers pass a package-level var or a fixed literal that must not be
// reordered in place across calls.
func TestWebHealthOrderedBackendsIsStable(t *testing.T) {
	h := newWebBackendHealth()
	names := []string{"exa-cli", "exa-mcp", "duckduckgo"}
	original := append([]string(nil), names...)

	h.markFailure("web_search", "exa-cli")
	_ = h.orderedBackends("web_search", names)

	if !equalStrings(names, original) {
		t.Fatalf("orderedBackends mutated its input slice: got %v, want unchanged %v", names, original)
	}
}

// TestWebHealthCoolUntilClearsEventually is a sanity check that cooldown is
// time-bound, not a permanent removal — inCooldown must become false again
// once enough time passes, so a wrong guess costs latency and not
// availability (mirrors providers.providerHealth's documented contract).
func TestWebHealthCoolUntilClearsEventually(t *testing.T) {
	h := newWebBackendHealth()
	now := time.Now()
	h.now = func() time.Time { return now }

	h.markFailure("web_search", "exa-cli")
	h.markFailure("web_search", "exa-cli")
	if !h.inCooldown("web_search", "exa-cli") {
		t.Fatal("expected exa-cli to be in cooldown after repeated failures")
	}

	h.now = func() time.Time { return now.Add(time.Hour) }
	if h.inCooldown("web_search", "exa-cli") {
		t.Fatal("expected cooldown to have expired after an hour")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
