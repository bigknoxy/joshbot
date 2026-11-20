package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxGlobResults caps how many paths a pattern with a "**" segment returns.
// A recursive walk can reach every entry in a workspace, and the result is
// text the model has to read; the cap keeps one glob from filling a turn.
const maxGlobResults = 100

// errGlobLimit stops the walk once the result cap is reached. It is not a
// failure to report: it means there are more matches than were returned.
var errGlobLimit = errors.New("glob result limit reached")

// hasDoubleStarSegment reports whether a pattern has a "**" segment. Two stars
// inside one name ("a**b") are two wildcards in a name, exactly as
// filepath.Glob reads them, and are not a recursive wildcard.
func hasDoubleStarSegment(pattern string) bool {
	for _, segment := range splitGlobPattern(pattern) {
		if segment == "**" {
			return true
		}
	}
	return false
}

// splitGlobPattern splits a pattern on its separators. The empty segments a
// leading or doubled separator leaves behind are dropped, and a volume name is
// kept out of the way: it names the root, not a segment to match.
func splitGlobPattern(pattern string) []string {
	volume := filepath.VolumeName(pattern)
	return strings.FieldsFunc(pattern[len(volume):], isGlobSeparator)
}

// isGlobSeparator reports whether r separates path segments. Both separators
// count, so a pattern written with forward slashes works on Windows too.
func isGlobSeparator(r rune) bool {
	return r == '/' || r == filepath.Separator
}

// hasGlobMeta reports whether a name carries a glob metacharacter.
func hasGlobMeta(name string) bool {
	return strings.ContainsAny(name, "*?[")
}

// globWorkspace returns the workspace in the terms the walk runs in: absolute
// and symlink-resolved, so the paths the walk returns and the paths they are
// rendered against are comparable.
func globWorkspace(workspace string) string {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		abs = workspace
	}
	resolved, err := resolveSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

// globWalkBase splits a pattern into the directory the walk starts from and
// the segments to match under it. The literal prefix of the pattern is the
// base, so "a/**/b" walks a and matches b below it; a wildcard in the prefix
// leaves the base at the workspace and the prefix among the segments.
func globWalkBase(workspace, pattern string) (string, []string) {
	segments := splitGlobPattern(pattern)
	base := workspace
	if filepath.IsAbs(pattern) {
		base = filepath.VolumeName(pattern) + string(filepath.Separator)
	}
	i := 0
	for ; i < len(segments); i++ {
		segment := segments[i]
		if segment == ".." {
			base = filepath.Dir(base)
			continue
		}
		if hasGlobMeta(segment) {
			break
		}
		base = filepath.Join(base, segment)
	}
	return base, segments[i:]
}

// globDoubleStar matches a pattern that has a "**" segment against the tree it
// names. A "**" segment stands for zero or more directory names, so "**/name"
// matches name in the root as well as at any depth, "a/**/b" matches b under a
// at any depth, and "**" on its own matches everything under the root.
//
// The walk reads every directory through readDirIn, so it stays inside the
// root it is given: a symlink pointing out of the workspace is refused rather
// than followed. It does not descend into symlinked directories, so a link
// cycle cannot make it recurse forever.
func (t *FilesystemTool) globDoubleStar(workspace, pattern string) ([]string, bool, error) {
	base, segments := globWalkBase(workspace, pattern)
	for _, segment := range segments {
		if segment == ".." {
			return nil, false, errors.New("invalid pattern: `..` is not allowed in a glob pattern")
		}
	}

	// resolvePath refuses a base that leaves the workspace, including one that
	// is lexically inside it but points out through a symlink; the glob
	// reports that refusal in its own words, naming the pattern.
	start, err := t.resolvePath(workspace, base)
	if err != nil {
		return nil, false, fmt.Errorf("access denied: pattern %s is outside workspace %s", pattern, workspace)
	}
	root := t.containmentRoot(workspace, start)

	// The limit is one past the cap, so the walk can tell "exactly at the
	// cap" from "there are more".
	w := &globWalker{
		root:  root,
		seen:  make(map[string]struct{}),
		limit: maxGlobResults + 1,
	}
	switch err := w.match(start, segments); {
	case err == nil:
	case errors.Is(err, errGlobLimit):
		// The cap stopped the walk; the matches found so far are the answer.
	default:
		return nil, false, fmt.Errorf("invalid pattern: %w", err)
	}

	sort.Strings(w.matches)
	capped := len(w.matches) > maxGlobResults
	if capped {
		w.matches = w.matches[:maxGlobResults]
	}
	return w.matches, capped, nil
}

// globWalker collects the paths a "**" pattern matches.
type globWalker struct {
	root    string
	matches []string
	seen    map[string]struct{}
	limit   int
}

// add records a match, and stops the walk once the limit is reached.
func (w *globWalker) add(path string) error {
	if _, ok := w.seen[path]; ok {
		return nil
	}
	w.seen[path] = struct{}{}
	w.matches = append(w.matches, path)
	if len(w.matches) >= w.limit {
		return errGlobLimit
	}
	return nil
}

// readDir lists a directory for the walk. A directory that cannot be read
// matches nothing, exactly as filepath.Glob treats an unreadable one.
func (w *globWalker) readDir(dir string) []os.DirEntry {
	entries, err := readDirIn(w.root, dir)
	if err != nil {
		return nil
	}
	return entries
}

// match matches the remaining pattern segments against what is under dir.
func (w *globWalker) match(dir string, segments []string) error {
	if len(segments) == 0 {
		return nil
	}
	if segments[0] == "**" {
		return w.matchAnyDepth(dir, segments[1:])
	}
	return w.matchName(dir, w.readDir(dir), segments)
}

// matchName matches the first segment against the entries of dir.
func (w *globWalker) matchName(dir string, entries []os.DirEntry, segments []string) error {
	for _, entry := range entries {
		matched, err := filepath.Match(segments[0], entry.Name())
		if err != nil {
			return err
		}
		if !matched {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if len(segments) == 1 {
			if err := w.add(path); err != nil {
				return err
			}
			continue
		}
		if !entry.IsDir() {
			continue
		}
		if err := w.match(path, segments[1:]); err != nil {
			return err
		}
	}
	return nil
}

// matchAnyDepth handles a "**" segment: it stands for zero or more directory
// names, so the rest of the pattern applies in dir and in every directory
// below it.
func (w *globWalker) matchAnyDepth(dir string, segments []string) error {
	entries := w.readDir(dir)
	if len(segments) == 0 {
		// "**" on its own: everything under the directory.
		return w.addUnder(dir, entries)
	}
	// "**" matches zero directories: the rest of the pattern applies here.
	if err := w.matchName(dir, entries, segments); err != nil {
		return err
	}
	// "**" matches one or more directories: the rest applies below each.
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := w.matchAnyDepth(filepath.Join(dir, entry.Name()), segments); err != nil {
			return err
		}
	}
	return nil
}

// addUnder records every entry under dir, at every depth.
func (w *globWalker) addUnder(dir string, entries []os.DirEntry) error {
	for _, entry := range entries {
		if err := w.add(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sub := filepath.Join(dir, entry.Name())
		if err := w.addUnder(sub, w.readDir(sub)); err != nil {
			return err
		}
	}
	return nil
}
