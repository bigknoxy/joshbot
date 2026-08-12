package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The @reboot fallback edits the user's crontab, which is shared state joshbot
// does not own: getting it wrong either loses whatever else was scheduled there
// or appends a duplicate entry on every onboard. Neither had a test.
//
// No seam is needed — the function shells out through PATH, so a fake crontab
// on PATH exercises the real code path including the LookPath guard.

// fakeCrontab puts a crontab script on PATH that reads from and writes to a
// file in a temp dir, so `crontab -l` and `crontab -` behave like the real one.
// existing is what `crontab -l` reports; the returned path holds whatever was
// installed. listStatus is the exit code of `crontab -l`, since the real one
// exits non-zero when no crontab exists.
func fakeCrontab(t *testing.T, existing string, listStatus int) (installed string) {
	t.Helper()

	dir := t.TempDir()
	installed = filepath.Join(dir, "installed.txt")
	current := filepath.Join(dir, "current.txt")
	if err := os.WriteFile(current, []byte(existing), 0600); err != nil {
		t.Fatalf("seed crontab: %v", err)
	}

	script := "#!/bin/sh\n" +
		// PATH is replaced below, so the script cannot rely on inheriting one.
		"PATH=/bin:/usr/bin\n" +
		"if [ \"$1\" = \"-l\" ]; then cat " + current + "; exit " + strconv.Itoa(listStatus) + "; fi\n" +
		"if [ \"$1\" = \"-\" ]; then cat > " + installed + "; exit 0; fi\n" +
		"exit 2\n"
	bin := filepath.Join(dir, "crontab")
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatalf("write fake crontab: %v", err)
	}

	t.Setenv("PATH", dir)
	return installed
}

func readOrEmpty(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// On a machine with no crontab yet, the entry is installed on its own. The
// real `crontab -l` exits non-zero and prints "no crontab for <user>" in that
// case, which must not be mistaken for a read failure — treating it as one
// leaves the fallback permanently uninstallable on exactly the machines that
// need it.
func TestCronStartupInstallsIntoAnEmptyCrontab(t *testing.T) {
	installed := fakeCrontab(t, "no crontab for someone\n", 1)
	bin := filepath.Join(t.TempDir(), "joshbot")
	withExecutable(t, bin)
	t.Setenv("HOME", t.TempDir())

	if err := installCronStartupEntry(); err != nil {
		t.Fatalf("installCronStartupEntry() = %v, want nil", err)
	}

	got := readOrEmpty(t, installed)
	if !strings.Contains(got, "@reboot "+bin+" gateway") {
		t.Errorf("the installed entry does not start joshbot's gateway:\n%s", got)
	}
	if !strings.Contains(got, "gateway.log") {
		t.Errorf("output is not redirected to a log, so boot failures are invisible:\n%s", got)
	}
	if strings.Contains(got, "no crontab for") {
		t.Errorf("crontab's own error text was written back as a crontab line:\n%s", got)
	}
}

// An existing crontab must survive. Overwriting it silently destroys unrelated
// scheduled work, which nothing here would ever report.
func TestCronStartupPreservesAnExistingCrontab(t *testing.T) {
	installed := fakeCrontab(t, "0 3 * * * /usr/local/bin/backup.sh\n", 0)
	withExecutable(t, filepath.Join(t.TempDir(), "joshbot"))
	t.Setenv("HOME", t.TempDir())

	if err := installCronStartupEntry(); err != nil {
		t.Fatalf("installCronStartupEntry() = %v, want nil", err)
	}

	got := readOrEmpty(t, installed)
	if !strings.Contains(got, "/usr/local/bin/backup.sh") {
		t.Errorf("the user's existing cron entry was destroyed:\n%s", got)
	}
	if !strings.Contains(got, "@reboot") {
		t.Errorf("the joshbot entry was not appended:\n%s", got)
	}
}

// Re-running onboard must not append a second copy. Duplicate @reboot lines
// start two gateways, which then compete for the same Telegram long poll.
func TestCronStartupIsIdempotent(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "joshbot")
	home := t.TempDir()
	logPath := filepath.Join(home, ".joshbot", "logs", "gateway.log")
	entry := "@reboot " + bin + " gateway >> " + logPath + " 2>&1"

	installed := fakeCrontab(t, entry+"\n", 0)
	withExecutable(t, bin)
	t.Setenv("HOME", home)

	if err := installCronStartupEntry(); err != nil {
		t.Fatalf("installCronStartupEntry() = %v, want nil", err)
	}

	if got := readOrEmpty(t, installed); got != "" {
		t.Errorf("crontab was rewritten even though the entry was already present:\n%s", got)
	}
}

// No crontab binary is a clear error, not a silent success: reporting the
// fallback as installed when nothing was scheduled is the worst outcome here.
func TestCronStartupReportsAMissingCrontab(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	withExecutable(t, filepath.Join(t.TempDir(), "joshbot"))

	err := installCronStartupEntry()
	if err == nil {
		t.Fatal("installCronStartupEntry() returned nil with no crontab on PATH")
	}
	// The message must say the binary is absent, not report a failed write:
	// falling through to `crontab -` produces "failed to install cron entry",
	// which sends the operator looking at permissions for a machine that simply
	// has no cron.
	if !strings.Contains(err.Error(), "crontab not found") {
		t.Errorf("error does not report a missing binary, so the cause is misattributed: %v", err)
	}
}

// The fallback is a Linux-only affordance — launchd covers macOS. On anything
// else it must be a no-op rather than shelling out, or onboard on macOS prompts
// for a mechanism the platform does not use.
func TestCronStartupFallbackIsALinuxOnlyPrompt(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the non-linux bail-out cannot be exercised on linux")
	}
	// PATH is emptied so any attempt to reach crontab would fail loudly.
	t.Setenv("PATH", t.TempDir())

	out := captureStdout(t, func() {
		if err := promptCronStartupFallback(); err != nil {
			t.Errorf("promptCronStartupFallback() = %v, want nil on %s", err, runtime.GOOS)
		}
	})
	if out != "" {
		t.Errorf("the cron fallback prompted on %s:\n%s", runtime.GOOS, out)
	}
}
