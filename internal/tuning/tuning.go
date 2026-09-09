// Package tuning implements the narrow per-tool timeout auto-tuning slice:
// a persisted, per-web-tool count of timeout/failure events (Tracker), a
// pure-Go step-up/step-down rule with hysteresis (Evaluate), and a small
// on-disk overlay the adjusted values are written to (Overlay) so they
// survive a restart without ever touching config.json.
//
// This is deliberately not the general multi-key MAPE-K framework: it tunes
// exactly four timeouts (web_search/web_code/web_company/web_research), the
// acceptance criterion for a change is always this package's own arithmetic
// (never an LLM's judgment), and it does nothing at all unless
// config.TuningConfig.Enabled is true.
package tuning

import (
	"sync"
	"time"

	"github.com/charmbracelet/log"
)

// EventsFileName and OverlayFileName are the filenames this package's two
// on-disk documents use under the joshbot home directory (~/.joshbot).
// internal/tuning does not import internal/config, so it has no notion of
// "home directory" itself — cmd/joshbot joins these with config.DefaultHome
// (or cfg.HomeDir()) rather than each maintaining its own copy of the name.
const (
	EventsFileName  = "tuning_events.jsonl"
	OverlayFileName = "tuning_overlay.json"
)

// Event is one persisted timeout/success observation for a single tool
// operation ("web_search", "web_code", "web_company", "web_research").
type Event struct {
	Time     time.Time `json:"time"`
	Tool     string    `json:"tool"`
	TimedOut bool      `json:"timed_out"`
}

// maxTrackedEvents bounds the in-memory window so a long-running process
// does not grow this without limit. The persisted file is never trimmed by
// this -- only what Tracker keeps resident is capped, oldest first.
const maxTrackedEvents = 20000

// Tracker holds a persisted, per-tool rolling window of timeout/failure
// events. It is safe for concurrent use: the web tool can be invoked
// concurrently across Telegram/CLI/API turns, and Record is called from
// whichever of those goroutines just finished a fallback-chain attempt.
//
// Tracker.Record satisfies tools.TimeoutRecorder structurally (Go interfaces
// are structural, so internal/tools never imports this package and this
// package never imports internal/tools -- no cycle in either direction).
type Tracker struct {
	mu     sync.Mutex
	path   string
	events []Event
	// now is overridable for tests; production leaves it as time.Now.
	now func() time.Time
}

// NewTracker constructs a Tracker backed by the JSONL file at path, replaying
// every previously persisted event into memory. This replay is what makes
// counters survive a process restart -- an in-memory-only Tracker would
// start every process with an empty window, and a personal assistant
// restarts often enough that such a window would never accumulate signal.
func NewTracker(path string) (*Tracker, error) {
	events, err := loadEvents(path)
	if err != nil {
		return nil, err
	}
	t := &Tracker{
		path:   path,
		events: events,
		now:    time.Now,
	}
	t.pruneLocked()
	return t, nil
}

// Record appends one event to the tracker's in-memory window and persists it
// to disk. A persistence failure is logged and otherwise swallowed rather
// than returned: Record is called from the hot path of a web-tool fallback
// attempt, where a disk hiccup must not turn into a user-visible tool
// failure -- the in-memory counter still advances either way, it is only the
// next restart's recall that is at risk.
func (t *Tracker) Record(tool string, timedOut bool) {
	ev := Event{Time: t.now(), Tool: tool, TimedOut: timedOut}

	t.mu.Lock()
	t.events = append(t.events, ev)
	t.pruneLocked()
	path := t.path
	t.mu.Unlock()

	if err := appendEvent(path, ev); err != nil {
		log.Warn("tuning: failed to persist event", "tool", tool, "error", err)
	}
}

// pruneLocked drops the oldest events once the in-memory window exceeds
// maxTrackedEvents. Callers must hold t.mu.
func (t *Tracker) pruneLocked() {
	if len(t.events) <= maxTrackedEvents {
		return
	}
	drop := len(t.events) - maxTrackedEvents
	t.events = t.events[drop:]
}

// Stats returns the timeout rate and sample count for tool over the trailing
// window ending now. n is 0 (and rate is 0) when no events for tool fall
// inside the window -- callers (rule.Evaluate via MinSamples) must treat a
// zero sample count as "not enough signal", never as "0% failures".
func (t *Tracker) Stats(tool string, window time.Duration) (rate float64, n int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := t.now().Add(-window)
	var total, timedOut int
	for _, ev := range t.events {
		if ev.Tool != tool {
			continue
		}
		if ev.Time.Before(cutoff) {
			continue
		}
		total++
		if ev.TimedOut {
			timedOut++
		}
	}
	if total == 0 {
		return 0, 0
	}
	return float64(timedOut) / float64(total), total
}

// Recent returns up to limit of the most recently recorded events for tool,
// newest first -- used by `joshbot tuning status` to show a bounded tail of
// history without loading the whole file through the CLI.
func (t *Tracker) Recent(tool string, limit int) []Event {
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []Event
	for i := len(t.events) - 1; i >= 0 && len(out) < limit; i-- {
		if tool != "" && t.events[i].Tool != tool {
			continue
		}
		out = append(out, t.events[i])
	}
	return out
}
