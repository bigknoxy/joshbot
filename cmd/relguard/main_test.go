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

	err := run(filepath.Join(dir, "nope.md"), head, "v1.41.0", false)
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
	if err := run(base, good, "v1.41.0", false); err != nil {
		t.Fatalf("an intact changelog must pass: %v", err)
	}

	bad := write(t, dir, "bad.md", strings.Replace(minimal, "- A thing.\n", "", 1))
	if err := run(base, bad, "v1.41.0", false); err == nil {
		t.Fatal("a changelog that loses a released line must fail")
	}

	// With no base file to compare, the version check still runs.
	if err := run("", base, "v1.47.1", false); err == nil {
		t.Fatal("a changelog behind the tag must fail even with no base comparison")
	}
}

// -require-section is the tag-time mode (#293): only the section check runs,
// and no base file is needed.
func TestRunRequireSection(t *testing.T) {
	dir := t.TempDir()
	head := write(t, dir, "CHANGELOG.md", minimal)

	if err := run("", head, "v1.41.0", true); err != nil {
		t.Errorf("a tag with a matching section must pass: %v", err)
	}
	if err := run("", head, "v1.42.0", true); err == nil {
		t.Error("a tag with no matching section must fail the release")
	}
	if err := run("", head, "", true); err == nil {
		t.Error("an empty tag must fail rather than gate nothing")
	}
}

// When the base branch's own changelog has no section for the newest tag, the
// failure is a release problem someone else caused, and the message must say
// so instead of sending this PR's author hunting through their own diff (#293).
func TestRunBlamesTheTagWhenBaseIsMissingTheSectionToo(t *testing.T) {
	dir := t.TempDir()
	base := write(t, dir, "base.md", minimal)
	head := write(t, dir, "head.md", minimal)

	err := run(base, head, "v1.42.0", false)
	if err == nil {
		t.Fatal("a tag newer than every section must still fail")
	}
	if !strings.Contains(err.Error(), "release problem on the base branch") ||
		!strings.Contains(err.Error(), "not a problem with this PR") {
		t.Errorf("the message must blame the tag, not the PR: %v", err)
	}

	// A branch that is genuinely behind (base has the section, head does not)
	// keeps the original guidance rather than blaming the tag.
	baseNew := write(t, dir, "base-new.md", strings.Replace(minimal, "## [1.41.0]", "## [1.42.0] - 2026-08-01\n\n- Newer.\n\n## [1.41.0]", 1))
	err = run(baseNew, head, "v1.42.0", false)
	if err == nil {
		t.Fatal("a head behind the base's release must fail")
	}
	if strings.Contains(err.Error(), "release problem on the base branch") {
		t.Errorf("a genuinely stale branch must keep the rebase guidance, got: %v", err)
	}
}
