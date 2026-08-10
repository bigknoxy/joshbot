package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dangling symlink used to escape the workspace: EvalSymlinks reports ENOENT
// for a link whose target does not exist, exactly as it does for a plain
// missing file, so resolveSymlinks resolved the parent and re-appended the link
// name verbatim. The lexical path is inside the workspace, so the containment
// check passed — and the write that followed was performed by the kernel
// through the link, landing outside. The agent can create the link itself with
// the shell tool, so this is reachable, not theoretical.
func TestWriteThroughDanglingSymlinkIsRefused(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{ws, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	victim := filepath.Join(outside, "pwned.txt")
	if err := os.Symlink(victim, filepath.Join(ws, "evil")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Fatalf("target must not exist yet, got err=%v", err)
	}

	tool := NewFilesystemTool(ws, true)
	res := tool.Execute(nil, map[string]any{
		"operation": "write_file",
		"path":      "evil",
		"content":   "pwned",
	})

	if res.Error == nil {
		t.Fatalf("write through a dangling symlink was permitted: %s", res.Output)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Fatalf("write escaped the workspace: %s exists", victim)
	}
}

// The same escape one level deeper: the link is an intermediate component of a
// path whose tail does not exist either.
func TestWriteBelowDanglingSymlinkDirIsRefused(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.Symlink(outside, filepath.Join(ws, "evildir")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tool := NewFilesystemTool(ws, true)
	res := tool.Execute(nil, map[string]any{
		"operation": "write_file",
		"path":      "evildir/pwned.txt",
		"content":   "pwned",
	})

	if res.Error == nil {
		t.Fatalf("write below a dangling symlinked dir was permitted: %s", res.Output)
	}
	if !strings.Contains(res.Error.Error(), "outside workspace") {
		t.Fatalf("expected a containment error, got %v", res.Error)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); err == nil {
		t.Fatalf("write escaped the workspace")
	}
}

// A symlink that stays inside the workspace is still ordinary, allowed work.
func TestWriteThroughInWorkspaceSymlinkStillWorks(t *testing.T) {
	ws := t.TempDir()
	if err := os.Symlink(filepath.Join(ws, "real.txt"), filepath.Join(ws, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tool := NewFilesystemTool(ws, true)
	res := tool.Execute(nil, map[string]any{
		"operation": "write_file",
		"path":      "link",
		"content":   "ok",
	})
	if res.Error != nil {
		t.Fatalf("in-workspace symlink write was refused: %v", res.Error)
	}
	data, err := os.ReadFile(filepath.Join(ws, "real.txt"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("expected content at the link target, got %q err=%v", data, err)
	}
}
