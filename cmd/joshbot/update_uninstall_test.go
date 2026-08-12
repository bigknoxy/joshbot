package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/service"
)

// The update check and the uninstall path are the two places where joshbot
// talks to something it cannot take back: a release API it does not own, and
// the user's own filesystem. Neither had a test, because neither had a seam.
// These drive both through releaseAPIURL / osExecutable / newServiceManager.

func withReleaseAPI(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	prev := releaseAPIURL
	releaseAPIURL = srv.URL
	t.Cleanup(func() { releaseAPIURL = prev })
}

// The happy path: the tag name in the release document is what comes back.
func TestGetLatestVersionReturnsTheTagName(t *testing.T) {
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	})

	got, err := getLatestVersion()
	if err != nil {
		t.Fatalf("getLatestVersion() error = %v", err)
	}
	if got != "v9.9.9" {
		t.Errorf("getLatestVersion() = %q, want the tag_name (v9.9.9)", got)
	}
}

// A non-200 must be an error naming the status. GitHub answers 403 on rate
// limit with a perfectly parseable body whose tag_name is absent, so treating
// any body as a release reports "" as the newest version and tells the user
// they are up to date when the check never ran.
func TestGetLatestVersionRejectsANon200(t *testing.T) {
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"rate limit exceeded"}`))
	})

	_, err := getLatestVersion()
	if err == nil {
		t.Fatal("a 403 from the release API was reported as success")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want it to name the status code", err)
	}
}

// A 200 carrying something that is not a release document is an error, not an
// empty version — same reason as above, one layer down.
func TestGetLatestVersionRejectsAnUnparseableBody(t *testing.T) {
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html>not json`))
	})

	if _, err := getLatestVersion(); err == nil {
		t.Fatal("an unparseable body was reported as a successful version check")
	}
}

// A well-formed document with no tag is the case that silently produced "".
func TestGetLatestVersionRejectsAMissingTag(t *testing.T) {
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"some release"}`))
	})

	got, err := getLatestVersion()
	if err == nil {
		t.Fatalf("a release with no tag_name was accepted, returning %q", got)
	}
}

// stubManager is a service.Manager that records what was called and returns
// whatever the test needs it to.
type stubManager struct {
	installed   bool
	uninstalled bool
	didInstall  bool
	err         error

	installResult service.Result
	installErr    error
	status        service.Status
	statusErr     error
	startErr      error
	statusCalls   int
}

func (s *stubManager) Install() (service.Result, error) {
	s.didInstall = true
	return s.installResult, s.installErr
}
func (s *stubManager) Uninstall() (service.Result, error) {
	s.uninstalled = true
	if s.err != nil {
		return service.Result{}, s.err
	}
	return service.Result{Message: "service removed", Success: true}, nil
}
func (s *stubManager) Status() (service.Status, error) {
	s.statusCalls++
	return s.status, s.statusErr
}
func (s *stubManager) Start() error      { return s.startErr }
func (s *stubManager) Stop() error       { return nil }
func (s *stubManager) Restart() error    { return nil }
func (s *stubManager) IsInstalled() bool { return s.installed }
func (s *stubManager) Name() string      { return "joshbot" }

// uninstallEnv points osExecutable at a throwaway binary and config.DefaultHome
// at a throwaway directory, so the removals are real but land in a temp dir.
// It returns both paths and the stub service manager.
func uninstallEnv(t *testing.T, svc service.Manager) (string, string) {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "joshbot")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir fake home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write fake config: %v", err)
	}

	prevExe, prevHome, prevSvc := osExecutable, config.DefaultHome, newServiceManager
	osExecutable = func() (string, error) { return bin, nil }
	config.DefaultHome = home
	newServiceManager = func(cfg service.Config) (service.Manager, error) { return svc, nil }
	t.Cleanup(func() {
		osExecutable = prevExe
		config.DefaultHome = prevHome
		newServiceManager = prevSvc
	})

	return bin, home
}

// withVersion sets the compiled-in version for one test. Everything runUpdate
// decides hangs off it.
func withVersion(t *testing.T, v string) {
	t.Helper()
	prev := Version
	Version = v
	t.Cleanup(func() { Version = prev })
}

// withExecutable points the running-binary path at something harmless.
func withExecutable(t *testing.T, path string) {
	t.Helper()
	prev := osExecutable
	osExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { osExecutable = prev })
}

// An unreachable or unhappy release API must not fail the update command: it
// prints the manual download URL and exits zero. Exiting non-zero here breaks
// every wrapper that runs `joshbot update` on a schedule, for a condition that
// is nearly always GitHub rate-limiting rather than anything wrong locally.
func TestUpdateSurvivesAFailedVersionCheck(t *testing.T) {
	withVersion(t, "v1.0.0")
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	out, code := runCLI(t, "update")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (a failed version check is not a failed update)", code)
	}
	if !strings.Contains(out, "Error checking for updates") {
		t.Errorf("the failure was not reported to the user:\n%s", out)
	}
	if !strings.Contains(out, "github.com/bigknoxy/joshbot/releases") {
		t.Errorf("no manual download route was offered:\n%s", out)
	}
}

// A version at or ahead of the latest release stops before touching the binary.
// Getting the comparison backwards would have joshbot downgrade itself on every
// run, which looks like an update succeeding.
func TestUpdateStopsWhenAlreadyCurrent(t *testing.T) {
	withVersion(t, "v9.9.9")
	withExecutable(t, filepath.Join(t.TempDir(), "joshbot"))
	var hits int
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	})

	out, code := runCLI(t, "update")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "Already up to date") {
		t.Errorf("a newer local version was not reported as up to date:\n%s", out)
	}
	if strings.Contains(out, "Downloading update") {
		t.Errorf("a download was started despite being up to date:\n%s", out)
	}
	if hits != 1 {
		t.Errorf("release API called %d times, want exactly 1", hits)
	}
}

// `go run` builds into the go-build cache, so replacing "the binary" would
// overwrite a throwaway file and leave the user's install untouched while
// reporting success. It must be refused, and the refusal must be an error.
func TestUpdateRefusesToReplaceAGoRunBinary(t *testing.T) {
	withVersion(t, "v1.0.0")
	withExecutable(t, filepath.Join(t.TempDir(), "go-build123", "b001", "exe", "joshbot"))
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	})

	app := newApp()
	app.Writer = io.Discard
	app.ErrWriter = io.Discard
	app.ExitErrHandler = func(*cli.Context, error) {}

	var err error
	out := captureStdout(t, func() {
		err = app.Run([]string{"joshbot", "update"})
	})
	if err == nil {
		t.Fatal("update from a go run binary exited 0")
	}
	if !strings.Contains(err.Error(), "go run") {
		t.Errorf("error = %v, want it to name 'go run' as the reason", err)
	}
	if strings.Contains(out, "Updated joshbot") {
		t.Errorf("the update reported success anyway:\n%s", out)
	}
}

// withServiceManager substitutes the launchd/systemd factory for the duration of
// a test. svc may be nil when the test wants the factory itself to fail.
func withServiceManager(t *testing.T, svc service.Manager, err error) {
	t.Helper()
	prev := newServiceManager
	newServiceManager = func(cfg service.Config) (service.Manager, error) { return svc, err }
	t.Cleanup(func() { newServiceManager = prev })
}

// `service install` must surface the manager's own message and log path — that
// path is the only thing the operator has to debug a daemon that will not start,
// and printing a generic "installed" instead sends them hunting for it.
func TestServiceInstallPrintsTheResultAndLogPath(t *testing.T) {
	svc := &stubManager{installResult: service.Result{
		Message: "installed as com.bigknoxy.joshbot",
		LogPath: "/var/log/joshbot.log",
		Success: true,
	}}
	withServiceManager(t, svc, nil)

	out, code := runCLI(t, "service", "install")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !svc.didInstall {
		t.Fatal("service install never called Install()")
	}
	if !strings.Contains(out, "installed as com.bigknoxy.joshbot") {
		t.Errorf("output did not carry the manager's message:\n%s", out)
	}
	if !strings.Contains(out, "/var/log/joshbot.log") {
		t.Errorf("output did not carry the log path:\n%s", out)
	}
}

// A failing Install is an error, not a success with a banner. Reporting zero
// here tells an install script the daemon is running when nothing was written.
func TestServiceInstallFailureIsAnError(t *testing.T) {
	withServiceManager(t, &stubManager{installErr: errors.New("launchctl bootstrap refused")}, nil)

	if _, code := runCLI(t, "service", "install"); code == 0 {
		t.Fatal("a failed service install exited 0")
	}
}

// A platform with no service backend must say so, on every service subcommand.
func TestServiceCommandsRefuseAnUnsupportedPlatform(t *testing.T) {
	for _, sub := range []string{"install", "uninstall", "status"} {
		t.Run(sub, func(t *testing.T) {
			withServiceManager(t, nil, errors.New("no service backend"))

			app := newApp()
			app.Writer = io.Discard
			app.ErrWriter = io.Discard
			app.ExitErrHandler = func(*cli.Context, error) {}

			var err error
			_ = captureStdout(t, func() {
				err = app.Run([]string{"joshbot", "service", sub})
			})
			if err == nil {
				t.Fatalf("service %s exited 0 with no service backend", sub)
			}
			if !strings.Contains(err.Error(), "not supported on this platform") {
				t.Errorf("error = %v, want it to say the platform is unsupported", err)
			}
		})
	}
}

// A service that was never installed reports an empty Status string. Printing it
// raw gave the operator a bare "Status: " — true of nothing, useful to no one.
func TestServiceStatusNamesAnEmptyStatus(t *testing.T) {
	withServiceManager(t, &stubManager{status: service.Status{}}, nil)

	out, code := runCLI(t, "service", "status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("a blank status was not reported as not installed:\n%s", out)
	}
	if strings.Contains(out, "Status: \n") {
		t.Errorf("a bare empty status reached the operator:\n%s", out)
	}
}

// A Status the backend cannot determine is reported and exits zero: `service
// status` is what a health check runs, and a non-zero exit there reads as
// "joshbot is down" when the truth is "launchctl would not answer".
func TestServiceStatusReportsAnUndeterminedStatusWithoutFailing(t *testing.T) {
	withServiceManager(t, &stubManager{statusErr: errors.New("launchctl print failed")}, nil)

	out, code := runCLI(t, "service", "status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (an unreadable status is not a failure)", code)
	}
	if !strings.Contains(out, "Unable to determine") {
		t.Errorf("output did not say the status could not be determined:\n%s", out)
	}
	if !strings.Contains(out, "launchctl print failed") {
		t.Errorf("output dropped the underlying reason:\n%s", out)
	}
}

// A running service must be reported as running. This and the blank-status case
// are the two the operator actually reads.
func TestServiceStatusReportsARunningService(t *testing.T) {
	withServiceManager(t, &stubManager{status: service.Status{Running: true, Installed: true}}, nil)

	out, _ := runCLI(t, "service", "status")
	if !strings.Contains(out, "is currently running") {
		t.Errorf("a running service was not reported as running:\n%s", out)
	}
}

// `service uninstall` surfaces the manager's message. The command is the only
// feedback the operator gets that the daemon definition is gone.
func TestServiceUninstallReportsTheResult(t *testing.T) {
	svc := &stubManager{}
	withServiceManager(t, svc, nil)

	out, code := runCLI(t, "service", "uninstall")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !svc.uninstalled {
		t.Fatal("service uninstall never called Uninstall()")
	}
	if !strings.Contains(out, "service removed") {
		t.Errorf("output did not carry the manager's message:\n%s", out)
	}
}

// A failing Uninstall must be an error here, unlike the whole-app `uninstall`
// where the binary still has to go. `service uninstall` has nothing else to do,
// so exiting zero claims a daemon was removed that is still registered.
func TestServiceUninstallFailureIsAnError(t *testing.T) {
	withServiceManager(t, &stubManager{err: errors.New("launchctl refused")}, nil)

	if _, code := runCLI(t, "service", "uninstall"); code == 0 {
		t.Fatal("a failed service uninstall exited 0")
	}
}

// runUpdate decides whether to restart the service or exec itself from this.
// Under `go run` it must answer before touching the service manager at all:
// the binary is throwaway, and probing launchd to decide what to do with it is
// both pointless and, on a machine with a real joshbot service, misleading.
func TestDetectRunningContextAnswersGoRunWithoutProbingTheService(t *testing.T) {
	svc := &stubManager{installed: true, status: service.Status{Running: true}}
	withServiceManager(t, svc, nil)
	withExecutable(t, filepath.Join(t.TempDir(), "go-build999", "b001", "exe", "joshbot"))

	ctx := detectRunningContext()
	if !ctx.IsGoRun {
		t.Error("a go-build path was not detected as go run")
	}
	if ctx.IsService {
		t.Error("go run was also reported as a running service")
	}
	if svc.statusCalls != 0 {
		t.Errorf("the service was probed %d time(s) under go run", svc.statusCalls)
	}
}

// Installed is not the same as running. An installed-but-stopped service must
// not be reported as a service context: runUpdate would then "restart" it and
// return, leaving the user with a stopped daemon and a success message,
// instead of exec'ing the new binary in place.
func TestDetectRunningContextSeparatesInstalledFromRunning(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "joshbot")
	withExecutable(t, bin)

	t.Run("installed and running", func(t *testing.T) {
		withServiceManager(t, &stubManager{installed: true, status: service.Status{Running: true}}, nil)
		if !detectRunningContext().IsService {
			t.Error("a running installed service was not detected")
		}
	})

	t.Run("installed but stopped", func(t *testing.T) {
		withServiceManager(t, &stubManager{installed: true, status: service.Status{Running: false}}, nil)
		if detectRunningContext().IsService {
			t.Error("a stopped service was reported as a running service context")
		}
	})

	t.Run("no service backend", func(t *testing.T) {
		withServiceManager(t, nil, errors.New("unsupported"))
		if detectRunningContext().IsService {
			t.Error("a platform with no service backend reported a service context")
		}
	})
}

// A service that installs but will not start is a warning with a recovery
// command, not a failure: the unit is written, and failing the whole onboarding
// at that point leaves the user with a half-configured install and no hint.
func TestDoServiceInstallTreatsAFailedStartAsAWarning(t *testing.T) {
	svc := &stubManager{
		installResult: service.Result{Message: "unit written", LogPath: "/var/log/joshbot.log"},
		startErr:      errors.New("launchctl kickstart refused"),
	}
	withServiceManager(t, svc, nil)

	var err error
	out := captureStdout(t, func() { err = doServiceInstall() })
	if err != nil {
		t.Fatalf("doServiceInstall() error = %v, want nil (a failed start is not a failed install)", err)
	}
	if !strings.Contains(out, "Could not start service") {
		t.Errorf("the failed start was not reported:\n%s", out)
	}
	if !strings.Contains(out, "joshbot service start") {
		t.Errorf("no recovery command was offered:\n%s", out)
	}
	if !strings.Contains(out, "/var/log/joshbot.log") {
		t.Errorf("the log path was dropped, which is the only debugging route:\n%s", out)
	}
}

// A failed Install is fatal here — there is no unit to start and nothing to
// warn about.
func TestDoServiceInstallFailureIsAnError(t *testing.T) {
	withServiceManager(t, &stubManager{installErr: errors.New("permission denied")}, nil)

	var err error
	_ = captureStdout(t, func() { err = doServiceInstall() })
	if err == nil {
		t.Fatal("doServiceInstall() returned nil after Install failed")
	}
}

// --force removes the binary, the config directory and an installed service,
// with no prompt. This is the whole point of the flag: an unattended uninstall
// that stops at a Scanln is a hang, not a refusal.
func TestUninstallForceRemovesBinaryConfigAndService(t *testing.T) {
	svc := &stubManager{installed: true}
	bin, home := uninstallEnv(t, svc)

	_, code := runCLI(t, "uninstall", "--force")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Errorf("binary still present at %s (stat err = %v)", bin, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("config dir still present at %s (stat err = %v)", home, err)
	}
	if !svc.uninstalled {
		t.Error("an installed service was left behind by --force")
	}
}

// --keep-config is the difference between uninstalling joshbot and destroying
// the user's provider keys, memory and sessions. It must survive --force.
func TestUninstallForceKeepConfigLeavesTheConfigDirectory(t *testing.T) {
	svc := &stubManager{}
	bin, home := uninstallEnv(t, svc)

	_, code := runCLI(t, "uninstall", "--force", "--keep-config")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Error("the binary was not removed")
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("--keep-config still removed the config directory: %v", err)
	}
}

// A service that fails to uninstall is a warning, not a fatal error: the binary
// still has to go, or the user is left with a half-uninstalled joshbot and no
// way to retry except by hand.
func TestUninstallContinuesWhenTheServiceUninstallFails(t *testing.T) {
	svc := &stubManager{installed: true, err: errors.New("launchctl refused")}
	bin, _ := uninstallEnv(t, svc)

	_, code := runCLI(t, "uninstall", "--force")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (a failed service uninstall must not abort)", code)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Error("the binary was left in place after a failed service uninstall")
	}
}

// A binary that is not there is refused up front, with a message naming the
// path it looked at. Letting it fall through to os.Remove also fails, but
// reports "failed to remove binary", which sends the user looking for a
// permissions problem instead of telling them joshbot is not where it thinks.
func TestUninstallFailsWhenTheBinaryIsMissing(t *testing.T) {
	svc := &stubManager{}
	bin, _ := uninstallEnv(t, svc)
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove fake binary: %v", err)
	}

	app := newApp()
	app.Writer = io.Discard
	app.ErrWriter = io.Discard
	app.ExitErrHandler = func(*cli.Context, error) {}

	var err error
	_ = captureStdout(t, func() {
		err = app.Run([]string{"joshbot", "uninstall", "--force"})
	})
	if err == nil {
		t.Fatal("uninstall reported success with no binary to remove")
	}
	if !strings.Contains(err.Error(), "binary not found") {
		t.Errorf("error = %v, want it to report the binary was not found", err)
	}
	if !strings.Contains(err.Error(), bin) {
		t.Errorf("error = %v, want it to name the path it looked at (%s)", err, bin)
	}
}
