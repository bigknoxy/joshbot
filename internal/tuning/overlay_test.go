package tuning

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestOverlayRoundTrip: write then LoadOverlay, values match.
func TestOverlayRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tuning_overlay.json")

	ov := &Overlay{Tools: map[string]OverlayEntry{
		"web_search": {BumpNanos: int64(10 * time.Second), TunedAt: time.Unix(5000, 0), StepCount: 2},
	}}
	if err := SaveOverlay(path, ov); err != nil {
		t.Fatalf("SaveOverlay: %v", err)
	}

	got, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	entry, ok := got.Tools["web_search"]
	if !ok {
		t.Fatalf("loaded overlay missing web_search entry: %+v", got)
	}
	if entry.Bump() != 10*time.Second {
		t.Fatalf("bump = %v, want 10s", entry.Bump())
	}
	if entry.StepCount != 2 {
		t.Fatalf("step count = %d, want 2", entry.StepCount)
	}
	if !entry.TunedAt.Equal(time.Unix(5000, 0)) {
		t.Fatalf("tuned_at = %v, want %v", entry.TunedAt, time.Unix(5000, 0))
	}
}

// TestOverlayLoadMissingFileReturnsEmptyNotError: a fresh install has no
// overlay yet -- that is the normal state, not a corruption.
func TestOverlayLoadMissingFileReturnsEmptyNotError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	ov, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay on a missing file returned an error: %v", err)
	}
	if ov == nil || len(ov.Tools) != 0 {
		t.Fatalf("LoadOverlay on a missing file = %+v, want an empty overlay", ov)
	}
}

// TestOverlayLoadCorruptFileReturnsError pins the fail-loud contract this
// file's top comment documents: LoadOverlay must never silently substitute
// an empty overlay for a file that exists but cannot be parsed -- that is
// the same "never paper over a config a caller intends to trust" contract
// config.LoadStrict follows for config.json, adapted to a generic JSON shape
// LoadStrict itself cannot express (it is typed to *config.Config).
func TestOverlayLoadCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tuning_overlay.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadOverlay(path)
	if err == nil {
		t.Fatal("LoadOverlay accepted a corrupt overlay file and returned no error")
	}
}
