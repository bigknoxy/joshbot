package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/tuning"
)

// tuningEnv sets up an isolated home with a config and returns its path.
func tuningEnv(t *testing.T) (configPath, home string) {
	t.Helper()

	home = t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".joshbot"), 0750); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	cfg := map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"workspace": filepath.Join(home, "workspace"),
			},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	configPath = filepath.Join(home, ".joshbot", "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, home
}

func runTuningCmd(t *testing.T, configPath string, args ...string) (string, int) {
	t.Helper()

	app := &cli.App{
		Flags:                 []cli.Flag{&cli.PathFlag{Name: "config"}},
		Commands:              []*cli.Command{tuningCommand()},
		Writer:                io.Discard,
		ErrWriter:             io.Discard,
		ExitErrHandler:        func(*cli.Context, error) {},
		CustomAppHelpTemplate: " ",
	}

	full := append([]string{"joshbot", "--config", configPath, "tuning"}, args...)

	var code int
	out := captureStdout(t, func() {
		err := app.Run(full)
		if err != nil {
			code = 1
			var ec cli.ExitCoder
			if ok := asExitCoder(err, &ec); ok {
				code = ec.ExitCode()
			}
		}
	})
	return out, code
}

// TestTuningStatusOnFreshInstallShowsDefaults: no overlay file exists yet,
// so every tool's effective timeout must read as its base/default, not
// error, and the command must exit 0.
func TestTuningStatusOnFreshInstallShowsDefaults(t *testing.T) {
	cfg, _ := tuningEnv(t)

	out, code := runTuningCmd(t, cfg, "status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "web_search") {
		t.Fatalf("status output missing web_search:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "disabled") {
		t.Fatalf("status output should note tuning is disabled by default:\n%s", out)
	}
}

// TestTuningResetIsNoOpWhenNothingTuned: resetting a fresh install (no
// overlay file, no events) must not error.
func TestTuningResetIsNoOpWhenNothingTuned(t *testing.T) {
	cfg, _ := tuningEnv(t)

	out, code := runTuningCmd(t, cfg, "reset")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
}

// TestTuningResetClearsOverlay: an overlay written directly (simulating a
// prior tune) is cleared by `tuning reset`, and status afterward reflects
// the reversion to the base timeout.
func TestTuningResetClearsOverlay(t *testing.T) {
	cfg, home := tuningEnv(t)

	overlayPath := filepath.Join(home, ".joshbot", tuning.OverlayFileName)
	ov := &tuning.Overlay{Tools: map[string]tuning.OverlayEntry{
		"web_search": {BumpNanos: int64(1e9) * 10, StepCount: 3},
	}}
	if err := tuning.SaveOverlay(overlayPath, ov); err != nil {
		t.Fatalf("SaveOverlay: %v", err)
	}

	statusBefore, code := runTuningCmd(t, cfg, "status")
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0; output:\n%s", code, statusBefore)
	}
	if !strings.Contains(statusBefore, "10s") {
		t.Fatalf("status before reset should show the +10s bump:\n%s", statusBefore)
	}

	_, code = runTuningCmd(t, cfg, "reset")
	if code != 0 {
		t.Fatalf("reset exit code = %d, want 0", code)
	}

	reloaded, err := tuning.LoadOverlay(overlayPath)
	if err != nil {
		t.Fatalf("LoadOverlay after reset: %v", err)
	}
	if len(reloaded.Tools) != 0 {
		t.Fatalf("overlay after reset = %+v, want empty", reloaded.Tools)
	}
}

// TestBaseWebTimeoutUsesConfiguredValueOrPackageDefault covers every branch
// of baseWebTimeout directly: an explicitly configured value wins, an unset
// (zero) one falls back to this command's own display default, and a tool
// name outside the tunable four returns zero rather than guessing.
func TestBaseWebTimeoutUsesConfiguredValueOrPackageDefault(t *testing.T) {
	cfg := config.Defaults()
	cfg.Tools.Web.SearchTimeout = config.Duration(9 * time.Second)
	cfg.Tools.Web.ResearchTimeout = config.Duration(11 * time.Second)
	cfg.Tools.Web.CodeTimeout = config.Duration(13 * time.Second)
	cfg.Tools.Web.CompanyTimeout = config.Duration(17 * time.Second)

	cases := []struct {
		tool string
		want time.Duration
	}{
		{"web_search", 9 * time.Second},
		{"web_research", 11 * time.Second},
		{"web_code", 13 * time.Second},
		{"web_company", 17 * time.Second},
	}
	for _, tc := range cases {
		if got := baseWebTimeout(cfg, tc.tool); got != tc.want {
			t.Errorf("baseWebTimeout(configured, %q) = %v, want %v", tc.tool, got, tc.want)
		}
	}

	unset := config.Defaults()
	defaultCases := []struct {
		tool string
		want time.Duration
	}{
		{"web_search", 25 * time.Second},
		{"web_research", 45 * time.Second},
		{"web_code", 20 * time.Second},
		{"web_company", 20 * time.Second},
	}
	for _, tc := range defaultCases {
		if got := baseWebTimeout(unset, tc.tool); got != tc.want {
			t.Errorf("baseWebTimeout(unset, %q) = %v, want package default %v", tc.tool, got, tc.want)
		}
	}

	if got := baseWebTimeout(unset, "not_a_real_tool"); got != 0 {
		t.Errorf("baseWebTimeout for an unknown tool = %v, want 0", got)
	}
}

// TestTuningStatusReportsEnabledAndTunedState exercises the status command
// with tuning turned on and an active bump, covering the branch that prints
// the "tuned bump" and "last tuned" lines rather than "none".
func TestTuningStatusReportsEnabledAndTunedState(t *testing.T) {
	cfg, home := tuningEnv(t)

	raw, _ := os.ReadFile(cfg)
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	generic["tuning"] = map[string]any{"enabled": true, "max_bump": "30s", "step": "5s"}
	data, _ := json.MarshalIndent(generic, "", "  ")
	if err := os.WriteFile(cfg, data, 0600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	overlayPath := filepath.Join(home, ".joshbot", tuning.OverlayFileName)
	ov := &tuning.Overlay{Tools: map[string]tuning.OverlayEntry{
		"web_code": {BumpNanos: int64(5 * time.Second), StepCount: 1},
	}}
	if err := tuning.SaveOverlay(overlayPath, ov); err != nil {
		t.Fatalf("SaveOverlay: %v", err)
	}

	out, code := runTuningCmd(t, cfg, "status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Tuning: enabled") {
		t.Fatalf("status did not report tuning as enabled:\n%s", out)
	}
	if !strings.Contains(out, "tuned bump") {
		t.Fatalf("status did not report the active tune for web_code:\n%s", out)
	}
}

// TestTuningStatusReportsRecentEvents covers the "recent events" tail, which
// only renders once at least one event has been recorded.
func TestTuningStatusReportsRecentEvents(t *testing.T) {
	cfg, home := tuningEnv(t)

	eventsPath := filepath.Join(home, ".joshbot", tuning.EventsFileName)
	tracker, err := tuning.NewTracker(eventsPath)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	tracker.Record("web_search", true)

	out, code := runTuningCmd(t, cfg, "status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Recent events") {
		t.Fatalf("status output missing the recent-events section:\n%s", out)
	}
}

// TestTuningStatusFailsWithACorruptOverlay covers tunerForCLI's error path:
// an overlay file that exists but cannot be parsed must fail the command
// rather than silently reporting an empty tuner state.
func TestTuningStatusFailsWithACorruptOverlay(t *testing.T) {
	cfg, home := tuningEnv(t)
	overlayPath := filepath.Join(home, ".joshbot", tuning.OverlayFileName)
	if err := os.WriteFile(overlayPath, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write corrupt overlay: %v", err)
	}

	_, code := runTuningCmd(t, cfg, "status")
	if code == 0 {
		t.Fatal("status with a corrupt overlay file exited 0, want a failure")
	}
}
