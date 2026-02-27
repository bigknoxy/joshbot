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
