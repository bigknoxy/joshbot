package tuning

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testTunerConfig() TunerConfig {
	return TunerConfig{
		Tools:  []string{"web_search", "web_code"},
		Window: time.Hour,
		Params: RuleParams{
			Threshold:  0.5,
			MinSamples: 4,
			Step:       5 * time.Second,
			MaxBump:    30 * time.Second,
			Cooldown:   10 * time.Minute,
		},
	}
}

func newTestTuner(t *testing.T) (*Tuner, string) {
	t.Helper()
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "tuning_events.jsonl")
	overlayPath := filepath.Join(dir, "tuning_overlay.json")

	tracker, err := NewTracker(eventsPath)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	tuner, err := NewTuner(tracker, overlayPath, testTunerConfig())
	if err != nil {
		t.Fatalf("NewTuner: %v", err)
	}
	return tuner, overlayPath
}

// TestTunerEvaluateStepsUpAndPersists: enough recorded timeouts for a tool
// pushes its overlay bump up, and the change survives being reloaded from
// disk (a fresh LoadOverlay call).
func TestTunerEvaluateStepsUpAndPersists(t *testing.T) {
	tuner, overlayPath := newTestTuner(t)
	for i := 0; i < 4; i++ {
		tuner.Record("web_search", true)
	}

	if err := tuner.Evaluate(); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if got := tuner.EffectiveTimeout("web_search", 20*time.Second); got != 25*time.Second {
		t.Fatalf("effective timeout = %v, want 25s (20s base + one 5s step)", got)
	}

	reloaded, err := LoadOverlay(overlayPath)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	if reloaded.Tools["web_search"].Bump() != 5*time.Second {
		t.Fatalf("persisted bump = %v, want 5s", reloaded.Tools["web_search"].Bump())
	}
}

// TestTunerEvaluateLeavesUntunedToolsAlone: a tool with no recorded events
// gets no bump and no overlay entry.
func TestTunerEvaluateLeavesUntunedToolsAlone(t *testing.T) {
	tuner, _ := newTestTuner(t)
	for i := 0; i < 4; i++ {
		tuner.Record("web_search", true)
	}
	if err := tuner.Evaluate(); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := tuner.EffectiveTimeout("web_code", 15*time.Second); got != 15*time.Second {
		t.Fatalf("web_code effective timeout = %v, want unchanged 15s", got)
	}
}

// TestTunerResetClearsOverlayButKeepsEvents: Reset must revert every tool's
// effective timeout to its base value without discarding the persisted
// event history (it is an append-only audit log).
func TestTunerResetClearsOverlayButKeepsEvents(t *testing.T) {
	tuner, overlayPath := newTestTuner(t)
	for i := 0; i < 4; i++ {
		tuner.Record("web_search", true)
	}
	if err := tuner.Evaluate(); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := tuner.EffectiveTimeout("web_search", 20*time.Second); got == 20*time.Second {
		t.Fatalf("precondition failed: tuner never stepped up")
	}

	if err := tuner.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if got := tuner.EffectiveTimeout("web_search", 20*time.Second); got != 20*time.Second {
		t.Fatalf("effective timeout after Reset = %v, want base 20s", got)
	}
	reloaded, err := LoadOverlay(overlayPath)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	if len(reloaded.Tools) != 0 {
		t.Fatalf("overlay after Reset = %+v, want empty", reloaded.Tools)
	}

	rate, n := tuner.tracker.Stats("web_search", time.Hour)
	if n < 4 {
		t.Fatalf("Reset discarded event history: n=%d, want at least 4", n)
	}
	_ = rate
}

// TestTunerResetIsNoOpWhenNothingTuned: resetting a fresh install must not
// error.
func TestTunerResetIsNoOpWhenNothingTuned(t *testing.T) {
	tuner, _ := newTestTuner(t)
	if err := tuner.Reset(); err != nil {
		t.Fatalf("Reset on a fresh tuner returned an error: %v", err)
	}
}

// TestTunerStatsRecentEventsAndOverlaySnapshot covers the CLI status
// command's read-only passthroughs.
func TestTunerStatsRecentEventsAndOverlaySnapshot(t *testing.T) {
	tuner, _ := newTestTuner(t)
	tuner.Record("web_search", true)
	tuner.Record("web_search", false)

	rate, n := tuner.Stats("web_search", time.Hour)
	if n != 2 {
		t.Fatalf("Stats sample count = %d, want 2", n)
	}
	if rate != 0.5 {
		t.Fatalf("Stats failure rate = %v, want 0.5", rate)
	}

	recent := tuner.RecentEvents(1)
	if len(recent) != 1 {
		t.Fatalf("RecentEvents(1) returned %d events, want 1", len(recent))
	}

	if err := tuner.Evaluate(); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	snap := tuner.OverlaySnapshot()
	if _, ok := snap["web_search"]; !ok {
		// Two samples is below testTunerConfig's MinSamples (4), so no tune
		// is expected -- the point here is only that the snapshot call
		// itself works and reflects the (empty) overlay accurately.
		if len(snap) != 0 {
			t.Fatalf("unexpected overlay snapshot with too few samples to tune: %+v", snap)
		}
	}
}

// TestDefaultTunerConfig pins the fixed rule parameters this narrow tuner
// uses beyond the two operator-declared bounds it actually exposes.
func TestDefaultTunerConfig(t *testing.T) {
	cfg := DefaultTunerConfig(30*time.Second, 5*time.Second)
	if cfg.Params.MaxBump != 30*time.Second || cfg.Params.Step != 5*time.Second {
		t.Fatalf("DefaultTunerConfig did not carry through the operator bounds: %+v", cfg.Params)
	}
	if len(cfg.Tools) != len(Tools) {
		t.Fatalf("DefaultTunerConfig.Tools = %v, want %v", cfg.Tools, Tools)
	}
	if cfg.Window != DefaultWindow || cfg.Params.Threshold != DefaultThreshold ||
		cfg.Params.MinSamples != DefaultMinSamples || cfg.Params.Cooldown != DefaultCooldown {
		t.Fatalf("DefaultTunerConfig did not use this package's fixed defaults: %+v", cfg)
	}
}

// stopBlockWindow is how long the test holds the tick's evaluation open
// before releasing it. A stop that waits for the evaluator cannot return
// while the evaluation is held, so the window only has to be long enough
// for a stop that returns at once to be noticed -- and short enough that
// the violation is reported promptly.
const stopBlockWindow = 500 * time.Millisecond

// TestTunerStartEvaluatesOnATickAndStopBlocksUntilExited proves the
// background loop actually runs Evaluate and that its stop func is
// synchronous, following the "restartable objects keep per-run channels off
// the struct" rule: Start must be safely callable, and its stop must not
// return before the goroutine has actually exited.
//
// The tick's evaluation is held open at the tuner's clock hook -- the
// first thing Evaluate does once it holds the tuner's lock -- so the
// evaluator is provably still running when stop is called, and the
// ordering of "the evaluation finished" and "stop returned" is decided by
// the events themselves rather than by how fast the machine is.
//
// The waits are generous on purpose: the overlay write fsyncs, and fsync
// latency on a loaded disk is seconds. A deadline shorter than the disk
// round trip measures the disk and reports a slow disk as a stuck tuner --
// the same defect #447 found in internal/agent.
func TestTunerStartEvaluatesOnATickAndStopBlocksUntilExited(t *testing.T) {
	tuner, overlayPath := newTestTuner(t)
	for i := 0; i < 4; i++ {
		tuner.Record("web_search", true)
	}

	evaluating := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseEvaluation := func() { releaseOnce.Do(func() { close(release) }) }
	var enteredOnce sync.Once
	tuner.now = func() time.Time {
		enteredOnce.Do(func() { close(evaluating) })
		<-release
		return time.Now()
	}

	stop := tuner.Start(20 * time.Millisecond)
	t.Cleanup(func() {
		releaseEvaluation()
		stop()
	})

	select {
	case <-evaluating:
	case <-time.After(60 * time.Second):
		t.Fatal("background Start loop never evaluated")
	}

	// The evaluation is still held, so a stop that waits for the evaluator
	// cannot have returned; one that does not wait returns at once, with
	// the evaluator still running. The evaluator holds the tuner's lock
	// for the whole evaluation, so a lock that is still held when the
	// stop returns is what tells the two apart.
	returnedEarly := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		stop()
		if tuner.mu.TryLock() {
			tuner.mu.Unlock()
			close(stopped)
			return
		}
		close(returnedEarly)
	}()

	go func() {
		time.Sleep(stopBlockWindow)
		releaseEvaluation()
	}()

	select {
	case <-returnedEarly:
		t.Fatal("stop returned while the evaluator was still running")
	case <-stopped:
	case <-time.After(60 * time.Second):
		t.Fatal("stop never returned after the evaluator exited")
	}

	ov, err := LoadOverlay(overlayPath)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	if len(ov.Tools) == 0 {
		t.Fatal("background Start loop never wrote an overlay adjustment")
	}
}
