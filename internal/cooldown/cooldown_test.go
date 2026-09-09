package cooldown

import (
	"testing"
	"time"
)

func TestBackoffBelowThresholdIsZero(t *testing.T) {
	if got := Backoff(1, 2, 0, 15*time.Second, 5*time.Minute, 5); got != 0 {
		t.Errorf("Backoff below threshold with no explicit duration = %v, want 0", got)
	}
}

func TestBackoffGrowsExponentiallyAtThreshold(t *testing.T) {
	base := 15 * time.Second
	max := 5 * time.Minute
	if got := Backoff(2, 2, 0, base, max, 5); got != base {
		t.Errorf("Backoff at threshold = %v, want base %v", got, base)
	}
	if got := Backoff(3, 2, 0, base, max, 5); got != base*2 {
		t.Errorf("Backoff one past threshold = %v, want %v", got, base*2)
	}
}

func TestBackoffCapsAtMax(t *testing.T) {
	base := 15 * time.Second
	max := 5 * time.Minute
	if got := Backoff(100, 2, 0, base, max, 5); got != max {
		t.Errorf("Backoff far past threshold = %v, want capped at max %v", got, max)
	}
}

func TestBackoffExplicitDurationTakesPrecedence(t *testing.T) {
	base := 15 * time.Second
	max := 5 * time.Minute
	explicit := 30 * time.Second
	if got := Backoff(10, 2, explicit, base, max, 5); got != explicit {
		t.Errorf("Backoff with explicit duration = %v, want the explicit %v", got, explicit)
	}
}

func TestBackoffExplicitDurationStillClampedToMax(t *testing.T) {
	max := 5 * time.Minute
	explicit := 10 * time.Minute
	if got := Backoff(1, 2, explicit, 15*time.Second, max, 5); got != max {
		t.Errorf("Backoff with over-max explicit duration = %v, want capped at max %v", got, max)
	}
}

// TestBackoffShiftClampPreventsOverflow pins the exact bug class this
// package exists to fix once instead of twice: an unclamped shift eventually
// overflows an int64 duration negative, which would silently drop the
// cooldown for the most persistently failing key. A huge failure count must
// still land exactly at max, never at a negative or zero duration.
func TestBackoffShiftClampPreventsOverflow(t *testing.T) {
	base := 15 * time.Second
	max := 5 * time.Minute
	got := Backoff(1_000_000, 2, 0, base, max, 5)
	if got != max {
		t.Errorf("Backoff with an enormous failure count = %v, want capped at max %v (no overflow)", got, max)
	}
	if got < 0 {
		t.Fatalf("Backoff returned a negative duration: %v", got)
	}
}
