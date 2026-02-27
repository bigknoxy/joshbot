package tools

import (
	"context"
	"testing"
	"time"
)

func TestShellToolRejectsWorkingDirOutsideWorkspaceByTraversal(t *testing.T) {
	ws := t.TempDir()
	tool := NewShellTool(2*time.Second, ws, true)

	res := tool.Execute(context.Background(), map[string]any{
		"command":     "pwd",
		"working_dir": "../outside",
	})
	if res.Error == nil {
		t.Fatal("expected error for traversal outside workspace")
	}
}

func TestShellToolRejectsAbsoluteWorkingDirOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	tool := NewShellTool(2*time.Second, ws, true)

	res := tool.Execute(context.Background(), map[string]any{
		"command":     "pwd",
		"working_dir": "/tmp",
	})
	if res.Error == nil {
		t.Fatal("expected error for absolute path outside workspace")
	}
}

func TestShellToolAllowList(t *testing.T) {
	ws := t.TempDir()
	// Only allow "ls" and "cat"
	tool := NewShellTool(2*time.Second, ws, true, "ls", "cat")

	// Test allowed command
	res := tool.Execute(context.Background(), map[string]any{
		"command": "ls -la",
	})
	if res.Error != nil {
		t.Fatalf("expected ls to be allowed, got: %v", res.Error)
	}

	// Test allowed exact match
	res = tool.Execute(context.Background(), map[string]any{
		"command": "cat file.txt",
	})
	if res.Error != nil {
		t.Fatalf("expected cat to be allowed, got: %v", res.Error)
	}

	// Test disallowed command
	res = tool.Execute(context.Background(), map[string]any{
		"command": "echo hello",
	})
	if res.Error == nil {
		t.Fatal("expected echo to be denied")
	}
}

func TestShellToolDenyListStillAppliesWithAllowList(t *testing.T) {
	ws := t.TempDir()
	// Allow all commands but still deny dangerous ones
	tool := NewShellTool(2*time.Second, ws, false)

	// Test deny list still blocks
	res := tool.Execute(context.Background(), map[string]any{
		"command": "rm -rf /",
	})
	if res.Error == nil {
		t.Fatal("expected rm -rf / to be denied")
	}
}
