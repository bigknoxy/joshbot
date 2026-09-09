package tools

import (
	"context"
	"testing"
	"time"
)

// tolerance absorbs scheduling jitter between when webPerCallDeadline computes
// a deadline and when the test reads it back.
const deadlineTolerance = 300 * time.Millisecond

// TestWebPerCallDeadlineDerivesFromRemainingBudget verifies that, when the
// incoming context carries a deadline, the sub-context's deadline is the
// remaining budget minus the finish reserve.
func TestWebPerCallDeadlineDerivesFromRemainingBudget(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelParent()

	sub, cancel := webPerCallDeadline(parent, 20*time.Second, 5*time.Second, 2*time.Second)
	defer cancel()

	deadline, ok := sub.Deadline()
	if !ok {
		t.Fatal("expected sub-context to carry a deadline")
	}
	remaining := time.Until(deadline)
	want := 8 * time.Second // 10s remaining - 2s reserve
	if diff := remaining - want; diff > deadlineTolerance || diff < -deadlineTolerance {
		t.Fatalf("sub-context deadline is %v from now, want ~%v", remaining, want)
	}
}

// TestWebPerCallDeadlineClampsToMaxBudget verifies the derived sub-timeout
// never exceeds maxBudget even when the parent's remaining budget (minus
// reserve) is much larger — one leg of a multi-tier fallback chain must not
// be allowed to eat the whole remaining turn.
func TestWebPerCallDeadlineClampsToMaxBudget(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelParent()

	sub, cancel := webPerCallDeadline(parent, 5*time.Second, 5*time.Second, 1*time.Second)
	defer cancel()

	deadline, ok := sub.Deadline()
	if !ok {
		t.Fatal("expected sub-context to carry a deadline")
	}
	remaining := time.Until(deadline)
	if remaining > 5*time.Second+deadlineTolerance {
		t.Fatalf("sub-context deadline is %v from now, want clamped to ~5s", remaining)
	}
}

// TestWebPerCallDeadlineFallsBackToStaticDefaultWhenNoDeadline verifies that,
// when the incoming context carries no deadline at all (a subagent or
// programmatic caller with none), the sub-context is timed at exactly the
// per-tool static default rather than derived from anything.
func TestWebPerCallDeadlineFallsBackToStaticDefaultWhenNoDeadline(t *testing.T) {
	sub, cancel := webPerCallDeadline(context.Background(), 20*time.Second, 5*time.Second, 2*time.Second)
	defer cancel()

	deadline, ok := sub.Deadline()
	if !ok {
		t.Fatal("expected sub-context to carry a deadline")
	}
	remaining := time.Until(deadline)
	want := 5 * time.Second
	if diff := remaining - want; diff > deadlineTolerance || diff < -deadlineTolerance {
		t.Fatalf("sub-context deadline is %v from now, want ~%v (the static default)", remaining, want)
	}
}

// TestWebPerCallDeadlineFloorsRatherThanZero verifies that a context whose
// remaining budget minus the reserve is already exhausted (or negative)
// still gets a short, positive window rather than an instantly-expired
// context — so the caller can attempt the call and fail with a real error
// instead of a deadline that expired before exec.CommandContext even started
// the process.
func TestWebPerCallDeadlineFloorsRatherThanZero(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelParent()

	// Reserve (5s) far exceeds the remaining budget (1s), so the naive
	// subtraction would be deeply negative.
	sub, cancel := webPerCallDeadline(parent, 20*time.Second, 5*time.Second, 5*time.Second)
	defer cancel()

	deadline, ok := sub.Deadline()
	if !ok {
		t.Fatal("expected sub-context to carry a deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		t.Fatalf("sub-context deadline already expired (%v from now), want a positive floor", remaining)
	}
	if remaining > 2*time.Second {
		t.Fatalf("sub-context deadline is %v from now, want a small positive floor, not a large window", remaining)
	}
}
