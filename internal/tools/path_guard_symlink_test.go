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
	// The error text matters: O_NOFOLLOW on the leaf refuses this too, with
	// ELOOP, so an assertion on "an error occurred" passed even with the
	// containment fix reverted and isolated nothing. Only "outside workspace"
	// shows resolveSymlinks followed the dangling link and the containment check
	// caught it.
	if !strings.Contains(res.Error.Error(), "outside workspace") {
		t.Fatalf("expected a containment error, got %v", res.Error)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Fatalf("write escaped the workspace: %s exists", victim)
	}
}

// The same containment, asserted on resolveSymlinks directly rather than
// through the write, so the unit under test cannot be confused with the
// kernel's O_NOFOLLOW backstop.
func TestResolveSymlinksFollowsDanglingLinkOutOfWorkspace(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	victim := filepath.Join(root, "outside", "pwned.txt")
	if err := os.Symlink(victim, filepath.Join(ws, "evil")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := resolveSymlinks(filepath.Join(ws, "evil"))
	if err != nil {
		t.Fatalf("resolveSymlinks: %v", err)
	}
	if got == filepath.Join(ws, "evil") {
		t.Fatalf("resolveSymlinks returned the link's own path, not its target: %s", got)
	}
	if isWithinBase(got, ws) {
		t.Fatalf("resolved path %s was reported inside the workspace %s", got, ws)
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

// --- dirfd containment (issue: O_NOFOLLOW on the leaf is not enough) ---

// O_NOFOLLOW constrains only the FINAL component of the path handed to open(2).
// Intermediate components are still resolved through symlinks by the kernel, so
// once resolvePath had blessed ws/sub/file.txt, replacing ws/sub with a symlink
// to somewhere outside redirected the write with no error at all —
// os.MkdirAll happily walked the link too. This calls the containment helpers
// directly, which is the layer that has to hold: resolvePath cannot, because by
// construction the swap happens after it returns.
func TestWriteThroughSwappedParentDirSymlinkIsRefused(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	target := filepath.Join(ws, "sub", "file.txt")

	// The swap the shell tool can perform between the check and the open.
	if err := os.Remove(filepath.Join(ws, "sub")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "sub")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := mkdirAllIn(ws, filepath.Dir(target)); err == nil {
		t.Errorf("mkdirAllIn traversed a symlinked parent")
	}
	if err := safeWriteFile(ws, target, []byte("PWNED"), 0o644); err == nil {
		t.Errorf("write traversed a symlinked parent component")
	}
	if _, err := os.Stat(filepath.Join(outside, "file.txt")); err == nil {
		t.Fatalf("write escaped the workspace: %s exists", filepath.Join(outside, "file.txt"))
	}

	// And a level deeper, where the whole tail has to be created.
	if err := mkdirAllIn(ws, filepath.Join(ws, "sub", "deep")); err == nil {
		t.Errorf("mkdirAllIn created a directory through a symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(outside, "deep")); err == nil {
		t.Fatalf("mkdir escaped the workspace")
	}
}

// The read side got no equivalent treatment at all: readFile, editFile's
// pre-read and grep all called os.ReadFile, which follows symlinks with no
// O_NOFOLLOW anywhere. Swapping a blessed name for a link to a 0600 file
// outside returned that file's contents — and for a bot whose threat model is
// SSH keys and cloud credentials, the read is the payoff.
func TestReadThroughSwappedSymlinkIsRefused(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{ws, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	secret := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(secret, []byte("BEGIN PRIVATE KEY"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	// Leaf swapped for a link out.
	blessed := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(blessed, []byte("innocent"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Remove(blessed); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(secret, blessed); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if data, err := safeReadFile(ws, blessed); err == nil {
		t.Errorf("read followed a swapped leaf symlink and returned %q", data)
	}

	// Intermediate component swapped for a link out.
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	deep := filepath.Join(ws, "sub", "id_rsa")
	if err := os.Remove(filepath.Join(ws, "sub")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "sub")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if data, err := safeReadFile(ws, deep); err == nil {
		t.Errorf("read traversed a symlinked parent and returned %q", data)
	}
}

// grep is the read escape that needs no race at all: filepath.Walk reports a
// symlink as an ordinary file (it Lstats), and the os.ReadFile that followed
// dereferenced it — so a single `ln -s ~/.ssh/id_rsa ws/notes.txt`, which the
// shell tool can create, put the key's contents in the grep output.
func TestGrepDoesNotReadThroughEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{ws, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	const secret = "AKIAPRIVATECREDENTIAL"
	if err := os.WriteFile(filepath.Join(outside, "creds"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "creds"), filepath.Join(ws, "notes.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tool := NewFilesystemTool(ws, true)
	res := tool.Execute(nil, map[string]any{"operation": "grep", "search": secret})
	if res.Error == nil && strings.Contains(res.Output, secret) {
		t.Fatalf("grep leaked a file outside the workspace: %s", res.Output)
	}

	// read_file through the same link is refused by name, before any open.
	res = tool.Execute(nil, map[string]any{"operation": "read_file", "path": "notes.txt"})
	if res.Error == nil {
		t.Fatalf("read_file followed a symlink out of the workspace: %s", res.Output)
	}
	if !strings.Contains(res.Error.Error(), "outside workspace") {
		t.Fatalf("expected a containment error, got %v", res.Error)
	}
}

// Ordinary work inside the workspace must keep working: nested directories get
// created, written, read back, edited and listed through the same helpers.
func TestContainedHelpersAllowOrdinaryWorkspaceWork(t *testing.T) {
	ws := t.TempDir()
	tool := NewFilesystemTool(ws, true)

	if res := tool.Execute(nil, map[string]any{
		"operation": "write_file", "path": "a/b/c.txt", "content": "hello",
	}); res.Error != nil {
		t.Fatalf("nested write refused: %v", res.Error)
	}
	if res := tool.Execute(nil, map[string]any{
		"operation": "read_file", "path": "a/b/c.txt",
	}); res.Error != nil || !strings.Contains(res.Output, "hello") {
		t.Fatalf("nested read failed: err=%v out=%q", res.Error, res.Output)
	}
	if res := tool.Execute(nil, map[string]any{
		"operation": "edit_file", "path": "a/b/c.txt", "search": "hello", "replace": "world",
	}); res.Error != nil {
		t.Fatalf("nested edit refused: %v", res.Error)
	}
	if res := tool.Execute(nil, map[string]any{
		"operation": "list_dir", "path": "a/b",
	}); res.Error != nil || !strings.Contains(res.Output, "c.txt") {
		t.Fatalf("list_dir failed: err=%v out=%q", res.Error, res.Output)
	}
	if res := tool.Execute(nil, map[string]any{
		"operation": "grep", "search": "world",
	}); res.Error != nil || !strings.Contains(res.Output, "world") {
		t.Fatalf("grep failed: err=%v out=%q", res.Error, res.Output)
	}
}
