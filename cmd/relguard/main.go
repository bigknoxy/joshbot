// Command relguard is the CI-side entry point for the release-history checks in
// internal/relguard. It is a separate main package on purpose: nothing here
// belongs in the joshbot binary an operator installs.
//
// Two modes (see .github/workflows/ci.yml and release.yml):
//
//	go run ./cmd/relguard -base base-CHANGELOG.md -head CHANGELOG.md -tag v1.47.1
//	go run ./cmd/relguard -head CHANGELOG.md -tag v1.47.1 -require-section
//
// The first is the pull-request guard. The second is the tag-time gate (#293):
// it fails the release workflow when the tag being released has no matching
// `## [x.y.z]` section, instead of letting the tag ship and every subsequent
// PR fail the first mode for a release problem that PR did not cause.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/bigknoxy/joshbot/internal/relguard"
)

func main() {
	base := flag.String("base", "", "path to CHANGELOG.md as it is on the base branch")
	head := flag.String("head", "CHANGELOG.md", "path to CHANGELOG.md as it is on this branch")
	tag := flag.String("tag", "", "latest release tag on the base branch (empty skips the version check)")
	requireSection := flag.Bool("require-section", false,
		"tag-time gate: require a `## [x.y.z]` section matching -tag (release workflow, not PRs)")
	flag.Parse()

	if err := run(*base, *head, *tag, *requireSection); err != nil {
		fmt.Fprintf(os.Stderr, "relguard: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("relguard: CHANGELOG.md release history is intact")
}

func run(basePath, headPath, tag string, requireSection bool) error {
	headBytes, err := os.ReadFile(headPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", headPath, err)
	}
	head := string(headBytes)

	if requireSection {
		return relguard.CheckTagHasSection(head, tag)
	}

	// A missing base file is a real failure, not something to skip: it means the
	// workflow could not produce the comparison, and reporting "intact" there
	// would make the guard report success precisely when it checked nothing.
	var base string
	if basePath != "" {
		baseBytes, err := os.ReadFile(basePath)
		if err != nil {
			return fmt.Errorf("reading the base branch's changelog from %s: %w", basePath, err)
		}
		base = string(baseBytes)
		if err := relguard.CheckReleasedSectionsIntact(base, head); err != nil {
			return err
		}
	}
	if err := relguard.CheckTopVersion(head, tag); err != nil {
		// When the base branch's own changelog also has no section for the
		// newest tag, this PR did nothing wrong — someone tagged without
		// cutting a section (#293). Say so, or the contributor is sent
		// hunting through their own diff for a release problem.
		if basePath != "" && relguard.CheckTopVersion(base, tag) != nil {
			return fmt.Errorf("the base branch's newest tag %s has no matching `## [%s]` section in the "+
				"base branch's own CHANGELOG.md — this is a release problem on the base branch, not a "+
				"problem with this PR. Cut the missing section on the base branch (see #293).\n\n"+
				"Original error: %w", tag, strings.TrimPrefix(strings.TrimSpace(tag), "v"), err)
		}
		return err
	}
	return nil
}
