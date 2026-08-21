package main

import (
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The re-exec hook. When this variable is set the test binary behaves as the
// relguard command itself, which is the only way to observe what main() does
// with a failing check: it calls os.Exit, so it cannot be observed in process.
const reexecEnv = "RELGUARD_TEST_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		main()
		return // unreachable on the failure path; main exits.
	}
	os.Exit(m.Run())
}

// runMainInProcess drives main() with the given argv (excluding argv[0]) from
// inside dir, returning what it wrote to stdout. Only safe for arguments that
// make main() succeed — the failure path calls os.Exit.
func runMainInProcess(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Chdir(dir)

	oldArgs, oldFlags, oldStdout := os.Args, flag.CommandLine, os.Stdout
	t.Cleanup(func() { os.Args, flag.CommandLine, os.Stdout = oldArgs, oldFlags, oldStdout })

	flag.CommandLine = flag.NewFlagSet("relguard", flag.ContinueOnError)
	os.Args = append([]string{"relguard"}, args...)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	main()

	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestMainDefaultsToCheckingCHANGELOGmdInTheWorkingDirectory pins the -head
// default. CI invokes relguard without -head in some steps, so a change to that
// default (or to the flag's name) would silently check the wrong file — or no
// file — while still printing success.
// Driven out of process on purpose: if the default ever stops resolving, main()
// takes the os.Exit path, which in process would abort the whole test binary
// instead of failing this one test.
func TestMainDefaultsToCheckingCHANGELOGmdInTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	// Deliberately the only changelog-shaped file here, and deliberately
	// broken: if -head defaulted to anything else, the read would fail with a
	// different path and the assertions below would not hold.
	write(t, dir, "CHANGELOG.md", strings.Replace(minimal, "## [1.41.0] - 2026-07-01", "## [1.0.0] - 2020-01-01", 1))

	stdout, stderr, code := reexec(t, dir, "-tag", "v1.41.0")

	if code != 1 {
		t.Fatalf("the default -head must have found and rejected CHANGELOG.md, got exit %d (stdout %q)", code, stdout)
	}
	if strings.Contains(stderr, "no such file") {
		t.Fatalf("-head must default to CHANGELOG.md in the working directory, got: %q", stderr)
	}
	if !strings.Contains(stderr, "1.0.0") {
		t.Fatalf("expected the version check to have read CHANGELOG.md, got: %q", stderr)
	}
}

// TestMainAcceptsTheDocumentedFlagTriple runs main() the way ci.yml does — with
// all three flags — and checks the success line lands on stdout. This is the
// contract the workflow depends on: exit 0 plus a legible confirmation.
func TestMainAcceptsTheDocumentedFlagTriple(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base-CHANGELOG.md", minimal)
	write(t, dir, "CHANGELOG.md", minimal+"\n- an appended note under 1.41.0\n")

	out := runMainInProcess(t, dir,
		"-base", "base-CHANGELOG.md",
		"-head", "CHANGELOG.md",
		"-tag", "v1.41.0",
	)

	if !strings.Contains(out, "relguard: CHANGELOG.md release history is intact") {
		t.Fatalf("expected the success line on stdout, got %q", out)
	}
}

// reexec runs this test binary as relguard from dir and returns stdout, stderr
// and the exit code.
func reexec(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), reexecEnv+"=1")

	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()

	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running relguard: %v (stderr: %s)", err, stderr.String())
	}
	return stdout.String(), stderr.String(), code
}

// TestMainExitsNonZeroAndSaysWhyWhenTheCheckFails is the guard's whole point:
// CI only notices a violated changelog because the process exits non-zero. A
// version of main() that printed the error but fell through to the success line
// would leave every release-history regression green, so both halves are pinned
// — the exit code and the absence of the success line.
func TestMainExitsNonZeroAndSaysWhyWhenTheCheckFails(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base-CHANGELOG.md", minimal)
	// A released line has been deleted on this branch.
	write(t, dir, "CHANGELOG.md", strings.Replace(minimal, "- A thing.\n", "", 1))

	stdout, stderr, code := reexec(t, dir, "-base", "base-CHANGELOG.md", "-tag", "v1.41.0")

	if code != 1 {
		t.Fatalf("a failed check must exit 1, got %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	if !strings.HasPrefix(stderr, "relguard: ") {
		t.Fatalf("the failure must be reported on stderr with the relguard prefix, got %q", stderr)
	}
	if strings.Contains(stdout, "intact") {
		t.Fatalf("a failed check must not also claim success, stdout was %q", stdout)
	}
}

// TestMainExitsNonZeroWhenTheHeadChangelogIsMissing covers the other way CI can
// be lied to: no CHANGELOG.md at all. Skipping it (or defaulting to an empty
// document) would make every check vacuously pass.
func TestMainExitsNonZeroWhenTheHeadChangelogIsMissing(t *testing.T) {
	dir := t.TempDir()

	stdout, stderr, code := reexec(t, dir, "-tag", "v1.41.0")

	if code != 1 {
		t.Fatalf("a missing head changelog must exit 1, got %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "CHANGELOG.md") {
		t.Fatalf("the error must name the file it could not read, got %q", stderr)
	}
	if strings.Contains(stdout, "intact") {
		t.Fatalf("a missing changelog must not report success, stdout was %q", stdout)
	}
}

// TestRunFailsWhenTheHeadChangelogIsUnreadable pins run()'s own head-read error,
// including that the message names the path — the CI log is the only place this
// is ever diagnosed from.
func TestRunFailsWhenTheHeadChangelogIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-CHANGELOG.md")

	err := run("", missing, "v1.41.0", false)
	if err == nil {
		t.Fatal("an unreadable head changelog must fail")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("the error must name the unreadable path, got: %v", err)
	}
}

// TestRunChecksTheBaseComparisonBeforeTheVersion pins the order of the two
// checks. If run() ran CheckTopVersion first, a PR that both deleted released
// history and lagged the tag would be reported only as a version problem, and
// the far more serious history loss would never be named in the CI log.
func TestRunChecksTheBaseComparisonBeforeTheVersion(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "base.md", minimal)
	// Both broken at once: released content deleted, and behind the tag.
	head := write(t, dir, "head.md", strings.Replace(minimal, "- A thing.\n", "", 1))

	err := run(base, head, "v9.99.0", false)
	if err == nil {
		t.Fatal("a changelog that is both truncated and behind the tag must fail")
	}
	if strings.Contains(err.Error(), "9.99.0") {
		t.Fatalf("the released-history failure must be reported first, got the version error: %v", err)
	}
}
