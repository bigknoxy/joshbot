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
