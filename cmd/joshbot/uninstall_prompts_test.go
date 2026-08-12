package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Interactive uninstall is the most destructive prompt joshbot has, and it is
// three prompts deep: service, binary, config. Each one is a bare fmt.Scanln
// compared against a string, so an inverted comparison or a mis-ordered read
// destroys an install that the user declined to remove — and the only signal
// is what is missing afterwards. --force is covered; the answered paths were
// not.

// runUninstallAnswering drives an interactive `joshbot uninstall` over stdin.
func runUninstallAnswering(t *testing.T, answers string, args ...string) int {
	t.Helper()
	withStdinInput(t, answers)
	_, code := runCLI(t, append([]string{"uninstall"}, args...)...)
	return code
}

// exists reports whether a path is present.
func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// The binary prompt is (y/N): anything other than y declines. Declining has to
// leave the install completely untouched — a user who typed n and lost their
// binary has no way back except reinstalling, and no way to know why.
func TestUninstallDeclinedAtTheBinaryPromptRemovesNothing(t *testing.T) {
	svc := &stubManager{}
	bin, home := uninstallEnv(t, svc)

	if code := runUninstallAnswering(t, "n\n"); code != 0 {
		t.Fatalf("exit code = %d, want 0; declining is not an error", code)
	}
	if !exists(t, bin) {
		t.Error("answering n at the binary prompt removed the binary anyway")
	}
	if !exists(t, filepath.Join(home, "config.json")) {
		t.Error("answering n at the binary prompt removed the config")
	}
}

// Enter at (y/N) is the default, and the default must be the safe one. This is
// the answer a user gives when they are not sure — it cannot be the one that
// deletes their install.
func TestUninstallDefaultAtTheBinaryPromptKeepsEverything(t *testing.T) {
	svc := &stubManager{}
	bin, _ := uninstallEnv(t, svc)

	if code := runUninstallAnswering(t, "\n"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !exists(t, bin) {
		t.Error("pressing Enter at a (y/N) prompt removed the binary")
	}
}

// The two prompts are independent: yes to the binary, no to the config. This
// is the ordinary "reinstall joshbot but keep my keys, memory and sessions"
// path, and getting it wrong destroys data the user explicitly kept.
func TestUninstallRemovesTheBinaryButKeepsADeclinedConfig(t *testing.T) {
	svc := &stubManager{}
	bin, home := uninstallEnv(t, svc)

	if code := runUninstallAnswering(t, "y\nn\n"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if exists(t, bin) {
		t.Error("answering y at the binary prompt left the binary in place")
	}
	if !exists(t, filepath.Join(home, "config.json")) {
		t.Error("the config was removed after the user declined the config prompt")
	}
}

// Both answered yes removes both. Without this the previous test passes for a
// uninstall that never removes the config at all.
func TestUninstallRemovesTheConfigWhenConfirmed(t *testing.T) {
	svc := &stubManager{}
	bin, home := uninstallEnv(t, svc)

	if code := runUninstallAnswering(t, "y\ny\n"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if exists(t, bin) {
		t.Error("the binary survived a confirmed uninstall")
	}
	if exists(t, home) {
		t.Error("the config directory survived a confirmed config removal")
	}
}

// The service prompt is the one that defaults to yes — (Y/n) — because the
// binary it points at is about to disappear. Answering n has to leave the
// service alone while the rest of the uninstall proceeds, and the answer must
// not be consumed by the binary prompt behind it.
func TestUninstallDeclinedServiceLeavesItInstalled(t *testing.T) {
	svc := &stubManager{installed: true}
	bin, _ := uninstallEnv(t, svc)

	if code := runUninstallAnswering(t, "n\ny\nn\n"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if svc.uninstalled {
		t.Error("the service was uninstalled after the user declined")
	}
	if exists(t, bin) {
		t.Error("declining the service prompt also skipped the binary removal")
	}
}

// Enter at (Y/n) takes the default, which here is yes: leaving a service
// pointed at a deleted binary behind is a daemon that fails to start forever,
// with nothing on screen to explain it.
func TestUninstallDefaultAtTheServicePromptUninstallsIt(t *testing.T) {
	svc := &stubManager{installed: true}
	_, _ = uninstallEnv(t, svc)

	if code := runUninstallAnswering(t, "\ny\nn\n"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !svc.uninstalled {
		t.Error("pressing Enter at a (Y/n) service prompt left the service installed")
	}
}
