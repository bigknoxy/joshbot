package tuning

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestTrackerPersistsAcrossRestart is the core requirement this package
// exists for: in-memory-only counters are explicitly wrong (a personal
// assistant restarts often enough that they never accumulate signal), so a
// fresh Tracker constructed from the same JSONL path after events were
// recorded through an earlier instance must see the same failure-rate state
// -- simulating a process restart without actually restarting one.
func TestTrackerPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tuning_events.jsonl")

	tracker1, err := NewTracker(path)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	tracker1.now = func() time.Time { return time.Unix(1000, 0) }
	for i := 0; i < 3; i++ {
		tracker1.Record("web_search", true)
	}
	for i := 0; i < 2; i++ {
		tracker1.Record("web_search", false)
	}

	rate1, n1 := tracker1.Stats("web_search", time.Hour)

	// Simulate a restart: a brand new Tracker built from the same path.
	tracker2, err := NewTracker(path)
	if err != nil {
		t.Fatalf("NewTracker (restart): %v", err)
	}
	tracker2.now = func() time.Time { return time.Unix(1000, 0) }
	rate2, n2 := tracker2.Stats("web_search", time.Hour)

	if n1 != 5 || n2 != 5 {
		t.Fatalf("sample counts = %d, %d; want 5, 5", n1, n2)
	}
	if rate1 != rate2 {
		t.Fatalf("failure rate before/after restart differs: %v vs %v", rate1, rate2)
	}
	wantRate := 3.0 / 5.0
	if rate2 != wantRate {
		t.Fatalf("restarted tracker failure rate = %v, want %v", rate2, wantRate)
	}
}

// TestTrackerStatsIgnoresEventsOutsideTheWindow proves the rolling window is
// actually rolling -- an old event must not keep dragging the rate down (or
// up) forever.
func TestTrackerStatsIgnoresEventsOutsideTheWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tuning_events.jsonl")
	tracker, err := NewTracker(path)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}

	base := time.Unix(10000, 0)
	tracker.now = func() time.Time { return base.Add(-2 * time.Hour) }
	tracker.Record("web_code", true) // outside the 1h window once "now" advances

	tracker.now = func() time.Time { return base }
	tracker.Record("web_code", false)

	rate, n := tracker.Stats("web_code", time.Hour)
	if n != 1 {
		t.Fatalf("sample count = %d, want 1 (the old event must be excluded)", n)
	}
	if rate != 0 {
		t.Fatalf("failure rate = %v, want 0", rate)
	}
}

// TestTrackerStatsIsPerTool proves events recorded for one tool never bleed
// into another tool's failure rate.
func TestTrackerStatsIsPerTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tuning_events.jsonl")
	tracker, err := NewTracker(path)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	tracker.Record("web_search", true)
	tracker.Record("web_search", true)
	tracker.Record("web_code", false)

	rate, n := tracker.Stats("web_code", time.Hour)
	if n != 1 || rate != 0 {
		t.Fatalf("web_code stats = (%v, %d), want (0, 1)", rate, n)
	}
}

// TestTrackerRecentReturnsNewestFirst covers the CLI status command's
// underlying data source.
func TestTrackerRecentReturnsNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tuning_events.jsonl")
	tracker, err := NewTracker(path)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	tracker.Record("web_search", false)
	tracker.Record("web_code", true)
	tracker.Record("web_search", true)

	all := tracker.Recent("", 2)
	if len(all) != 2 {
		t.Fatalf("Recent(\"\", 2) returned %d events, want 2", len(all))
	}
	if all[0].Tool != "web_search" || !all[0].TimedOut {
		t.Fatalf("newest event = %+v, want the last web_search timeout recorded", all[0])
	}

	filtered := tracker.Recent("web_code", 10)
	if len(filtered) != 1 || filtered[0].Tool != "web_code" {
		t.Fatalf("Recent(\"web_code\", 10) = %+v, want exactly the one web_code event", filtered)
	}
}

// TestTrackerPruneBoundsInMemoryWindow proves a long-running process does
// not grow the in-memory window without limit -- only the resident slice is
// capped, oldest first.
func TestTrackerPruneBoundsInMemoryWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tuning_events.jsonl")
	tracker, err := NewTracker(path)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}

	// Seed just under the cap directly (avoiding maxTrackedEvents real
	// Record calls, each of which appends to disk) then push it over with
	// one real Record call that must trigger the prune.
	tracker.mu.Lock()
	tracker.events = make([]Event, maxTrackedEvents)
	for i := range tracker.events {
		tracker.events[i] = Event{Time: time.Unix(int64(i), 0), Tool: "web_search"}
	}
	tracker.mu.Unlock()

	tracker.Record("web_search", true)

	tracker.mu.Lock()
	n := len(tracker.events)
	newestKept := tracker.events[len(tracker.events)-1]
	oldestKept := tracker.events[0]
	tracker.mu.Unlock()

	if n != maxTrackedEvents {
		t.Fatalf("in-memory window length = %d, want capped at %d", n, maxTrackedEvents)
	}
	if !newestKept.TimedOut {
		t.Fatalf("the just-recorded event was pruned instead of the oldest one")
	}
	if oldestKept.Time.Unix() != 1 {
		t.Fatalf("oldest kept event has time %v, want index 1 (index 0 should have been dropped)", oldestKept.Time)
	}
}

// TestTrackerRecordIsRaceClean is the concurrency requirement: the web tool
// can be invoked concurrently across Telegram/CLI/API turns, so Record must
// be safe to call from many goroutines at once. Run with `go test -race`.
func TestTrackerRecordIsRaceClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tuning_events.jsonl")
	tracker, err := NewTracker(path)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tracker.Record("web_search", n%2 == 0)
			_, _ = tracker.Stats("web_search", time.Hour)
		}(i)
	}
	wg.Wait()

	_, n := tracker.Stats("web_search", time.Hour)
	if n != 50 {
		t.Fatalf("sample count after concurrent Record = %d, want 50", n)
	}
}
