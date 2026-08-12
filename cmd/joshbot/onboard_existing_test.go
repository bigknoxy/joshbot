package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// Re-running `joshbot onboard` over an existing install is the one command that
// can move a user's whole ~/.joshbot aside. Which branch it takes is decided by
// --force, --keep-data, or a single keystroke, and the only visible difference
// afterwards is whether MEMORY.md still exists. None of these branches had
// coverage.

// seedInstall writes an existing install with a sentinel memory file and
// returns the home directory. The sentinel is the assertion: onboard never
// reports what it did to the workspace, so its survival is the only evidence.
func seedInstall(t *testing.T) string {
	t.Helper()

	home := filepath.Join(t.TempDir(), ".joshbot")
	setHome(t, home)

	memDir := filepath.Join(home, "workspace", "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("remember-me"), 0600); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	existing := config.Defaults()
	existing.Providers = map[string]config.ProviderConfig{
		"nvidia": {APIKey: "nvapi-keepme", Enabled: true},
	}
	existing.ProviderDefaults.Default = "nvidia"
	existing.Agents.Defaults.Model = "z-ai/glm-5.2"
	if err := config.Save(existing); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return home
}

// memorySurvived reports whether the seeded sentinel is still in place.
func memorySurvived(t *testing.T, home string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "workspace", "memory", "MEMORY.md"))
	return err == nil && string(b) == "remember-me"
}

// backupCount counts the .joshbot.backup.* directories next to home.
func backupCount(t *testing.T, home string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(home))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".joshbot.backup.") {
			n++
		}
	}
	return n
}

// runOnboardWith drives one onboard run over stdin and returns its error.
func runOnboardWith(t *testing.T, home, stdin string, args ...string) error {
	t.Helper()
	withStdinInput(t, stdin)
	stubTokenValidator(t, func(string) error { return nil })
	full := append([]string{
		"joshbot", "--config", filepath.Join(home, "config.json"), "onboard",
	}, args...)
	return onboardApp().Run(full)
}

// The prompt sequence after the existing-install decision: 1 nvidia; Enter keep
// key; 2 personality; Enter keep name; Enter accept model; 2 skip telegram;
// 2 skip service install.
const onboardPrompts = "1\n\n2\n\n\n2\n2\n"

// --keep-data is what an operator reaches for to change a provider without
// losing their assistant's memory. Backing up here would be silent data loss
// from the user's point of view: nothing on screen says the workspace moved,
// and the next run starts with an assistant that has forgotten everything.
func TestOnboardKeepDataLeavesTheWorkspaceWhereItIs(t *testing.T) {
	home := seedInstall(t)

	if err := runOnboardWith(t, home, onboardPrompts, "--keep-data"); err != nil {
		t.Fatalf("onboard --keep-data: %v", err)
	}
	if !memorySurvived(t, home) {
		t.Error("--keep-data destroyed the existing memory")
	}
	if n := backupCount(t, home); n != 0 {
		t.Errorf("--keep-data made %d backup(s); it must not move the install", n)
	}
}

// --force is the opposite contract: back up, then start fresh. A --force that
// quietly reconfigured in place would leave the old workspace mixed with a new
// config, which is exactly what the operator asked to be rid of.
func TestOnboardForceBacksUpTheExistingInstall(t *testing.T) {
	home := seedInstall(t)

	if err := runOnboardWith(t, home, "", "--force", "--provider", "ollama"); err != nil {
		t.Fatalf("onboard --force: %v", err)
	}
	if memorySurvived(t, home) {
		t.Error("--force left the old memory in place instead of starting fresh")
	}
	if n := backupCount(t, home); n != 1 {
		t.Errorf("backups = %d, want exactly 1; --force must not delete without one", n)
	}
}

// The interactive menu prints "(default: 1)", and Enter must mean what the menu
// says it means. Anything else is the worst possible outcome of this command:
// the operator accepts a default labelled "keep existing data" and their
// workspace is moved out from under them.
func TestOnboardInteractiveDefaultKeepsExistingData(t *testing.T) {
	home := seedInstall(t)

	if err := runOnboardWith(t, home, "\n"+onboardPrompts); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	if !memorySurvived(t, home) {
		t.Error("pressing Enter at a menu that says (default: 1) destroyed the memory")
	}
	if n := backupCount(t, home); n != 0 {
		t.Errorf("the default choice made %d backup(s), want 0", n)
	}
}

// Choice 1 spelled out has to agree with the default, or the menu means two
// different things depending on how it is answered.
func TestOnboardInteractiveChoiceOneKeepsExistingData(t *testing.T) {
	home := seedInstall(t)

	if err := runOnboardWith(t, home, "1\n"+onboardPrompts); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	if !memorySurvived(t, home) {
		t.Error("choice 1 destroyed the memory it promised to keep")
	}
	if n := backupCount(t, home); n != 0 {
		t.Errorf("choice 1 made %d backup(s), want 0", n)
	}
}

// Choice 2 is the destructive one, and the menu promises a backup with it.
// Deleting without one is unrecoverable; the backup is the whole reason the
// option is safe to offer.
func TestOnboardInteractiveChoiceTwoBacksUpAndStartsFresh(t *testing.T) {
	home := seedInstall(t)

	if err := runOnboardWith(t, home, "2\n"+onboardPrompts, "--provider", "ollama"); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	if memorySurvived(t, home) {
		t.Error("choice 2 kept the old workspace instead of starting fresh")
	}
	if n := backupCount(t, home); n != 1 {
		t.Errorf("backups = %d, want exactly 1; choice 2 promises one", n)
	}
}
