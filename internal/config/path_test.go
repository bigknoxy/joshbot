package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withHome isolates the package globals these tests move around.
func withHome(t *testing.T) {
	t.Helper()
	oldHome, oldWs, oldPath := DefaultHome, DefaultWorkspace, activeConfigPath
	t.Cleanup(func() {
		DefaultHome, DefaultWorkspace, activeConfigPath = oldHome, oldWs, oldPath
	})
}

// --config names a FILE. Using only its directory and loading config.json from
// there means a user testing a change sees results from a completely different
// file and draws the wrong conclusion.
func TestLoadFrom_ReadsTheNamedFile(t *testing.T) {
	withHome(t)
	dir := t.TempDir()

	// A decoy at the conventional name. If the base name were discarded this
	// is what would be loaded.
	decoy := `{"providers":{"decoy":{"api_key":"k","enabled":true}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(decoy), 0o600); err != nil {
		t.Fatal(err)
	}
	wanted := `{"providers":{"chosen":{"api_key":"k","enabled":true}}}`
	target := filepath.Join(dir, "staging.json")
	if err := os.WriteFile(target, []byte(wanted), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(target)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if _, ok := cfg.Providers["chosen"]; !ok {
		t.Errorf("did not load the named file; providers = %v", providerNames(cfg))
	}
	if _, ok := cfg.Providers["decoy"]; ok {
		t.Error("loaded config.json from the directory instead of the named file")
	}
}

// Silently falling back to defaults is the worst outcome: the user believes
// their file was read.
func TestLoadFrom_MissingFileIsAnError(t *testing.T) {
	withHome(t)
	missing := filepath.Join(t.TempDir(), "nope.json")

	_, err := LoadFrom(missing)
	if err == nil {
		t.Fatal("a nonexistent config path returned defaults instead of an error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error should name the path that failed; got %v", err)
	}
}

// Reading one file and writing another is silent loss of intent: `configure`
// would appear to work while updating a file nobody asked about.
func TestSave_WritesBackToTheLoadedFile(t *testing.T) {
	withHome(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "staging.json")
	if err := os.WriteFile(target, []byte(`{"log_level":"debug"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(target)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	cfg.LogLevel = "warn"
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the loaded file was not written back: %v", err)
	}
	if !strings.Contains(string(data), "warn") {
		t.Errorf("the change did not reach the loaded file: %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		t.Error("Save created config.json alongside the file it was asked to use")
	}
}

// DefaultWorkspace is computed from DefaultHome at package init. Moving the
// home without recomputing it leaves the workspace pointing at the old
// location, which is how a "use this config" flag ends up half-applied.
func TestSetHome_UpdatesDerivedPaths(t *testing.T) {
	withHome(t)
	dir := t.TempDir()

	SetHome(dir)

	if DefaultHome != dir {
		t.Errorf("DefaultHome = %q, want %q", DefaultHome, dir)
	}
	if want := filepath.Join(dir, "workspace"); DefaultWorkspace != want {
		t.Errorf("DefaultWorkspace = %q, want %q — derived globals went stale", DefaultWorkspace, want)
	}
	cfg := &Config{}
	for name, got := range map[string]string{
		"sessions": cfg.SessionsDir(),
		"media":    cfg.MediaDir(),
		"cron":     cfg.CronDir(),
	} {
		if want := filepath.Join(dir, name); got != want {
			t.Errorf("%s dir = %q, want %q", name, got, want)
		}
	}
	if want := filepath.Join(dir, "config.json"); ConfigPath() != want {
		t.Errorf("ConfigPath() = %q, want %q", ConfigPath(), want)
	}
}

// Loading a file anchors everything else to its directory, so sessions and the
// skills trust store do not end up split across two homes.
func TestLoadFrom_AnchorsHomeToTheFilesDirectory(t *testing.T) {
	withHome(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "alt.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadFrom(target); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if DefaultHome != dir {
		t.Errorf("DefaultHome = %q, want %q; derived paths would point at the wrong home", DefaultHome, dir)
	}
	if ConfigPath() != target {
		t.Errorf("ConfigPath() = %q, want %q", ConfigPath(), target)
	}
}

func providerNames(cfg *Config) []string {
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	return names
}
