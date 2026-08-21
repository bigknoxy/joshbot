package relguard

import (
	"strings"
	"testing"
)

// baseChangelog is the shape this repo's file actually has: a preamble, an
// [Unreleased] section, then released sections newest-first.
const baseChangelog = `# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
- Something new (#200).

## [1.41.0] - 2026-07-01

### Fixed
- The launchd job never started (#177).
- A second fixed thing.

## [1.40.0] - 2026-06-01

### Added
- The first thing.
`

// TestStaleBaseRevertingAReleaseStampFails is failure 2 from issue #120,
// reproduced: three branches cut before [1.41.0] was stamped each carried a
// diff that would have deleted it, and nothing but a human reading the diff
// stood in the way.
func TestStaleBaseRevertingAReleaseStampFails(t *testing.T) {
	head := strings.Replace(baseChangelog, `## [1.41.0] - 2026-07-01

### Fixed
- The launchd job never started (#177).
- A second fixed thing.

`, "", 1)
	if !strings.Contains(head, "1.40.0") || strings.Contains(head, "1.41.0") {
		t.Fatal("the fixture did not remove exactly the 1.41.0 section")
	}

	err := CheckReleasedSectionsIntact(baseChangelog, head)
	if err == nil {
		t.Fatal("deleting a released section must fail the guard")
	}
	// The message has to name the section, or the author is left diffing a
	// thousand-line file to find out what the guard means.
	if !strings.Contains(err.Error(), "1.41.0") {
		t.Fatalf("the error must name the lost section, got: %v", err)
	}
}

// TestLosingOneLineOfAReleasedSectionFails — a whole section going missing is
// the loud case. A single bullet quietly dropped from release history is the
// one that actually survives review.
func TestLosingOneLineOfAReleasedSectionFails(t *testing.T) {
	head := strings.Replace(baseChangelog, "- A second fixed thing.\n", "", 1)

	err := CheckReleasedSectionsIntact(baseChangelog, head)
	if err == nil {
		t.Fatal("dropping a line from a released section must fail the guard")
	}
	if !strings.Contains(err.Error(), "A second fixed thing") {
		t.Fatalf("the error must quote the lost line, got: %v", err)
	}
}

// TestOrdinaryPullRequestPasses — a guard that fires on normal work gets turned
// off. Editing [Unreleased], adding a new release above the old ones, and
// reordering within a released section all have to stay green.
func TestOrdinaryPullRequestPasses(t *testing.T) {
	t.Run("editing Unreleased", func(t *testing.T) {
		head := strings.Replace(baseChangelog,
			"- Something new (#200).",
			"- Something new (#200).\n- And another thing (#201).", 1)
		if err := CheckReleasedSectionsIntact(baseChangelog, head); err != nil {
			t.Fatalf("adding to [Unreleased] must pass: %v", err)
		}
	})

	t.Run("removing from Unreleased", func(t *testing.T) {
		// A release PR moves entries out of [Unreleased] into a new section.
		head := strings.Replace(baseChangelog,
			"## [Unreleased]\n\n### Added\n- Something new (#200).\n",
			"## [Unreleased]\n\n## [1.42.0] - 2026-08-01\n\n### Added\n- Something new (#200).\n", 1)
		if err := CheckReleasedSectionsIntact(baseChangelog, head); err != nil {
			t.Fatalf("stamping a release must pass: %v", err)
		}
	})

	t.Run("reordering within a released section", func(t *testing.T) {
		head := strings.Replace(baseChangelog,
			"- The launchd job never started (#177).\n- A second fixed thing.",
			"- A second fixed thing.\n- The launchd job never started (#177).", 1)
		if err := CheckReleasedSectionsIntact(baseChangelog, head); err != nil {
			t.Fatalf("reordering existing lines loses nothing and must pass: %v", err)
		}
	})

	t.Run("unchanged", func(t *testing.T) {
		if err := CheckReleasedSectionsIntact(baseChangelog, baseChangelog); err != nil {
			t.Fatalf("an untouched changelog must pass: %v", err)
		}
	})
}

// TestTopVersionBehindTheTagFails, and the ahead case that must not.
func TestTopVersion(t *testing.T) {
	t.Run("behind the tag fails", func(t *testing.T) {
		err := CheckTopVersion(baseChangelog, "v1.47.1")
		if err == nil {
			t.Fatal("a changelog topping out at 1.41.0 with v1.47.1 tagged must fail")
		}
		for _, want := range []string{"1.41.0", "v1.47.1"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the error must name %s, got: %v", want, err)
			}
		}
	})

	t.Run("level with the tag passes", func(t *testing.T) {
		if err := CheckTopVersion(baseChangelog, "v1.41.0"); err != nil {
			t.Fatalf("the normal steady state must pass: %v", err)
		}
	})

	t.Run("ahead of the tag passes", func(t *testing.T) {
		// This project stamps the version in a PR and pushes the tag only after
		// it merges, so every release PR is briefly ahead. A guard that failed
		// here would block the exact change it exists to protect.
		if err := CheckTopVersion(baseChangelog, "v1.40.0"); err != nil {
			t.Fatalf("a release PR is ahead of the newest tag and must pass: %v", err)
		}
	})

	t.Run("no tag is not a failure", func(t *testing.T) {
		if err := CheckTopVersion(baseChangelog, ""); err != nil {
			t.Fatalf("an untagged repo has nothing to compare and must pass: %v", err)
		}
	})

	t.Run("a changelog with no release at all fails against a tag", func(t *testing.T) {
		if err := CheckTopVersion("# Changelog\n\n## [Unreleased]\n", "v1.0.0"); err == nil {
			t.Fatal("a tagged repo whose changelog has no released section must fail")
		}
	})

	t.Run("an unreadable tag is reported, not ignored", func(t *testing.T) {
		if err := CheckTopVersion(baseChangelog, "not-a-version"); err == nil {
			t.Fatal("a tag that cannot be parsed must be an error, not a silent pass")
		}
	})
}

// TestTheRepoOwnChangelogParses — the fixtures above are hand-written, so they
// prove the logic and not that it matches the real file. This pins the parser
// against the file the guard will actually run on.
func TestVersionHeadingIgnoresTheDate(t *testing.T) {
	name, ok := versionHeading("## [1.47.1] - 2026-08-10")
	if !ok || name != "1.47.1" {
		t.Fatalf("versionHeading = %q, %v; want 1.47.1, true", name, ok)
	}
	if _, ok := versionHeading("### Added"); ok {
		t.Fatal("a subsection heading is not a version heading")
	}
	if _, ok := versionHeading("Some prose mentioning ## [1.0.0] mid-line"); ok {
		t.Fatal("only a line that starts with the heading counts")
	}
}

// CheckTagHasSection is the tag-time gate (#293): a tag pushed while the
// entries still sit under [Unreleased] must fail the release, naming the tag
// and the missing heading, instead of failing every subsequent PR.
func TestCheckTagHasSection(t *testing.T) {
	changelog := "# Changelog\n\n## [Unreleased]\n\n## [1.57.0] - 2026-08-18\n\n### Fixed\n- A thing.\n"

	if err := CheckTagHasSection(changelog, "v1.57.0"); err != nil {
		t.Errorf("a tag with a matching section must pass: %v", err)
	}
	if err := CheckTagHasSection(changelog, "1.57.0"); err != nil {
		t.Errorf("the v prefix must be optional: %v", err)
	}

	err := CheckTagHasSection(changelog, "v1.58.0")
	if err == nil {
		t.Fatal("a tag with no matching section must fail")
	}
	for _, want := range []string{"## [1.58.0]", "v1.58.0", "[Unreleased]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q, got: %v", want, err)
		}
	}

	if err := CheckTagHasSection(changelog, ""); err == nil {
		t.Error("an empty tag must fail — the gate would otherwise pass having checked nothing")
	}
	if err := CheckTagHasSection(changelog, "not-a-version"); err == nil {
		t.Error("an unparseable tag must fail")
	}
}
