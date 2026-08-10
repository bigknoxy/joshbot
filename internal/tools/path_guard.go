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
// components — resolving each re-appended component itself, because a symlink
// whose target does not exist is reported by EvalSymlinks as ENOENT just like a
// plain missing file. Skipping that step returned the lexical path for
// `ws/evil -> /outside/x`, which passes isWithinBase and is then followed by
// the open that comes next.
//
// The result is the path a containment check should use. It does NOT close the
// TOCTOU gap: nothing stops the final component being replaced by a symlink
// between this call and the open, so a write path must additionally open with
// O_NOFOLLOW (see writeNoFollow in filesystem.go).
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
			return appendResolved(resolvedParent, missing)
		}
		if !os.IsNotExist(perr) {
			return "", perr
		}
		cur = parent
	}
}

// appendResolved joins base with tail one component at a time, following any
// component that turns out to be a symlink. filepath.EvalSymlinks cannot do
// this itself: a link pointing at something that does not exist fails with
// ENOENT, so the escaping component would be re-appended verbatim and the
// containment check would inspect the link's own path instead of its target.
func appendResolved(base string, tail []string) (string, error) {
	cur := base
	for _, comp := range tail {
		cur = filepath.Join(cur, comp)

		fi, err := os.Lstat(cur)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, rerr := os.Readlink(cur)
		if rerr != nil {
			return "", rerr
		}
		if filepath.IsAbs(target) {
			cur = filepath.Clean(target)
		} else {
			cur = filepath.Clean(filepath.Join(filepath.Dir(cur), target))
		}
		// Resolve the target the same way, so a chain of dangling links is
		// followed to its end and a workspace that itself lives behind a
		// symlink (macOS /tmp -> /private/tmp) still compares equal to the
		// resolved base. A link cycle terminates: EvalSymlinks reports ELOOP,
		// which is not ENOENT, so resolveSymlinks returns the error rather
		// than recursing.
		r, rerr := resolveSymlinks(cur)
		if rerr != nil {
			return "", rerr
		}
		cur = r
	}
	return cur, nil
}
