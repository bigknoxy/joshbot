// Command relguard is the CI-side entry point for the release-history checks in
// internal/relguard. It is a separate main package on purpose: nothing here
// belongs in the joshbot binary an operator installs.
//
// Usage (see .github/workflows/ci.yml):
//
//	go run ./cmd/relguard -base base-CHANGELOG.md -head CHANGELOG.md -tag v1.47.1
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/bigknoxy/joshbot/internal/relguard"
)

func main() {
	base := flag.String("base", "", "path to CHANGELOG.md as it is on the base branch")
	head := flag.String("head", "CHANGELOG.md", "path to CHANGELOG.md as it is on this branch")
	tag := flag.String("tag", "", "latest release tag on the base branch (empty skips the version check)")
	flag.Parse()

	if err := run(*base, *head, *tag); err != nil {
		fmt.Fprintf(os.Stderr, "relguard: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("relguard: CHANGELOG.md release history is intact")
}

func run(basePath, headPath, tag string) error {
	headBytes, err := os.ReadFile(headPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", headPath, err)
	}
	head := string(headBytes)

	// A missing base file is a real failure, not something to skip: it means the
	// workflow could not produce the comparison, and reporting "intact" there
	// would make the guard report success precisely when it checked nothing.
	if basePath != "" {
		baseBytes, err := os.ReadFile(basePath)
		if err != nil {
			return fmt.Errorf("reading the base branch's changelog from %s: %w", basePath, err)
		}
		if err := relguard.CheckReleasedSectionsIntact(string(baseBytes), head); err != nil {
			return err
		}
	}
	return relguard.CheckTopVersion(head, tag)
}
