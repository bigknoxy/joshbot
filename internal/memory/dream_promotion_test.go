package memory

import (
	"context"
	"os"

	"strings"
	"sync"
	"testing"
)

// A promotion that fails must not destroy its input.
//
// The raw log is the input to the promotion and the facts in MEMORY.md are
// the output. Trimming the input before the output has landed means the
// facts are lost for good: the consolidated log is only ever read, so
// nothing promotes it a second time, and the next pass has nothing left to
// work from.
func TestConsolidateAndPromoteKeepsTheRawLogWhenThePromotionFails(t *testing.T) {
	ws := t.TempDir()
	ctx := context.Background()

	mgr, err := New(ws, WithDream(WithDreamMode(DreamFull)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, e := range []string{
		"deploy pipeline runs on github actions",
		"github actions runs the deploy pipeline nightly",
	} {
		if err := mgr.AppendHistory(ctx, e); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
	}

	raw := mgr.Dream().CountRawRecords()
	if raw == 0 {
		t.Fatal("no raw records were recorded")
	}

	// Make the promotion fail: the facts cannot be written when MEMORY.md
	// cannot be written. A directory in its place fails the write for every
	// user, including root - a read-only file does not, and a test that only
	// fails for a non-root user is a test that says nothing.
	memPath := mgr.MemoryPath()
	if err := os.Mkdir(memPath, 0o750); err != nil {
		t.Fatalf("mkdir MEMORY.md: %v", err)
	}

	err = mgr.Dream().ConsolidateAndPromote(ctx, mgr)
	if err == nil {
		t.Fatal("expected the failed promotion to be reported")
	}
	if !strings.Contains(err.Error(), "reconcile dream facts") {
		t.Errorf("error %q does not say the promotion failed", err)
	}

	// The input is still there for the next pass.
	if got := mgr.Dream().CountRawRecords(); got != raw {
		t.Errorf("the raw log holds %d record(s), want %d - the failed promotion destroyed its input", got, raw)
	}
}

// Two passes must not write the same insights twice.
//
// The background ticker and the `joshbot memory consolidate` command can both
// ask for a pass. Without a lock they read the same raw records and append
// the same insights to the durable log, and the agent learns the same thing
// twice.
func TestConsolidateAndPromoteSerialisesConcurrentPasses(t *testing.T) {
	ws := t.TempDir()
	ctx := context.Background()

	mgr, err := New(ws, WithDream(WithDreamMode(DreamFull)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, e := range []string{
		"deploy pipeline runs on github actions",
		"github actions runs the deploy pipeline nightly",
		"user prefers dark mode in the terminal",
	} {
		if err := mgr.AppendHistory(ctx, e); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mgr.Dream().ConsolidateAndPromote(ctx, mgr); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent pass failed: %v", err)
	}

	insights, err := mgr.Dream().ListConsolidated()
	if err != nil {
		t.Fatalf("ListConsolidated: %v", err)
	}
	// The first pass consolidates three records into a small number of
	// insights; four passes must not produce four times that.
	if len(insights) > 4 {
		t.Errorf("the durable log holds %d insights after four concurrent passes; the passes were not serialised", len(insights))
	}
}
