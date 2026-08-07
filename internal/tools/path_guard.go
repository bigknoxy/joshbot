// Package tools provides the tool system for joshbot's agent.
package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// isWithinBase returns true if path is inside base (or equal), after cleaning.
func isWithinBase(path, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && rel != "")
}

// resolveSymlinks returns path with all symlinks resolved, so that a lexically
// inside path that actually points outside the base (e.g. ws/link -> /etc) can
// be caught by isWithinBase. filepath.EvalSymlinks requires the target to
// exist, which would break creating a new file, so for a not-yet-existing path
// we resolve the nearest existing ancestor and re-append the remaining
// components. The result is the path a containment check — and the subsequent
// operation, to avoid a TOCTOU gap — should use.
func resolveSymlinks(path string) (string, error) {
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	// Walk up to the nearest existing ancestor, resolve it, then re-append the
	// missing tail. This still catches a symlinked ancestor (the escape vector)
	// while letting a create inside the workspace succeed.
	var missing []string
	cur := path
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the filesystem root without finding an existing ancestor.
			return path, nil
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		resolvedParent, perr := filepath.EvalSymlinks(parent)
		if perr == nil {
			return filepath.Join(append([]string{resolvedParent}, missing...)...), nil
		}
		if !os.IsNotExist(perr) {
			return "", perr
		}
		cur = parent
	}
}
