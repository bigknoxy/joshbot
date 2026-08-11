package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// `onboard --config /elsewhere/config.json` used to write the new config where
// asked while still inspecting, backing up and rewriting the real ~/.joshbot:
// runOnboard captured homeDir from config.DefaultHome before anything applied
// the flag. Pointing --config at a scratch file to try joshbot out therefore
// disturbed the live install (issue #97).
func TestRunOnboard_ConfigFlagAnchorsTheHome(t *testing.T) {
	realHome := withTempHome(t)

	// A live install the run must not touch.
	if err := os.MkdirAll(filepath.Join(realHome, "workspace"), 0o700); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	const sentinel = `{"do":"not touch"}`
	realConfig := filepath.Join(realHome, "config.json")
	if err := os.WriteFile(realConfig, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	altHome := t.TempDir()
	altConfig := filepath.Join(altHome, "config.json")

	c := onboardContext(t,
		map[string]string{"config": altConfig, "provider": "ollama"},
		map[string]bool{"force": true})
	if err := runOnboard(c); err != nil {
		t.Fatalf("runOnboard: %v", err)
	}

	if _, err := os.Stat(altConfig); err != nil {
		t.Errorf("--config file was not written: %v", err)
	}

	got, err := os.ReadFile(realConfig)
	if err != nil {
		t.Fatalf("the real config was removed: %v", err)
	}
	if string(got) != sentinel {
		t.Errorf("the real config was rewritten: %q", got)
	}

	// backupExisting renames the home aside, so a stray backup is the tell
	// that onboarding operated on the wrong directory even if it later
	// recreated what it moved.
	entries, err := os.ReadDir(filepath.Dir(realHome))
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	base := filepath.Base(realHome)
	for _, e := range entries {
		if e.Name() != base && strings.HasPrefix(e.Name(), base) {
			t.Errorf("onboarding created %s beside the real home; it backed up the wrong install", e.Name())
		}
	}

	// And the anchoring must be complete: everything derived from the home
	// follows the flag, not just the config file.
	if config.DefaultHome != altHome {
		t.Errorf("DefaultHome = %q, want %q", config.DefaultHome, altHome)
	}
	if want := filepath.Join(altHome, "workspace"); config.DefaultWorkspace != want {
		t.Errorf("DefaultWorkspace = %q, want %q", config.DefaultWorkspace, want)
	}
}
