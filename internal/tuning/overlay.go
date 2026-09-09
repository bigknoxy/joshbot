package tuning

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// This file's load contract deliberately differs from events.go's.
// loadEvents is lenient (skips a malformed line) because the events file is
// a rolling signal stream the tuner rebuilds its own state from. The overlay
// is different: it is the one place this package writes a value that
// directly changes what timeout the web tool uses, so a file that exists but
// cannot be parsed must never be silently treated as "no adjustment" — that
// would be indistinguishable from a healthy, untouched install and would
// hide real corruption from the operator.
//
// This is the same "never paper over a document a caller intends to trust"
// contract config.LoadStrict follows for config.json. It cannot literally be
// a call to config.LoadStrict — that function's signature is typed to
// *config.Config, not a generic JSON shape — so LoadOverlay below
// reimplements the same fail-loud contract by hand. A missing file is not
// corruption, though: it is what every install looks like before its first
// tune, so LoadOverlay treats os.IsNotExist as an empty overlay rather than
// an error, and only a file that exists but fails to parse is fatal.

// OverlayEntry is the tuner's adjustment for one tool, layered on top of its
// configured (or package-default) timeout. BumpNanos is a plain int64 rather
// than config.Duration: this file is never part of the operator's
// config.json schema (see NewTuner's caller for how it is merged on top of
// the config-file value instead), so it carries none of config.Duration's
// "must round-trip through an operator-edited JSON file" obligations.
type OverlayEntry struct {
	BumpNanos int64     `json:"bump_nanos"`
	TunedAt   time.Time `json:"tuned_at"`
	StepCount int       `json:"step_count"`
}

// Bump returns the entry's adjustment as a time.Duration.
func (e OverlayEntry) Bump() time.Duration {
	return time.Duration(e.BumpNanos)
}

// Overlay is the small persisted document the tuner writes: one
// OverlayEntry per tool it has ever adjusted. Tools absent from the map have
// never been tuned and carry no adjustment.
type Overlay struct {
	Tools map[string]OverlayEntry `json:"tools"`
}

// LoadOverlay reads the overlay at path. A missing file returns an empty,
// non-nil Overlay with no error — see this file's top comment for why that
// is not the same case as a file that exists but is corrupt, which returns a
// non-nil error instead of silently substituting an empty overlay.
func LoadOverlay(path string) (*Overlay, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Overlay{Tools: map[string]OverlayEntry{}}, nil
		}
		return nil, fmt.Errorf("failed to read tuning overlay %s: %w", path, err)
	}

	var ov Overlay
	if err := json.Unmarshal(data, &ov); err != nil {
		return nil, fmt.Errorf("tuning overlay %s is corrupt and was not applied: %w", path, err)
	}
	if ov.Tools == nil {
		ov.Tools = map[string]OverlayEntry{}
	}
	return &ov, nil
}

// SaveOverlay writes ov to path atomically: a uniquely named temp file in
// the same directory, then a rename over the target. This is a local copy of
// internal/session's writeFileAtomic (CreateTemp + Chmod + Write + Sync +
// Close + Rename) rather than an import of it — that helper is unexported to
// internal/session and this package has no other reason to depend on it.
func SaveOverlay(path string, ov *Overlay) error {
	data, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal tuning overlay: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create tuning overlay directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary overlay file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to set temporary overlay file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to write temporary overlay file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to sync temporary overlay file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close temporary overlay file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("failed to rename temporary overlay file: %w", err)
	}
	return nil
}
