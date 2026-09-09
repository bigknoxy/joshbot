package tuning

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// eventFileMode mirrors internal/session's sessionFileMode: owner-only,
// because the event stream can carry operator-identifying tool-usage
// patterns even though it holds no conversation content.
const eventFileMode = 0o600

// appendEvent appends one JSON-encoded event as a line to path, creating the
// parent directory and the file if either is missing.
//
// This mirrors internal/session.Manager.Archive's shape: open with
// O_CREATE|O_WRONLY|O_APPEND, write one marshaled line, fsync before
// returning. Append-only because the events file is the only durable record
// of what actually happened; a truncating rewrite that failed partway would
// lose it, the same reasoning that keeps Archive append-only rather than
// rewriting the whole file.
//
// Unlike Archive, this is NOT called under a mutex: Tracker.Record releases
// t.mu before calling appendEvent, so concurrent Record calls (from
// concurrent Telegram/CLI/API turns) can call this concurrently on the same
// path. Correctness here relies entirely on O_APPEND making each Write a
// single atomic append syscall — safe for the one-Write-per-call shape this
// function has today. If this function ever grows a second write, a
// read-modify-write, or anything else that is not one atomic Write, it needs
// its own lock (or must go back under t.mu) — do not assume the caller
// serializes this for you.
func appendEvent(path string, ev Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create tuning events directory: %w", err)
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("failed to marshal tuning event: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, eventFileMode)
	if err != nil {
		return fmt.Errorf("failed to open tuning events file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to append tuning event: %w", err)
	}
	return f.Sync()
}

// loadEvents reads every event previously appended to path. A missing file
// is not an error -- it is what a fresh install looks like, exactly as an
// empty Tracker window is the correct starting state.
//
// Unlike overlay.go's LoadOverlay, a malformed line here is skipped rather
// than treated as fatal: this file is a rolling signal stream the tuner
// rebuilds its own working state from, not an operator-trusted document the
// way config.json or the overlay's own small JSON object is (see
// session.Manager.Load's identical leniency for its own JSONL, and
// overlay.go's top comment for why the two files' load contracts
// deliberately differ).
func loadEvents(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read tuning events file: %w", err)
	}

	var events []Event
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events, nil
}
