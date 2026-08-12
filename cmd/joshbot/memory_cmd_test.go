package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `joshbot memory` is the only operator-visible surface of Dream consolidation:
// Stage 1 rides on the history append and Stage 2 otherwise never runs on its
// own. Without these commands a configured dream_mode is indistinguishable from
// a no-op, which is exactly how the whole subsystem sat unwired (#193).

// dreamEnv writes a config with the given dream_mode and returns its path plus
// the workspace directory.
func dreamEnv(t *testing.T, mode string) (cfgPath, workspace string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".joshbot"), 0o750); err != nil {
		t.Fatal(err)
	}
	workspace = filepath.Join(home, "workspace")
	defaults := map[string]any{"workspace": workspace}
	if mode != "" {
		defaults["dream_mode"] = mode
	}
	data, _ := json.MarshalIndent(map[string]any{
		"agents": map[string]any{"defaults": defaults},
	}, "", "  ")
	cfgPath = filepath.Join(home, ".joshbot", "config.json")
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, workspace
}

// writeDreamRaw seeds the Stage 1 log the way a run of joshbot would.
func writeDreamRaw(t *testing.T, workspace string, contents ...string) {
	t.Helper()
	dir := filepath.Join(workspace, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i, c := range contents {
		line, _ := json.Marshal(map[string]any{
			"id": "r" + string(rune('a'+i)), "type": "thought", "content": c,
			"ts": "2026-08-12T00:00:00Z",
		})
		b.Write(line)
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "dream_raw.log"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryStatusReportsOffWhenDreamModeIsUnset(t *testing.T) {
	cfg, _ := dreamEnv(t, "")

	out, code := runCLI(t, "--config", cfg, "memory", "status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "off") {
		t.Errorf("status did not say Dream is off:\n%s", out)
	}
	// Reporting zero counts instead of "off" reads as "on and idle", which
	// sends the operator looking for a bug that is a config setting.
	if strings.Contains(out, "Raw records") {
		t.Errorf("status printed counts for a disabled subsystem:\n%s", out)
	}
	if !strings.Contains(out, "dream_mode") {
		t.Errorf("status does not name the key that turns it on:\n%s", out)
	}
}

func TestMemoryConsolidateRefusesWhenDreamIsOff(t *testing.T) {
	cfg, _ := dreamEnv(t, "")

	out, code := runCLI(t, "--config", cfg, "memory", "consolidate")
	if code == 0 {
		t.Fatalf("consolidate exited 0 with Dream off; a script cannot tell it did nothing:\n%s", out)
	}
}

func TestMemoryConsolidateTurnsRawRecordsIntoInsightsAndDrainsTheLog(t *testing.T) {
	cfg, ws := dreamEnv(t, "full")
	writeDreamRaw(t, ws,
		"the deploy pipeline runs on github actions",
		"github actions runs the deploy pipeline nightly",
	)

	// Before: the raw records are counted and nothing is consolidated yet.
	out, code := runCLI(t, "--config", cfg, "memory", "status")
	if code != 0 {
		t.Fatalf("status exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "Raw records:  2") {
		t.Errorf("status did not count the seeded raw records:\n%s", out)
	}
	if !strings.Contains(out, "Insights:     0") {
		t.Errorf("status reported insights before consolidation:\n%s", out)
	}

	out, code = runCLI(t, "--config", cfg, "memory", "consolidate")
	if code != 0 {
		t.Fatalf("consolidate exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "github actions") {
		t.Errorf("consolidate printed no insight text:\n%s", out)
	}

	// After: the raw log is drained and the insights are readable back from
	// disk by a second process, which is what makes them durable.
	out, code = runCLI(t, "--config", cfg, "memory", "status")
	if code != 0 {
		t.Fatalf("status exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "Raw records:  0") {
		t.Errorf("the raw log was not drained:\n%s", out)
	}
	if strings.Contains(out, "Insights:     0") {
		t.Errorf("the consolidated insights did not survive the process:\n%s", out)
	}
	if !strings.Contains(out, "github actions") {
		t.Errorf("status does not list the stored insights:\n%s", out)
	}
}

// Consolidate is a no-op below DreamFull, so "record" mode used to print
// "Consolidated 2 raw record(s) into 0 insight(s)" and exit 0 forever while
// dream_raw.log grew without bound — the agent -m anti-pattern, success
// reported over work that never happened.
func TestMemoryConsolidateRefusesInRecordMode(t *testing.T) {
	cfg, workspace := dreamEnv(t, "record")
	writeDreamRaw(t, workspace, "the deploy pipeline runs on github actions")

	out, code := runCLI(t, "--config", cfg, "memory", "consolidate")
	if code == 0 {
		t.Fatalf("consolidate exited 0 in record mode:\n%s", out)
	}
	if strings.Contains(out, "Consolidated") {
		t.Errorf("consolidate claimed to have consolidated something:\n%s", out)
	}
}
