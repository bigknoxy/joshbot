package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/service"
)

// Everything past the version check overwrites the binary the operator is
// currently running, and every way it can go wrong leaves them with no working
// joshbot and no way to run `joshbot update` again to recover. None of it had
// coverage, because the download URL was hardcoded at github.com and the last
// statement of a successful update replaces the process image.
// releaseDownloadBase and execSelf are package vars for exactly that.

// withDownloadBase points the binary download at an httptest server.
func withDownloadBase(t *testing.T, h http.HandlerFunc) *string {
	t.Helper()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	prev := releaseDownloadBase
	releaseDownloadBase = srv.URL
	t.Cleanup(func() { releaseDownloadBase = prev })
	return &gotPath
}

// withExecSelf records the restart instead of replacing the test process.
func withExecSelf(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := execSelf
	execSelf = func(argv0 string, argv []string, envv []string) error {
		got = append([]string{argv0}, argv...)
		return nil
	}
	t.Cleanup(func() { execSelf = prev })
	return &got
}

// installedBinary writes a stand-in for the running binary and points
// osExecutable at it.
func installedBinary(t *testing.T, content string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "joshbot")
	if err := os.WriteFile(bin, []byte(content), 0755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	withExecutable(t, bin)
	return bin
}

// The whole point of the command. The replacement has to land on the path the
// operator's shell resolves — writing the new bytes anywhere else reports
// success while leaving the old binary in place, which is indistinguishable
// from an update that silently does nothing.
func TestUpdateReplacesTheRunningBinaryAndRestartsIt(t *testing.T) {
	withVersion(t, "v1.0.0")
	bin := installedBinary(t, "old-binary")
	withServiceManager(t, &stubManager{}, nil)
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	})
	gotPath := withDownloadBase(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("new-binary"))
	})
	execArgs := withExecSelf(t)

	out, code := runCLI(t, "update")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out)
	}

	got, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("the binary is gone after a successful update: %v", err)
	}
	if string(got) != "new-binary" {
		t.Errorf("binary content = %q, want the downloaded bytes", got)
	}

	// A binary that is not executable is as broken as a missing one, and the
	// downloaded temp file starts at whatever umask gives it.
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0100 == 0 {
		t.Errorf("mode = %v, want the owner execute bit set", info.Mode().Perm())
	}

	// Leaving these behind fills the install directory with half-updates, and
	// a stale .bak next to the binary is the thing operators restore by hand.
	for _, leftover := range []string{".joshbot_new", "joshbot.bak"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(bin), leftover)); err == nil {
			t.Errorf("%s was left behind after a successful update", leftover)
		}
	}

	// The URL is assembled from four moving parts and the release tag appears
	// twice, as the directory and inside the asset name. Getting any of them
	// wrong is a 404 the command reports as "release not found for this
	// platform/architecture", which reads as an unsupported machine.
	wantPath := fmt.Sprintf("/v9.9.9/joshbot_v9.9.9_%s_%s", runtime.GOOS, runtime.GOARCH)
	if *gotPath != wantPath {
		t.Errorf("download path = %q, want %q", *gotPath, wantPath)
	}

	if len(*execArgs) == 0 {
		t.Fatal("the updated binary was never restarted")
	}
	// getBinaryPath resolves symlinks, and macOS temp dirs are one (/var ->
	// /private/var), so compare the resolved paths.
	wantExe, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if (*execArgs)[0] != wantExe {
		t.Errorf("restarted %q, want the replaced binary %q", (*execArgs)[0], wantExe)
	}
}

// A download that fails must leave the existing install exactly as it was.
// Replacing the binary with a truncated or error-page body is the one outcome
// the operator cannot recover from with the same command.
func TestUpdateLeavesTheBinaryIntactWhenTheDownloadFails(t *testing.T) {
	withVersion(t, "v1.0.0")
	bin := installedBinary(t, "old-binary")
	withServiceManager(t, &stubManager{}, nil)
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	})
	withDownloadBase(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	execArgs := withExecSelf(t)

	out, code := runCLI(t, "update")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (a failed download is not a crash)\n%s", code, out)
	}
	if !strings.Contains(out, "Error downloading") {
		t.Errorf("the failure was not reported:\n%s", out)
	}

	got, err := os.ReadFile(bin)
	if err != nil || string(got) != "old-binary" {
		t.Fatalf("the existing binary did not survive a failed download: %q, %v", got, err)
	}
	if strings.Contains(out, "Updated joshbot") {
		t.Errorf("a failed download reported success:\n%s", out)
	}
	if len(*execArgs) != 0 {
		t.Errorf("the binary was restarted after a failed download: %v", *execArgs)
	}
}

// When joshbot runs under launchd or systemd, exec'ing over the process leaves
// the supervisor tracking a process it did not start: the new image runs
// without the service's environment and the next `service restart` kills the
// wrong thing. The service manager has to do the restart.
func TestUpdateRestartsTheServiceInsteadOfExecing(t *testing.T) {
	withVersion(t, "v1.0.0")
	installedBinary(t, "old-binary")
	svc := &stubManager{installed: true, status: service.Status{Running: true}}
	withServiceManager(t, svc, nil)
	withReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	})
	withDownloadBase(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("new-binary"))
	})
	execArgs := withExecSelf(t)

	out, code := runCLI(t, "update")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out)
	}
	if !svc.restarted {
		t.Error("the service was never restarted, so the daemon keeps running the old binary")
	}
	if len(*execArgs) != 0 {
		t.Errorf("a service install was exec'd over instead of restarted: %v", *execArgs)
	}
}
