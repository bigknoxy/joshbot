//go:build darwin

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestLaunchd points HOME at a scratch dir so Install/Uninstall never touch the
// operator's real ~/Library/LaunchAgents, and gives the job a name that cannot
// collide with a real installed job (so isRunning is deterministically false).
func newTestLaunchd(t *testing.T, cfg Config) (*launchdManager, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	mgr, err := newLaunchd(cfg)
	if err != nil {
		t.Fatalf("newLaunchd() error = %v", err)
	}
	return mgr.(*launchdManager), home
}

// Regression: a field moving, being renamed, or losing its value in the plist
// template. launchd does not report a malformed or incomplete job usefully — the
// gateway simply never starts, or starts in the wrong directory / with the wrong
// HOME so it reads a different config. This pins the whole document byte for byte.
func TestLaunchdInstallWritesGoldenPlist(t *testing.T) {
	mgr, home := newTestLaunchd(t, Config{
		Name:        "joshbot",
		DisplayName: "Joshbot AI Assistant",
		ExecPath:    "/usr/local/bin/joshbot",
		WorkingDir:  "/Users/tester/.joshbot",
	})

	res, err := mgr.Install()
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if !res.Success {
		t.Errorf("Install() Success = false, want true")
	}
	wantLog := filepath.Join(home, ".joshbot", "logs", "joshbot.log")
	if res.LogPath != wantLog {
		t.Errorf("Install() LogPath = %q, want %q", res.LogPath, wantLog)
	}

	got, err := os.ReadFile(mgr.plistPath)
	if err != nil {
		t.Fatalf("reading installed plist: %v", err)
	}

	want := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.joshbot.joshbot</string>
    <key>DisplayName</key>
    <string>Joshbot AI Assistant</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/joshbot</string>
        <string>gateway</string>
    </array>
    <key>WorkingDirectory</key>
    <string>/Users/tester/.joshbot</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s/.joshbot/logs/joshbot.log</string>
    <key>StandardErrorPath</key>
    <string>%s/.joshbot/logs/joshbot.error.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>%s</string>
    </dict>
</dict>
</plist>
`, home, home, home)

	if string(got) != want {
		t.Errorf("installed plist mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Regression: the plist Label drifting away from the identifier used to bootout /
// grep `launchctl list`. If they disagree, uninstall silently leaves a running job
// behind and Status always reports "stopped" for a service that is actually up.
func TestLaunchdLabelMatchesServiceID(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "alt", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	body, err := os.ReadFile(mgr.plistPath)
	if err != nil {
		t.Fatalf("reading plist: %v", err)
	}
	wantLabel := "<string>" + mgr.serviceID() + "</string>"
	if !strings.Contains(string(body), wantLabel) {
		t.Errorf("plist does not contain Label %q; serviceID()=%q", wantLabel, mgr.serviceID())
	}
	if !strings.Contains(mgr.serviceTarget(), mgr.serviceID()) {
		t.Errorf("serviceTarget()=%q does not contain serviceID()=%q", mgr.serviceTarget(), mgr.serviceID())
	}
}

// Regression: `launchctl bootstrap gui <plist>` — a domain *name* where launchctl
// requires a domain *target*. launchctl rejects it, so `joshbot service start`
// fails on every Mac. The UID must be present in both bootstrap and bootout.
func TestLaunchdLaunchctlArgs(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})

	wantDomain := fmt.Sprintf("gui/%d", os.Getuid())
	if mgr.domainTarget() != wantDomain {
		t.Errorf("domainTarget() = %q, want %q", mgr.domainTarget(), wantDomain)
	}

	boot := mgr.bootstrapArgs()
	wantBoot := []string{"bootstrap", wantDomain, mgr.plistPath}
	if len(boot) != len(wantBoot) {
		t.Fatalf("bootstrapArgs() = %q, want %q", boot, wantBoot)
	}
	for i := range wantBoot {
		if boot[i] != wantBoot[i] {
			t.Fatalf("bootstrapArgs() = %q, want %q", boot, wantBoot)
		}
	}

	out := mgr.bootoutArgs()
	wantOut := []string{"bootout", wantDomain + "/dev.joshbot.joshbot"}
	if len(out) != len(wantOut) || out[0] != wantOut[0] || out[1] != wantOut[1] {
		t.Errorf("bootoutArgs() = %q, want %q", out, wantOut)
	}
}

// Regression: Install silently overwriting an existing job's plist. The operator's
// customised or differently-configured service would be replaced with no warning;
// the error text names the path so they can inspect it.
func TestLaunchdInstallRefusesWhenAlreadyInstalled(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("first Install() error = %v", err)
	}

	res, err := mgr.Install()
	if err == nil {
		t.Fatal("second Install() succeeded, want 'already installed' error")
	}
	if !strings.Contains(err.Error(), "already installed") {
		t.Errorf("Install() error = %q, want it to say 'already installed'", err)
	}
	if !strings.Contains(err.Error(), mgr.plistPath) {
		t.Errorf("Install() error = %q, want it to name the plist path %q", err, mgr.plistPath)
	}
	if res.Success {
		t.Error("Install() returned Success=true alongside an error")
	}
}

// Regression: the log directory being created world- or group-readable. The
// gateway's stdout log contains conversation content; ~/.joshbot/logs must be 0700.
func TestLaunchdInstallLogDirIsPrivate(t *testing.T) {
	mgr, home := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(home, ".joshbot", "logs"))
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("log dir mode = %#o, want 0700 (log holds conversation content)", perm)
	}
}

// Regression: Install failing with an unhelpful raw syscall error when
// ~/Library/LaunchAgents cannot be created (here: a plain file occupies the path).
// The operator needs to be told which directory failed, not just "not a directory".
func TestLaunchdInstallReportsUnwritableLaunchAgents(t *testing.T) {
	mgr, home := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if err := os.WriteFile(filepath.Join(home, "Library"), []byte("not a dir"), 0600); err != nil {
		t.Fatalf("seeding blocker file: %v", err)
	}

	if _, err := mgr.Install(); err == nil {
		t.Fatal("Install() succeeded with ~/Library blocked by a regular file")
	} else if !strings.Contains(err.Error(), "LaunchAgents directory") {
		t.Errorf("Install() error = %q, want it to name the LaunchAgents directory", err)
	}
}

// Regression: Install reporting success when the plist could not actually be
// written (here: ~/Library/LaunchAgents is not writable). A Result{Success:true}
// over a failed write tells the operator the daemon is installed when it is not.
func TestLaunchdInstallReportsPlistWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	mgr, home := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0755); err != nil {
		t.Fatalf("seeding LaunchAgents dir: %v", err)
	}
	if err := os.Chmod(agents, 0500); err != nil {
		t.Fatalf("chmod LaunchAgents dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(agents, 0755) })

	res, err := mgr.Install()
	if err == nil {
		t.Fatal("Install() succeeded with the plist path occupied by a directory")
	}
	if !strings.Contains(err.Error(), "failed to write plist") {
		t.Errorf("Install() error = %q, want it to say 'failed to write plist'", err)
	}
	if res.Success {
		t.Error("Install() returned Success=true on a failed plist write")
	}
}

// Regression: Uninstall reporting success (or panicking) when nothing is installed.
// `joshbot service uninstall` on a clean machine must say so, and must not exit 0
// pretending it removed something.
func TestLaunchdUninstallWhenNotInstalled(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	res, err := mgr.Uninstall()
	if err == nil {
		t.Fatal("Uninstall() succeeded with no service installed")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("Uninstall() error = %q, want it to say 'not installed'", err)
	}
	if res.Success {
		t.Error("Uninstall() returned Success=true alongside an error")
	}
}

// Regression: Uninstall leaving the plist on disk. If the file survives, launchd
// reloads the job at next login and the operator's "uninstall" silently did nothing.
func TestLaunchdUninstallRemovesPlist(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if !mgr.IsInstalled() {
		t.Fatal("IsInstalled() = false right after Install()")
	}

	// Hide launchctl so isRunning() takes its error branch and no real job is touched.
	t.Setenv("PATH", "")

	res, err := mgr.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if !res.Success {
		t.Error("Uninstall() Success = false, want true")
	}
	if _, err := os.Stat(mgr.plistPath); !os.IsNotExist(err) {
		t.Errorf("plist still present after Uninstall(): stat err = %v", err)
	}
	if mgr.IsInstalled() {
		t.Error("IsInstalled() = true after Uninstall()")
	}
}

// Regression: Start/Stop/Restart shelling out to launchctl for a service that was
// never installed. launchctl's own error is opaque; joshbot must fail fast with a
// message the operator can act on, and each of the three must do it.
func TestLaunchdLifecycleRefusesWhenNotInstalled(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})

	for name, fn := range map[string]func() error{
		"Start":   mgr.Start,
		"Stop":    mgr.Stop,
		"Restart": mgr.Restart,
	} {
		err := fn()
		if err == nil {
			t.Errorf("%s() succeeded with no service installed", name)
			continue
		}
		if !strings.Contains(err.Error(), "not installed") {
			t.Errorf("%s() error = %q, want it to say 'not installed'", name, err)
		}
	}
}

// Regression: Status claiming a service exists (or erroring) when none is
// installed. `joshbot service status` is scripted against, so the uninstalled case
// must be a clean zero Status with no error.
func TestLaunchdStatusNotInstalled(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	st, err := mgr.Status()
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if st.Installed || st.Running || st.Status != "" {
		t.Errorf("Status() = %+v, want zero Status for an uninstalled service", st)
	}
}

// Regression: an installed-but-not-loaded service reporting the wrong words.
// Status.Status is printed to the operator and must be "stopped" here, not "" or
// "unknown", and Installed must be true even though the job is not running.
func TestLaunchdStatusInstalledNotRunning(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	// launchctl unreachable => isRunning() must degrade to false, not panic or hang.
	t.Setenv("PATH", "")

	st, err := mgr.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !st.Installed {
		t.Error("Status().Installed = false for an installed service")
	}
	if st.Running {
		t.Error("Status().Running = true although launchctl is unreachable")
	}
	if st.Status != "stopped" {
		t.Errorf("Status().Status = %q, want \"stopped\"", st.Status)
	}
}

// Regression: the zero Config producing an empty Label ("dev.joshbot..plist"),
// an empty WorkingDirectory, or an empty ProgramArguments entry. `joshbot service
// install` passes Config{Name:"joshbot"} only, so these defaults are the real
// shipping values and an empty one yields a job launchd will never run.
func TestLaunchdDefaultsFillEveryPlistField(t *testing.T) {
	mgr, home := newTestLaunchd(t, Config{})

	if mgr.config.Name != "joshbot" {
		t.Errorf("default Name = %q, want \"joshbot\"", mgr.config.Name)
	}
	if mgr.config.DisplayName != "Joshbot AI Assistant" {
		t.Errorf("default DisplayName = %q", mgr.config.DisplayName)
	}
	if mgr.config.Description != "Personal AI assistant with Telegram integration" {
		t.Errorf("default Description = %q", mgr.config.Description)
	}
	if want := filepath.Join(home, ".joshbot"); mgr.config.WorkingDir != want {
		t.Errorf("default WorkingDir = %q, want %q", mgr.config.WorkingDir, want)
	}
	if mgr.config.ExecPath == "" || !filepath.IsAbs(mgr.config.ExecPath) {
		t.Errorf("default ExecPath = %q, want an absolute path from os.Executable()", mgr.config.ExecPath)
	}
	if want := filepath.Join(home, "Library", "LaunchAgents", "dev.joshbot.joshbot.plist"); mgr.plistPath != want {
		t.Errorf("plistPath = %q, want %q", mgr.plistPath, want)
	}
	if want := filepath.Join(home, ".joshbot", "logs", "joshbot.error.log"); mgr.errorPath != want {
		t.Errorf("errorPath = %q, want %q", mgr.errorPath, want)
	}
}

// Regression: a non-default Config.Name leaking only into some of the paths, so
// two differently-named services collide on one plist or one launchd Label.
func TestLaunchdNameFlowsIntoPlistPathAndLabel(t *testing.T) {
	mgr, home := newTestLaunchd(t, Config{Name: "staging", ExecPath: "/bin/true", WorkingDir: "/tmp"})

	if want := filepath.Join(home, "Library", "LaunchAgents", "dev.joshbot.staging.plist"); mgr.plistPath != want {
		t.Errorf("plistPath = %q, want %q", mgr.plistPath, want)
	}
	if mgr.serviceID() != "dev.joshbot.staging" {
		t.Errorf("serviceID() = %q, want \"dev.joshbot.staging\"", mgr.serviceID())
	}
	if mgr.Name() != "launchd" {
		t.Errorf("Name() = %q, want \"launchd\" (the manager kind, not the service name)", mgr.Name())
	}
}

// Regression: NewManager on darwin returning a systemd manager, or the
// darwin-only stubs losing their "not available here" errors — cmd/joshbot prints
// these directly when service install is attempted on the wrong platform.
func TestDarwinFactoryReturnsLaunchd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mgr, err := NewManager(Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if mgr.Name() != "launchd" {
		t.Errorf("NewManager().Name() = %q, want \"launchd\" on darwin", mgr.Name())
	}
	if _, ok := mgr.(*launchdManager); !ok {
		t.Errorf("NewManager() returned %T, want *launchdManager", mgr)
	}

	if m, err := newSystemdManager(Config{}); err == nil || m != nil {
		t.Errorf("newSystemdManager() = (%v, %v), want (nil, error) on darwin", m, err)
	} else if !strings.Contains(err.Error(), "systemd") {
		t.Errorf("newSystemdManager() error = %q, want it to mention systemd", err)
	}

	if m, err := newUnsupportedManager(); err == nil || m != nil {
		t.Errorf("newUnsupportedManager() = (%v, %v), want (nil, error)", m, err)
	} else if !strings.Contains(err.Error(), "unsupported platform") {
		t.Errorf("newUnsupportedManager() error = %q, want \"unsupported platform\"", err)
	}
}

// fakeLaunchctl puts a stub `launchctl` first on PATH that records every argv it
// receives to a file. `list` prints listOutput, so isRunning() can be steered.
// exitCode is returned for every non-list subcommand.
func fakeLaunchctl(t *testing.T, listOutput string, exitCode int) (dir string, argvLog func() []string) {
	t.Helper()
	dir = t.TempDir()
	logFile := filepath.Join(dir, "argv.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
if [ "$1" = "list" ]; then printf '%%s\n' %q; exit 0; fi
exit %d
`, logFile, listOutput, exitCode)
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), []byte(script), 0755); err != nil {
		t.Fatalf("writing fake launchctl: %v", err)
	}
	t.Setenv("PATH", dir)
	return dir, func() []string {
		b, err := os.ReadFile(logFile)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// Regression: the exact argv joshbot hands launchctl. `bootstrap gui <plist>`
// (no UID) and `bootout <label>` (no domain) are both rejected by launchctl, so
// start/stop fail on every Mac while joshbot's own error text says nothing useful.
// This drives the real code path through a stub binary rather than asserting on
// the arg builders alone.
func TestLaunchdStartStopInvokeLaunchctlWithDomainTarget(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	_, argv := fakeLaunchctl(t, "", 0)

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	got := argv()
	uid := os.Getuid()
	want := []string{
		fmt.Sprintf("bootstrap gui/%d %s", uid, mgr.plistPath),
		fmt.Sprintf("bootout gui/%d/dev.joshbot.joshbot", uid),
	}
	if len(got) != len(want) {
		t.Fatalf("launchctl invocations = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("launchctl invocation %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Regression: Restart not stopping a job that is currently loaded. launchd refuses
// to bootstrap a label that is already bootstrapped, so skipping the bootout turns
// `joshbot service restart` into a no-op that reports success.
func TestLaunchdRestartBootsOutRunningJobFirst(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	_, argv := fakeLaunchctl(t, "123\t0\tdev.joshbot.joshbot", 0)

	if err := mgr.Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	got := argv()
	if len(got) != 3 {
		t.Fatalf("launchctl invocations = %q, want list + bootout + bootstrap", got)
	}
	if got[0] != "list" {
		t.Errorf("first invocation = %q, want %q", got[0], "list")
	}
	if !strings.HasPrefix(got[1], "bootout ") {
		t.Errorf("second invocation = %q, want a bootout of the running job", got[1])
	}
	if !strings.HasPrefix(got[2], "bootstrap ") {
		t.Errorf("third invocation = %q, want a bootstrap", got[2])
	}
}

// Regression: Status reporting "stopped" for a job that launchctl lists as loaded.
// `joshbot status` is what an operator checks before debugging why the bot is
// silent; a false "stopped" sends them to reinstall a healthy service.
func TestLaunchdStatusRunning(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	fakeLaunchctl(t, "4242\t0\tdev.joshbot.joshbot", 0)

	st, err := mgr.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !st.Running || st.Status != "running" {
		t.Errorf("Status() = %+v, want Running=true Status=\"running\"", st)
	}
}

// Regression: Uninstall deleting the plist even though bootout failed, leaving a
// job loaded in launchd that nothing can now stop or reference. It must abort and
// keep the plist so the operator can retry.
func TestLaunchdUninstallAbortsWhenBootoutFails(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	fakeLaunchctl(t, "1\t0\tdev.joshbot.joshbot", 1)

	res, err := mgr.Uninstall()
	if err == nil {
		t.Fatal("Uninstall() succeeded although bootout failed")
	}
	if !strings.Contains(err.Error(), "failed to unload service") {
		t.Errorf("Uninstall() error = %q, want it to say 'failed to unload service'", err)
	}
	if res.Success {
		t.Error("Uninstall() returned Success=true after a failed bootout")
	}
	if !mgr.IsInstalled() {
		t.Error("plist was removed despite the bootout failure; the job is now orphaned in launchd")
	}
}

// Regression: Start/Stop swallowing launchctl's failure and returning nil. The CLI
// prints "Service started" off a nil error, so a job that never loaded looks fine.
func TestLaunchdStartStopPropagateLaunchctlFailure(t *testing.T) {
	mgr, _ := newTestLaunchd(t, Config{Name: "joshbot", ExecPath: "/bin/true", WorkingDir: "/tmp"})
	if _, err := mgr.Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	fakeLaunchctl(t, "", 1)

	if err := mgr.Start(); err == nil {
		t.Error("Start() = nil although launchctl exited non-zero")
	}
	if err := mgr.Stop(); err == nil {
		t.Error("Stop() = nil although launchctl exited non-zero")
	}
}
