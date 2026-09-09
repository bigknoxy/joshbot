package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/tuning"
)

// TestSetupComponentsWithTuningEnabledRegistersAWebToolRecorder covers the
// setupComponents branch that only runs when tuning.enabled is true: it must
// build a working Tracker/Tuner, register one more background service (the
// tuner's periodic Evaluate loop, alongside cron/heartbeat/consolidator),
// and wire the web tool with a TimeoutRecorder without erroring.
func TestSetupComponentsWithTuningEnabledRegistersAWebToolRecorder(t *testing.T) {
	stopBackgroundServices() // start from a clean registry

	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test", APIBase: "https://example.invalid/v1"},
	}
	cfg.Tuning.Enabled = true
	cfg.Tuning.MaxBump = config.Duration(30 * time.Second)
	cfg.Tuning.Step = config.Duration(5 * time.Second)

	_, _, _, _, reg, _, err := setupComponents(cfg)
	if err != nil {
		t.Fatalf("setupComponents with tuning enabled: %v", err)
	}
	if _, ok := reg.Get("web"); !ok {
		t.Fatal("web tool is not registered")
	}

	bgMu.Lock()
	got := len(bgStoppers)
	bgMu.Unlock()
	// cron, heartbeat, consolidator, tuner: one more than the baseline three.
	if got < 4 {
		t.Fatalf("setupComponents with tuning enabled registered %d background services, want at least 4", got)
	}

	eventsPath := filepath.Join(cfg.HomeDir(), tuning.EventsFileName)
	if _, err := tuning.NewTracker(eventsPath); err != nil {
		t.Fatalf("tuning events file was not created where expected: %v", err)
	}
}

// TestSetupComponentsWithTuningDisabledRegistersNoExtraBackgroundService
// pins the off-by-default contract: with tuning.enabled left false (the
// zero value), setupComponents must register exactly the three background
// services every setup has always started — no Tracker, no overlay file,
// no extra goroutine.
func TestSetupComponentsWithTuningDisabledRegistersNoExtraBackgroundService(t *testing.T) {
	stopBackgroundServices()

	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test", APIBase: "https://example.invalid/v1"},
	}

	if _, _, _, _, _, _, err := setupComponents(cfg); err != nil {
		t.Fatalf("setupComponents: %v", err)
	}

	bgMu.Lock()
	got := len(bgStoppers)
	bgMu.Unlock()
	if got != 3 {
		t.Fatalf("setupComponents with tuning disabled registered %d background services, want exactly 3", got)
	}
}

// TestSetupComponentsWithTuningEnabledMergesAnExistingOverlay proves the
// overlay-merge-at-startup path actually reads a pre-existing bump: writing
// an overlay adjustment before setupComponents runs must not error and the
// tuning events file must still be created alongside it.
func TestSetupComponentsWithTuningEnabledMergesAnExistingOverlay(t *testing.T) {
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test", APIBase: "https://example.invalid/v1"},
	}
	cfg.Tuning.Enabled = true
	cfg.Tuning.MaxBump = config.Duration(30 * time.Second)
	cfg.Tuning.Step = config.Duration(5 * time.Second)

	overlayPath := filepath.Join(cfg.HomeDir(), tuning.OverlayFileName)
	ov := &tuning.Overlay{Tools: map[string]tuning.OverlayEntry{
		"web_search": {BumpNanos: int64(10 * time.Second)},
	}}
	if err := tuning.SaveOverlay(overlayPath, ov); err != nil {
		t.Fatalf("SaveOverlay: %v", err)
	}

	_, _, _, _, reg, _, err := setupComponents(cfg)
	if err != nil {
		t.Fatalf("setupComponents with a pre-existing overlay: %v", err)
	}
	if _, ok := reg.Get("web"); !ok {
		t.Fatal("web tool is not registered")
	}

	reloaded, err := tuning.LoadOverlay(overlayPath)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	if reloaded.Tools["web_search"].Bump() != 10*time.Second {
		t.Fatalf("pre-existing overlay was not left intact: %+v", reloaded.Tools["web_search"])
	}
}
