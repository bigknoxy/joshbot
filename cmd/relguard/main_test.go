package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimal = `# Changelog

## [Unreleased]

## [1.41.0] - 2026-07-01

### Fixed
- A thing.
`

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// TestRunFailsWhenTheBaseChangelogIsMissing — the guard's whole value is that it
// fires. A workflow step that could not produce the base file must be an error,
// because reporting success there means reporting success on an unchecked PR.
func TestRunFailsWhenTheBaseChangelogIsMissing(t *testing.T) {
	dir := t.TempDir()
	head := write(t, dir, "CHANGELOG.md", minimal)

	err := run(filepath.Join(dir, "nope.md"), head, "v1.41.0")
	if err == nil {
		t.Fatal("an unreadable base changelog must fail")
	}
	if !strings.Contains(err.Error(), "base branch") {
		t.Fatalf("the error must say which file it could not read, got: %v", err)
	}
}

func TestRunPassesAndFailsOnRealFiles(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "base.md", minimal)

	good := write(t, dir, "good.md", minimal+"\n- appended under 1.41.0 is fine\n")
	if err := run(base, good, "v1.41.0"); err != nil {
		t.Fatalf("an intact changelog must pass: %v", err)
	}

	bad := write(t, dir, "bad.md", strings.Replace(minimal, "- A thing.\n", "", 1))
	if err := run(base, bad, "v1.41.0"); err == nil {
		t.Fatal("a changelog that loses a released line must fail")
	}

	// With no base file to compare, the version check still runs.
	if err := run("", base, "v1.47.1"); err == nil {
		t.Fatal("a changelog behind the tag must fail even with no base comparison")
	}
}
