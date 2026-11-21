package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runGlob runs a glob operation and fails the test if the tool refused it.
func runGlob(t *testing.T, tool *FilesystemTool, pattern string) ToolResult {
	t.Helper()
	res := tool.Execute(context.Background(), map[string]any{
		"operation": "glob",
		"pattern":   pattern,
	})
	if res.Error != nil {
		t.Fatalf("glob %q: %v", pattern, res.Error)
	}
	return res
}

// globOutputPaths returns the paths a glob reported, in the order it printed
// them.
func globOutputPaths(output string) []string {
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Found ") {
			continue
		}
		paths = append(paths, line)
	}
	return paths
}

// writeGlobFile writes a file, creating the directories above it.
func writeGlobFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// A "**" segment matches the name in the root as well as at any depth. The
// pattern every model writes by default used to match nothing at all, and the
// tool said so with an empty list rather than an error (#486).
func TestGlobDoubleStarMatchesAtRootAndNested(t *testing.T) {
	ws := t.TempDir()
	writeGlobFile(t, filepath.Join(ws, "target.txt"))
	writeGlobFile(t, filepath.Join(ws, "sub", "target.txt"))
	writeGlobFile(t, filepath.Join(ws, "sub", "deep", "target.txt"))
	writeGlobFile(t, filepath.Join(ws, "other.txt"))

	tool := NewFilesystemTool(ws, true)
	want := []string{"sub/deep/target.txt", "sub/target.txt", "target.txt"}

	got := globOutputPaths(runGlob(t, tool, "**/target.txt").Output)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("**/target.txt = %v, want %v", got, want)
	}

	// The same pattern, named absolutely.
	got = globOutputPaths(runGlob(t, tool, filepath.Join(ws, "**", "target.txt")).Output)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("absolute **/target.txt = %v, want %v", got, want)
	}
}

// "**" on its own matches every entry under the root: the files and the
// directories, at every depth.
func TestGlobDoubleStarAloneMatchesEverythingUnderRoot(t *testing.T) {
	ws := t.TempDir()
	writeGlobFile(t, filepath.Join(ws, "a.txt"))
	writeGlobFile(t, filepath.Join(ws, "sub", "b.txt"))
	writeGlobFile(t, filepath.Join(ws, "sub", "deep", "c.txt"))

	tool := NewFilesystemTool(ws, true)
	got := globOutputPaths(runGlob(t, tool, "**").Output)
	want := []string{"a.txt", "sub", "sub/b.txt", "sub/deep", "sub/deep/c.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("** = %v, want %v", got, want)
	}
}

// A "**" in the middle of a pattern matches any number of directories between
// the names on either side of it.
func TestGlobDoubleStarInMiddleOfPattern(t *testing.T) {
	ws := t.TempDir()
	writeGlobFile(t, filepath.Join(ws, "sub", "target.txt"))
	writeGlobFile(t, filepath.Join(ws, "sub", "deep", "target.txt"))
	writeGlobFile(t, filepath.Join(ws, "other", "target.txt"))

	tool := NewFilesystemTool(ws, true)
	got := globOutputPaths(runGlob(t, tool, "sub/**/target.txt").Output)
	want := []string{"sub/deep/target.txt", "sub/target.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sub/**/target.txt = %v, want %v", got, want)
	}
}

// A pattern without a "**" segment is unchanged: same matches, same order,
// same text. A pattern that matches nothing still matches nothing, and two
// stars inside one name are two wildcards in a name, not a recursive one.
func TestGlobWithoutDoubleStarIsUnchanged(t *testing.T) {
	ws := t.TempDir()
	writeGlobFile(t, filepath.Join(ws, "a.txt"))
	writeGlobFile(t, filepath.Join(ws, "b.txt"))
	writeGlobFile(t, filepath.Join(ws, "sub", "c.txt"))

	tool := NewFilesystemTool(ws, true)
	cases := []struct {
		pattern string
		want    string
	}{
		{"*.txt", "Found 2 files matching *.txt:\n  a.txt\n  b.txt\n"},
		{"sub/*.txt", "Found 1 files matching sub/*.txt:\n  sub/c.txt\n"},
		{"missing*.txt", "No files match the pattern"},
		{"**not-a-name", "No files match the pattern"},
	}
	for _, tc := range cases {
		res := runGlob(t, tool, tc.pattern)
		if res.Output != tc.want {
			t.Fatalf("glob %q output = %q, want %q", tc.pattern, res.Output, tc.want)
		}
	}
}

// The recursive walk stays inside the workspace: it does not follow a symlink
// out of it, and a pattern that walks out is refused rather than answered
// with an empty list.
func TestGlobDoubleStarStaysInsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	writeGlobFile(t, filepath.Join(outside, "secret.txt"))
	writeGlobFile(t, filepath.Join(ws, "inside.txt"))
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tool := NewFilesystemTool(ws, true)
	got := globOutputPaths(runGlob(t, tool, "**").Output)
	for _, path := range got {
		if strings.Contains(path, "secret") {
			t.Fatalf("** reached outside the workspace: %v", got)
		}
	}

	for _, pattern := range []string{"../**/secret.txt", "/etc/**"} {
		res := tool.Execute(context.Background(), map[string]any{
			"operation": "glob",
			"pattern":   pattern,
		})
		if res.Error == nil {
			t.Fatalf("expected %q to be refused, got: %s", pattern, res.Output)
		}
		if !strings.Contains(res.Error.Error(), "outside workspace") {
			t.Fatalf("expected an access-denied error for %q, got: %v", pattern, res.Error)
		}
	}
}

// A "**" pattern reaches an allowed path the way a plain one does: the walk
// is rooted there instead of at the workspace.
func TestGlobDoubleStarInAllowedPath(t *testing.T) {
	ws := t.TempDir()
	allowed := t.TempDir()
	writeGlobFile(t, filepath.Join(allowed, "target.txt"))

	tool := NewFilesystemTool(ws, true, allowed)
	got := globOutputPaths(runGlob(t, tool, filepath.Join(allowed, "**", "target.txt")).Output)
	if len(got) != 1 || !strings.HasSuffix(got[0], "target.txt") {
		t.Fatalf("** in an allowed path = %v, want the one target.txt", got)
	}
}

// The walk's matches are symlink-resolved, and on macOS every temp path is
// (/var -> /private/var), so a resolved match held against the lexical
// allowed entry filtered every match away and the tool answered with an
// empty list. This reproduces that without macOS: the allowed path is
// registered through a symlink, the pattern walks the real one.
func TestGlobDoubleStarInAnAllowedPathReachedThroughASymlink(t *testing.T) {
	ws := t.TempDir()
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeGlobFile(t, filepath.Join(real, "target.txt"))

	tool := NewFilesystemTool(ws, true, alias)
	got := globOutputPaths(runGlob(t, tool, filepath.Join(real, "**", "target.txt")).Output)
	if len(got) != 1 || !strings.HasSuffix(got[0], "target.txt") {
		t.Fatalf("** in an allowed path reached through a symlink = %v, want the one target.txt", got)
	}

	// The same match through the alias itself, the way a caller that only
	// knows the registered path would ask.
	got = globOutputPaths(runGlob(t, tool, filepath.Join(alias, "**", "target.txt")).Output)
	if len(got) != 1 || !strings.HasSuffix(got[0], "target.txt") {
		t.Fatalf("** through the alias = %v, want the one target.txt", got)
	}
}

// With the restriction off the walk is still rooted at the workspace for a
// relative pattern, and nothing is refused.
func TestGlobDoubleStarWithoutRestriction(t *testing.T) {
	ws := t.TempDir()
	writeGlobFile(t, filepath.Join(ws, "sub", "target.txt"))

	tool := NewFilesystemTool(ws, false)
	got := globOutputPaths(runGlob(t, tool, "**/target.txt").Output)
	want := []string{"sub/target.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("**/target.txt = %v, want %v", got, want)
	}
}

// The result cap holds under a "**" pattern: a walk that reaches a whole
// workspace does not fill the turn, and the model is told the list is partial.
func TestGlobDoubleStarResultCap(t *testing.T) {
	ws := t.TempDir()
	for i := 0; i < maxGlobResults+25; i++ {
		writeGlobFile(t, filepath.Join(ws, fmt.Sprintf("file%03d.txt", i)))
	}

	tool := NewFilesystemTool(ws, true)
	res := runGlob(t, tool, "**")
	paths := globOutputPaths(res.Output)
	if len(paths) != maxGlobResults {
		t.Fatalf("** returned %d paths, want the cap of %d", len(paths), maxGlobResults)
	}
	if !strings.Contains(res.Output, "result cap") {
		t.Fatalf("expected the capped result to say it is partial, got: %s", res.Output)
	}
}
