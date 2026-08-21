// Package relguard checks a pull request's CHANGELOG.md against the state of
// the branch it will merge into.
//
// Both checks exist because of failures that were observed for real during the
// v1.40–v1.42 release run, and neither produced a merge conflict, a CI failure
// or any other signal (issue #120):
//
//   - Three branches cut from a pre-release main each carried a diff that would
//     have deleted the `## [1.41.0]` heading. Squash-merging any of them would
//     have un-released a version in the docs while the git tag stayed. They were
//     caught only by reading the diffs by hand.
//   - The top released heading can end up behind the latest tag the same way.
//
// The checks compare the branch's CHANGELOG.md against the *tip* of the base
// branch rather than against the merge base, because the tip is what the squash
// result is measured against: a line that exists on the tip and not on the
// branch is a line the merge removes, however it got that way.
package relguard

import (
	"fmt"
	"strconv"
	"strings"
)

// UnreleasedHeading is the one section a pull request is expected to edit
// freely. Everything below it is release history and is append-only.
const UnreleasedHeading = "Unreleased"

// section is one `## [x.y.z]` block of a changelog.
type section struct {
	name  string   // the text inside the brackets: "1.47.1", "Unreleased"
	lines []string // every line under the heading, up to the next heading
}

// parseSections splits a changelog on its `## [...]` headings. Anything before
// the first heading (the file's preamble) is not a section and is ignored: it
// is prose about the format, not release history.
func parseSections(changelog string) []section {
	var out []section
	for _, line := range strings.Split(changelog, "\n") {
		if name, ok := versionHeading(line); ok {
			out = append(out, section{name: name})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].lines = append(out[len(out)-1].lines, line)
		}
	}
	return out
}

// versionHeading reports whether a line is a `## [name]` heading and returns the
// bracketed name. The trailing date (`## [1.47.1] - 2026-08-10`) is not part of
// the name, so re-dating a release is not mistaken for renaming it.
func versionHeading(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "## [") {
		return "", false
	}
	end := strings.Index(s, "]")
	if end < 0 {
		return "", false
	}
	return s[len("## ["):end], true
}

// CheckReleasedSectionsIntact fails when merging head would remove any line
// from a released section that exists on base.
//
// Release history is append-only: a branch whose diff deletes from it is a
// stale-base artifact, not an intentional edit. The comparison is per line and
// order-insensitive within a section, so reflowing or reordering an existing
// entry is fine and only genuinely losing text is an error. `[Unreleased]` is
// exempt — editing it is the normal thing a pull request does.
func CheckReleasedSectionsIntact(base, head string) error {
	headSections := map[string][]string{}
	for _, s := range parseSections(head) {
		headSections[s.name] = s.lines
	}

	var problems []string
	for _, b := range parseSections(base) {
		if b.name == UnreleasedHeading {
			continue
		}
		hl, ok := headSections[b.name]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"the released section [%s] exists on the base branch but not on this one — "+
					"merging would delete the whole release", b.name))
			continue
		}
		have := map[string]int{}
		for _, l := range hl {
			have[strings.TrimSpace(l)]++
		}
		for _, l := range b.lines {
			t := strings.TrimSpace(l)
			if t == "" {
				continue
			}
			if have[t] == 0 {
				problems = append(problems, fmt.Sprintf(
					"[%s] loses a line that exists on the base branch: %s", b.name, truncate(t)))
				continue
			}
			have[t]--
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("CHANGELOG.md release history is append-only, but merging this branch would "+
		"change it:\n  - %s\n\nThis usually means the branch was cut before a release was stamped. "+
		"Rebase on the base branch and keep the released sections as they are there",
		strings.Join(problems, "\n  - "))
}

func truncate(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// CheckTopVersion fails when the top-most released heading in the changelog is
// *behind* the latest tag on the base branch.
//
// Ahead is deliberately allowed: this project stamps the version in a pull
// request and pushes the tag only after that merges and CI goes green, so a
// changelog one version ahead of the newest tag is the normal state of a
// release PR. Behind is the failure — it means the branch carries a changelog
// from before a release that has already shipped.
//
// An empty tag (a repository with no tags yet, or a shallow checkout that
// fetched none) is not an error: with nothing to compare against there is
// nothing to say, and failing there would block every PR on a fetch depth.
func CheckTopVersion(changelog, latestTag string) error {
	tag := strings.TrimSpace(latestTag)
	if tag == "" {
		return nil
	}
	tagVer, err := parseSemver(tag)
	if err != nil {
		return fmt.Errorf("could not read the latest tag %q: %w", latestTag, err)
	}

	top, ok := topReleased(changelog)
	if !ok {
		return fmt.Errorf("CHANGELOG.md has no released `## [x.y.z]` section, but %s is tagged", tag)
	}
	topVer, err := parseSemver(top)
	if err != nil {
		return fmt.Errorf("the top released heading [%s] is not a version: %w", top, err)
	}
	if compareSemver(topVer, tagVer) < 0 {
		return fmt.Errorf("CHANGELOG.md's newest released section is [%s], but %s is already tagged "+
			"on the base branch — this branch predates that release. Rebase and keep the newer "+
			"section", top, tag)
	}
	return nil
}

// CheckTagHasSection fails when the changelog has no `## [<tag-without-v>]`
// section for the tag being released.
//
// It is the tag-time half of the guard (#293): the PR-side checks compare a
// branch against main, so a tag pushed while the entries still sat under
// `[Unreleased]` produced no failure for the person who tagged — it failed
// changelog-guard on every *subsequent* PR instead, with a message pointing at
// the changelog rather than at the tag. Running this in the release workflow
// stops the release itself, naming the real cause.
func CheckTagHasSection(changelog, tag string) error {
	ver := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if ver == "" {
		return fmt.Errorf("no tag was given to check the changelog against")
	}
	if _, err := parseSemver(ver); err != nil {
		return fmt.Errorf("could not read the release tag %q: %w", tag, err)
	}
	for _, s := range parseSections(changelog) {
		if s.name == ver {
			return nil
		}
	}
	return fmt.Errorf("CHANGELOG.md has no `## [%s]` section, but %s is being released — "+
		"cut the [Unreleased] entries into a `## [%s]` section (and merge that to main) before tagging",
		ver, tag, ver)
}

// topReleased returns the name of the first `## [x.y.z]` heading that is not
// `[Unreleased]`.
func topReleased(changelog string) (string, bool) {
	for _, s := range parseSections(changelog) {
		if s.name != UnreleasedHeading {
			return s.name, true
		}
	}
	return "", false
}

// parseSemver reads `1.47.1` or `v1.47.1`. Pre-release and build suffixes are
// not used by this project's tags, so they are rejected rather than guessed at.
func parseSemver(s string) ([3]int, error) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("%q is not a three-part version", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("%q is not a three-part version", s)
		}
		v[i] = n
	}
	return v, nil
}

func compareSemver(a, b [3]int) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}
