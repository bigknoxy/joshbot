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
