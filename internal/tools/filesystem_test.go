package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemToolGlobWithoutPath(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewFilesystemTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"operation": "glob",
		"pattern":   "*.txt",
	})
	if res.Error != nil {
		t.Fatalf("glob should succeed without path: %v", res.Error)
	}
	if !strings.Contains(res.Output, "a.txt") {
		t.Fatalf("expected output to contain a.txt, got: %s", res.Output)
	}
}

func TestFilesystemToolGrepWithoutPath(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "notes.md"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewFilesystemTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"operation": "grep",
		"search":    "beta",
	})
	if res.Error != nil {
		t.Fatalf("grep should succeed without path: %v", res.Error)
	}
	if !strings.Contains(res.Output, "notes.md") {
		t.Fatalf("expected grep output to contain notes.md, got: %s", res.Output)
	}
}

func TestFilesystemToolGrepWithRelativePath(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewFilesystemTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"operation": "grep",
		"path":      "sub",
		"search":    "needle",
	})
	if res.Error != nil {
		t.Fatalf("grep with path should succeed: %v", res.Error)
	}
	if !strings.Contains(res.Output, "sub/file.txt") {
		t.Fatalf("expected grep output to contain sub/file.txt, got: %s", res.Output)
	}
}

func TestFilesystemToolGlobRestrictsAbsoluteOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	tool := NewFilesystemTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"operation": "glob",
		"pattern":   "/etc/*",
	})
	if res.Error == nil {
		t.Fatal("expected restriction error for absolute pattern outside workspace")
	}
}

func TestFilesystemToolAllowedPaths(t *testing.T) {
	ws := t.TempDir()
	// Create a temp directory outside workspace to allow
	allowedDir := t.TempDir()

	tool := NewFilesystemTool(ws, true, allowedDir)

	// Test reading a file in allowed path
	testFile := filepath.Join(allowedDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("allowed content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	res := tool.Execute(context.Background(), map[string]any{
		"operation": "read_file",
		"path":      testFile,
	})
	if res.Error != nil {
		t.Fatalf("expected read from allowed path to succeed, got: %v", res.Error)
	}
	if !strings.Contains(res.Output, "allowed content") {
		t.Fatalf("expected output to contain allowed content, got: %s", res.Output)
	}

	// Test reading a file NOT in allowed path should fail
	res = tool.Execute(context.Background(), map[string]any{
		"operation": "read_file",
		"path":      "/etc/passwd",
	})
	if res.Error == nil {
		t.Fatal("expected access denied for path not in allowed list")
	}
}

func TestFilesystemToolAllowedPathsGlob(t *testing.T) {
	ws := t.TempDir()
	allowedDir := t.TempDir()

	tool := NewFilesystemTool(ws, true, allowedDir)

	// Create test file
	if err := os.WriteFile(filepath.Join(allowedDir, "data.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	res := tool.Execute(context.Background(), map[string]any{
		"operation": "glob",
		"pattern":   filepath.Join(allowedDir, "*.json"),
	})
	if res.Error != nil {
		t.Fatalf("expected glob in allowed path to succeed, got: %v", res.Error)
	}
	if !strings.Contains(res.Output, "data.json") {
		t.Fatalf("expected output to contain data.json, got: %s", res.Output)
	}
}

// TestFilesystemToolSymlinkEscape covers #153: a symlink that is lexically
// inside the workspace but points outside it must be rejected. Without
// symlink resolution, ws/link/passwd cleans to a path under the workspace and
// slips past isWithinBase, reading /etc through the link.
func TestFilesystemToolSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tool := NewFilesystemTool(ws, true)

	// Read through the escaping symlink must be denied.
	res := tool.Execute(context.Background(), map[string]any{
		"operation": "read_file",
		"path":      "link/secret.txt",
	})
	if res.Error == nil {
		t.Fatalf("expected read through escaping symlink to be denied, got output: %s", res.Output)
	}
	if !strings.Contains(res.Error.Error(), "outside workspace") {
		t.Fatalf("expected access-denied error, got: %v", res.Error)
	}

	// Write through the escaping symlink must be denied too.
	res = tool.Execute(context.Background(), map[string]any{
		"operation": "write_file",
		"path":      "link/planted.txt",
		"content":   "payload",
	})
	if res.Error == nil {
		t.Fatalf("expected write through escaping symlink to be denied")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Fatalf("write escaped the workspace: file was planted outside")
	}
}

// A create of a not-yet-existing file inside the workspace must still succeed:
// EvalSymlinks fails on the missing leaf, so resolvePath resolves the parent.
func TestFilesystemToolCreateNewFileInsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	tool := NewFilesystemTool(ws, true)

	res := tool.Execute(context.Background(), map[string]any{
		"operation": "write_file",
		"path":      "sub/dir/new.txt",
		"content":   "hello",
	})
	if res.Error != nil {
		t.Fatalf("create inside workspace should succeed, got: %v", res.Error)
	}
	data, err := os.ReadFile(filepath.Join(ws, "sub", "dir", "new.txt"))
	if err != nil {
		t.Fatalf("file was not created: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: %q", data)
	}
}
