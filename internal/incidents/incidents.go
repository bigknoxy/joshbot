// Package incidents implements a bounded, append-only record of agent turn
// failures — turn timeouts, LLM failures and mid-stream deaths — so an
// operator can see what actually went wrong after the fact instead of
// scrolling a chat transcript.
//
// It deliberately mirrors internal/tuning's shape: a small struct, a JSONL
// file at owner-only mode, an in-memory window pruned to a fixed cap, and a
// lenient load that skips malformed lines. Like tuning, this is not an audit
// trail: `Clear` truncates. The record exists to answer "what happened and
// how often", never to preserve history forever.
//
// The package imports only stdlib and internal/redact, so internal/agent and
// cmd/joshbot can both hold it without a cycle in either direction.
package incidents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/bigknoxy/joshbot/internal/redact"
)

// FileName is the incidents file's name inside the joshbot home directory
// (~/.joshbot by default). The joining is done by the caller (cmd/joshbot)
// so this package never imports internal/config.
const FileName = "incidents.jsonl"

// Incident types.
const (
	TurnTimeout = "turn_timeout"
	LLMFailure  = "llm_failure"
	StreamDied  = "stream_died"
)

// fileMode mirrors internal/session and internal/tuning: owner-only, because
// incident details can carry provider error text that mentions
// operator-specific configuration.
const fileMode = 0o600

// maxIncidents bounds the in-memory window the same way tuning's
// maxTrackedEvents does: only what the process keeps resident is capped,
// oldest first. The file is never trimmed by this.
const maxIncidents = 500

// Self-heal arithmetic (pure, deterministic — the same contract tuning's
// Evaluate carries): look at the trailing window, count turn_timeout
// incidents, and derive a bounded bump. Nothing here mutates config.json or
// a running agent; the bump is read at startup and takes effect on the next
// restart, exactly like every other config.Duration.
const (
	// HealLookbackWindow is how far back a timeout counts toward the bump.
	HealLookbackWindow = 24 * time.Hour
	// HealBumpPerTimeout is the extra timeout each recent turn_timeout earns.
	HealBumpPerTimeout = 30 * time.Second
	// HealMaxTimeoutsCounted caps how many timeouts contribute, so the bump
	// itself is bounded: HealBumpPerTimeout * HealMaxTimeoutsCounted.
	HealMaxTimeoutsCounted = 4
)

// HealMaxBump is the largest bump TimeoutBump can ever return.
const HealMaxBump = HealBumpPerTimeout * HealMaxTimeoutsCounted

// heal_timeouts values, mirrored in config.Validate. Kept here (not in
// internal/config) so the incidents package stays the single owner of the
// self-heal arithmetic; config imports this package to share them.
const (
	// HealOff is the zero-value default: no self-healing.
	HealOff = "off"
	// HealBump enables the startup timeout bump.
	HealBump = "bump"
)

// Incident is one recorded turn failure. Detail is short, already-redacted
// prose (the error's first line, never a full tool result).
type Incident struct {
	Time         time.Time `json:"time"`
	Type         string    `json:"type"`
	Session      string    `json:"session,omitempty"`
	Channel      string    `json:"channel,omitempty"`
	Model        string    `json:"model,omitempty"`
	ElapsedNanos int64     `json:"elapsed_nanos,omitempty"`
	Iterations   int       `json:"iterations,omitempty"`
	ToolCalls    int       `json:"tool_calls,omitempty"`
	Detail       string    `json:"detail,omitempty"`
}

// Log holds a persisted rolling window of incidents. It is safe for
// concurrent use: Record is called from whichever turn goroutine just failed,
// and concurrent Telegram/CLI/API turns can race.
type Log struct {
	mu        sync.Mutex
	path      string
	incidents []Incident
	// now is overridable for tests; production leaves it as time.Now.
	now func() time.Time
}

// NewLog constructs a Log backed by the JSONL file at path, replaying every
// previously persisted incident into memory. A fresh install has no file,
// which is exactly an empty window.
func NewLog(path string) (*Log, error) {
	incidents, err := loadIncidents(path)
	if err != nil {
		return nil, err
	}
	l := &Log{
		path:      path,
		incidents: incidents,
		now:       time.Now,
	}
	l.pruneLocked()
	return l, nil
}

// Record appends one incident to the in-memory window and persists it to
// disk. A persistence failure is logged and swallowed rather than returned:
// Record runs on a turn's error path, where a disk hiccup must not become a
// second failure on top of the first — the in-memory window still advances
// either way, it is only the next restart's recall that is at risk.
func (l *Log) Record(inc Incident) {
	if inc.Time.IsZero() {
		inc.Time = l.now()
	}
	inc.Detail = redact.String(inc.Detail)

	l.mu.Lock()
	l.incidents = append(l.incidents, inc)
	l.pruneLocked()
	path := l.path
	l.mu.Unlock()

	if err := appendIncident(path, inc); err != nil {
		log.Warn("incidents: failed to persist incident", "type", inc.Type, "error", err)
	}
}

// pruneLocked drops the oldest incidents once the in-memory window exceeds
// maxIncidents. Callers must hold l.mu.
func (l *Log) pruneLocked() {
	if len(l.incidents) <= maxIncidents {
		return
	}
	drop := len(l.incidents) - maxIncidents
	l.incidents = l.incidents[drop:]
}

// Recent returns up to limit of the most recently recorded incidents, newest
// first. An empty type matches every incident.
func (l *Log) Recent(limit int, typ string) []Incident {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []Incident
	for i := len(l.incidents) - 1; i >= 0 && len(out) < limit; i-- {
		if typ != "" && l.incidents[i].Type != typ {
			continue
		}
		out = append(out, l.incidents[i])
	}
	return out
}

// CountSince returns how many incidents of typ (empty matches any) fall
// inside the trailing window ending now.
func (l *Log) CountSince(window time.Duration, typ string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-window)
	n := 0
	for _, inc := range l.incidents {
		if inc.Time.Before(cutoff) {
			continue
		}
		if typ != "" && inc.Type != typ {
			continue
		}
		n++
	}
	return n
}

// Prune drops incidents older than olderThan from memory and rewrites the
// file. Unlike tuning's events file this rewrite is truncating by design —
// the record exists to be bounded, not to be an audit trail — but the write
// is still atomic (temp file + rename) so a failed prune cannot destroy what
// it was pruning.
func (l *Log) Prune(olderThan time.Duration) (removed int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-olderThan)
	kept := l.incidents[:0]
	for _, inc := range l.incidents {
		if inc.Time.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, inc)
	}
	if removed == 0 {
		return 0, nil
	}
	// rewriteLocked runs under l.mu, so the read-modify-write cannot race a
	// concurrent Record here -- but it CAN fail, and a failed prune must not
	// have already destroyed the in-memory record it was pruning: the
	// in-memory window is the authoritative copy, the file only feeds the
	// next restart. Publish kept only after the rewrite succeeds.
	if err := rewriteLocked(l.path, kept); err != nil {
		return removed, err
	}
	l.incidents = kept
	return removed, nil
}

// Clear removes every incident: the file and the in-memory window.
func (l *Log) Clear() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.incidents = nil
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove incidents file: %w", err)
	}
	return nil
}

// TimeoutBump returns the bounded self-heal bump for turn timeouts: each
// turn_timeout inside HealLookbackWindow adds HealBumpPerTimeout, capped at
// HealMaxTimeoutsCounted timeouts. Zero when nothing recent timed out.
func (l *Log) TimeoutBump() time.Duration {
	n := l.CountSince(HealLookbackWindow, TurnTimeout)
	if n > HealMaxTimeoutsCounted {
		n = HealMaxTimeoutsCounted
	}
	return time.Duration(n) * HealBumpPerTimeout
}

// appendIncident appends one JSON-encoded incident as a line to path,
// creating the parent directory and the file if either is missing.
//
// This mirrors internal/tuning's appendEvent: open with
// O_CREATE|O_WRONLY|O_APPEND and write one marshaled line. It is NOT
// called under a mutex — Record releases l.mu before
// calling it, so concurrent turns can call this concurrently on the same
// path. Correctness relies entirely on O_APPEND making each Write a single
// atomic append syscall for the one-Write-per-call shape this function has.
// If this ever grows a second write or a read-modify-write, it needs its own
// lock — do not assume the caller serializes this for you.
func appendIncident(path string, inc Incident) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create incidents directory: %w", err)
	}

	data, err := json.Marshal(inc)
	if err != nil {
		return fmt.Errorf("failed to marshal incident: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, fileMode)
	if err != nil {
		return fmt.Errorf("failed to open incidents file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to append incident: %w", err)
	}
	// No fsync here: on the dev host one Sync measured ~115ms, and replaying
	// a file that has grown past the cap would burn a minute of CI per test
	// run. Tuning makes the same trade for its own events; the in-memory
	// window is authoritative either way, the file only feeds the next
	// restart's replay.
	return nil
}

// rewriteLocked replaces the file's contents with exactly the given
// incidents, atomically: a uniquely-named temp file followed by a rename, so
// a concurrent reader never sees a torn file. Callers must hold l.mu — this
// is the one file mutation that is not an O_APPEND single Write.
func rewriteLocked(path string, incidents []Incident) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create incidents directory: %w", err)
	}

	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create incidents temp file: %w", err)
	}
	tmpName := f.Name()

	var sb strings.Builder
	for _, inc := range incidents {
		data, err := json.Marshal(inc)
		if err != nil {
			f.Close()
			os.Remove(tmpName)
			return fmt.Errorf("failed to marshal incident: %w", err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	if _, err := f.WriteString(sb.String()); err != nil {
		f.Close()
		os.Remove(tmpName)
		return fmt.Errorf("failed to write incidents temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to close incidents temp file: %w", err)
	}
	if err := os.Chmod(tmpName, fileMode); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to set incidents file mode: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to replace incidents file: %w", err)
	}
	return nil
}

// loadIncidents reads every incident previously appended to path. A missing
// file is not an error — it is what a fresh install looks like, exactly as
// an empty window is the correct starting state.
//
// A malformed line is skipped rather than treated as fatal: this file is a
// rolling signal stream the process rebuilds its own working state from, not
// an operator-trusted document the way config.json is (see tuning's
// loadEvents for the same contract and reasoning).
func loadIncidents(path string) ([]Incident, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read incidents file: %w", err)
	}

	var incidents []Incident
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var inc Incident
		if err := json.Unmarshal([]byte(line), &inc); err != nil {
			continue
		}
		incidents = append(incidents, inc)
	}
	return incidents, nil
}
