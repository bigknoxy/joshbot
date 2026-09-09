package tuning

import (
	"sync"
	"time"

	"github.com/charmbracelet/log"
)

// Tools is the fixed set of web-tool operations this narrow tuner ever
// evaluates. Exported so cmd/joshbot does not need to hand-maintain a second
// copy of this list.
var Tools = []string{"web_search", "web_code", "web_company", "web_research"}

// Fixed (non-operator-configurable) rule parameters beyond the two bounds
// config.TuningConfig actually exposes (MaxBump, Step). Exposing every knob
// of the rule as its own config key was explicitly out of scope — this is
// the narrowed slice, not the general multi-key framework — so Window,
// Threshold, MinSamples and Cooldown are fixed constants here rather than
// config fields.
const (
	// DefaultWindow is how far back a tune decision looks.
	DefaultWindow = 24 * time.Hour
	// DefaultThreshold is the failure rate that triggers a step up.
	DefaultThreshold = 0.5
	// DefaultMinSamples is the minimum number of events inside the window
	// before the rule will act at all.
	DefaultMinSamples = 5
	// DefaultCooldown is the minimum time between two tune events for the
	// same tool — the hysteresis that prevents thrashing.
	DefaultCooldown = 30 * time.Minute
)

// DefaultTunerConfig builds a TunerConfig for Tools using this package's
// fixed rule parameters and the two operator-declared bounds from
// config.TuningConfig (maxBump, step). cmd/joshbot is the only intended
// caller — internal/tuning does not import internal/config, so the caller
// passes the two Duration values already resolved.
func DefaultTunerConfig(maxBump, step time.Duration) TunerConfig {
	return TunerConfig{
		Tools:  Tools,
		Window: DefaultWindow,
		Params: RuleParams{
			Threshold:  DefaultThreshold,
			MinSamples: DefaultMinSamples,
			Step:       step,
			MaxBump:    maxBump,
			Cooldown:   DefaultCooldown,
		},
	}
}

// TunerConfig bounds and paces one Tuner: which tools it evaluates, the
// rolling window each evaluation looks back over, and the pure rule's
// parameters (thresholds, step, operator-declared max bump, cooldown).
type TunerConfig struct {
	// Tools is the fixed list of tool operations this Tuner evaluates —
	// "web_search", "web_code", "web_company", "web_research". A tool
	// outside this list is never evaluated even if events were recorded for
	// it (e.g. by a future caller), which keeps the tuner's surface exactly
	// as narrow as the design calls for.
	Tools []string
	// Window is how far back Tracker.Stats looks when the rule evaluates.
	Window time.Duration
	// Params configures Evaluate (see rule.go).
	Params RuleParams
}

// Tuner ties together the persisted event Tracker, the pure step-up/step-down
// rule, and the on-disk Overlay the adjusted values are written to. It is
// the one thing cmd/joshbot wires up for the whole "per-tool timeout
// auto-tuning" slice.
//
// Tuner.Record forwards to its Tracker and so satisfies tools.TimeoutRecorder
// structurally, exactly as Tracker itself does — a caller may hand either a
// bare *Tracker or a *Tuner to tools.WithTimeoutRecorder.
type Tuner struct {
	tracker     *Tracker
	overlayPath string
	cfg         TunerConfig

	mu      sync.Mutex
	overlay *Overlay
	// now is overridable for tests; production leaves it as time.Now.
	now func() time.Time
}

// NewTuner constructs a Tuner backed by tracker and the overlay file at
// overlayPath, loading any existing overlay (see LoadOverlay's fail-loud
// contract for a corrupt file — that error is returned here, not swallowed,
// because starting a live tuner from state it cannot actually read would
// silently discard whatever adjustment the operator was relying on).
func NewTuner(tracker *Tracker, overlayPath string, cfg TunerConfig) (*Tuner, error) {
	ov, err := LoadOverlay(overlayPath)
	if err != nil {
		return nil, err
	}
	return &Tuner{
		tracker:     tracker,
		overlayPath: overlayPath,
		cfg:         cfg,
		overlay:     ov,
		now:         time.Now,
	}, nil
}

// Record forwards to the underlying Tracker.
func (tu *Tuner) Record(tool string, timedOut bool) {
	tu.tracker.Record(tool, timedOut)
}

// EffectiveTimeout returns base plus whatever bump the tuner has on file for
// tool. A tool the tuner has never adjusted (or one outside cfg.Tools)
// returns base unchanged.
func (tu *Tuner) EffectiveTimeout(tool string, base time.Duration) time.Duration {
	tu.mu.Lock()
	defer tu.mu.Unlock()
	return base + tu.overlay.Tools[tool].Bump()
}

// Stats returns the tracker's failure rate and sample count for tool over
// window — a thin passthrough so a CLI status command need not reach into
// the Tuner's private tracker field.
func (tu *Tuner) Stats(tool string, window time.Duration) (rate float64, n int) {
	return tu.tracker.Stats(tool, window)
}

// RecentEvents returns up to limit of the tracker's most recently recorded
// events across every tool, newest first.
func (tu *Tuner) RecentEvents(limit int) []Event {
	return tu.tracker.Recent("", limit)
}

// Overlay returns a snapshot of the tuner's current per-tool state, for
// `joshbot tuning status` to render. The returned map is a copy — the
// caller may read it freely without racing further tuning.
func (tu *Tuner) OverlaySnapshot() map[string]OverlayEntry {
	tu.mu.Lock()
	defer tu.mu.Unlock()
	out := make(map[string]OverlayEntry, len(tu.overlay.Tools))
	for k, v := range tu.overlay.Tools {
		out[k] = v
	}
	return out
}

// Evaluate runs the rule once for every tool in cfg.Tools and persists any
// resulting change to the overlay file. It is safe to call from a timer
// goroutine (Start) or directly from a CLI command.
func (tu *Tuner) Evaluate() error {
	tu.mu.Lock()
	defer tu.mu.Unlock()

	now := tu.now()
	changed := false
	for _, tool := range tu.cfg.Tools {
		entry := tu.overlay.Tools[tool]
		rate, n := tu.tracker.Stats(tool, tu.cfg.Window)
		decision, newBump := Evaluate(tu.cfg.Params, entry.Bump(), rate, n, entry.TunedAt, now)
		if decision == NoChange {
			continue
		}

		entry.BumpNanos = int64(newBump)
		entry.TunedAt = now
		entry.StepCount++
		tu.overlay.Tools[tool] = entry
		changed = true

		log.Info("tuning: adjusted per-tool timeout", "tool", tool, "decision", decision,
			"bump", newBump, "failure_rate", rate, "samples", n)
	}

	if !changed {
		return nil
	}
	return SaveOverlay(tu.overlayPath, tu.overlay)
}

// Reset clears every tool's overlay adjustment (reverting to the configured
// or default base timeout) and appends a marker event to the tracker's
// append-only event log — never truncating it, since it is meant to be a
// durable audit trail, not working state.
func (tu *Tuner) Reset() error {
	tu.mu.Lock()
	tu.overlay = &Overlay{Tools: map[string]OverlayEntry{}}
	path := tu.overlayPath
	ov := tu.overlay
	tu.mu.Unlock()

	if err := SaveOverlay(path, ov); err != nil {
		return err
	}
	// A dedicated tool name that Tracker.Stats will never see queried for a
	// real tool operation, so it cannot pollute any tool's failure rate —
	// it exists purely as a marker in the append-only history.
	tu.tracker.Record("__tuning_reset__", false)
	return nil
}

// Start runs Evaluate on a fixed interval until the returned stop func is
// called, which blocks until the goroutine has actually exited. The done
// channel is allocated fresh per call rather than stored on the struct
// (mirroring this repo's "restartable objects keep per-run channels off the
// struct" rule for the bus and Discord channel), so a Tuner could in
// principle be Started more than once without one run's shutdown reaching
// into another's channel.
func (tu *Tuner) Start(interval time.Duration) (stop func()) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if err := tu.Evaluate(); err != nil {
					log.Warn("tuning: evaluation failed", "error", err)
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(stopCh) })
		<-done
	}
}
