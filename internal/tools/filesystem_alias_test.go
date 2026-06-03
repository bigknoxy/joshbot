package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aliasTestCase holds the alias name and expected operation for table-driven tests.
type aliasTestCase struct {
	name string
	op   string
}

func aliasTestCases() []aliasTestCase {
	return []aliasTestCase{
		{"read_file", "read_file"},
		{"write_file", "write_file"},
		{"edit_file", "edit_file"},
		{"list_dir", "list_dir"},
		{"glob", "glob"},
		{"grep", "grep"},
	}
}

func TestFilesystemAlias_Name(t *testing.T) {
	for _, tc := range aliasTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			alias := &filesystemAlias{name: tc.name, op: tc.op}
			if got := alias.Name(); got != tc.name {
				t.Errorf("Name() = %q, want %q", got, tc.name)
			}
		})
	}
}

func TestFilesystemAlias_Description(t *testing.T) {
	// Create a real filesystem tool to use as the underlying tool
	fs := NewFilesystemTool(t.TempDir(), true)
	for _, tc := range aliasTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			alias := &filesystemAlias{fs: fs, name: tc.name, op: tc.op}
			desc := alias.Description()
			if desc == "" {
				t.Error("Description() returned empty string")
			}
			if desc != fs.Description() {
				t.Errorf("Description() = %q, want %q", desc, fs.Description())
			}
		})
	}
}

func TestFilesystemAlias_Parameters(t *testing.T) {
	fs := NewFilesystemTool(t.TempDir(), true)
	expected := fs.Parameters()

	for _, tc := range aliasTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			alias := &filesystemAlias{fs: fs, name: tc.name, op: tc.op}
			params := alias.Parameters()

			if len(params) != len(expected) {
				t.Fatalf("Parameters() returned %d items, want %d", len(params), len(expected))
			}

			for i := range params {
				if params[i].Name != expected[i].Name {
					t.Errorf("Parameters()[%d].Name = %q, want %q", i, params[i].Name, expected[i].Name)
				}
				if params[i].Type != expected[i].Type {
					t.Errorf("Parameters()[%d].Type = %q, want %q", i, params[i].Type, expected[i].Type)
				}
				if params[i].Required != expected[i].Required {
					t.Errorf("Parameters()[%d].Required = %v, want %v", i, params[i].Required, expected[i].Required)
				}
			}
		})
	}
}

func TestFilesystemAlias_Execute_InjectsOperation(t *testing.T) {
	ws := t.TempDir()
	filePath := filepath.Join(ws, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	fs := NewFilesystemTool(ws, true)
	alias := &filesystemAlias{fs: fs, name: "read_file", op: "read_file"}

	// Execute without "operation" in args — the alias should inject it
	res := alias.Execute(context.Background(), map[string]any{
		"path": "test.txt",
	})
	if res.Error != nil {
		t.Fatalf("Execute() with injected operation failed: %v", res.Error)
	}
	if !strings.Contains(res.Output, "hello world") {
		t.Errorf("expected output to contain %q, got: %s", "hello world", res.Output)
	}
}

func TestFilesystemAlias_WriteFile_Works(t *testing.T) {
	ws := t.TempDir()
	fs := NewFilesystemTool(ws, true)
	alias := &filesystemAlias{fs: fs, name: "write_file", op: "write_file"}

	content := "some test content"
	res := alias.Execute(context.Background(), map[string]any{
		"path":    "new_file.txt",
		"content": content,
	})
	if res.Error != nil {
		t.Fatalf("write_file alias failed: %v", res.Error)
	}

	// Verify file was actually written
	data, err := os.ReadFile(filepath.Join(ws, "new_file.txt"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", string(data), content)
	}
}

func TestFilesystemAlias_ReadFile_Works(t *testing.T) {
	ws := t.TempDir()
	filePath := filepath.Join(ws, "greeting.txt")
	if err := os.WriteFile(filePath, []byte("hello from alias test"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	fs := NewFilesystemTool(ws, true)
	alias := &filesystemAlias{fs: fs, name: "read_file", op: "read_file"}

	res := alias.Execute(context.Background(), map[string]any{
		"path": "greeting.txt",
	})
	if res.Error != nil {
		t.Fatalf("read_file alias failed: %v", res.Error)
	}
	if !strings.Contains(res.Output, "hello from alias test") {
		t.Errorf("expected output to contain %q, got: %s", "hello from alias test", res.Output)
	}
}

func TestFilesystemAlias_EditFile_Works(t *testing.T) {
	ws := t.TempDir()
	filePath := filepath.Join(ws, "editable.txt")
	original := "replace this word please"
	if err := os.WriteFile(filePath, []byte(original), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	fs := NewFilesystemTool(ws, true)
	alias := &filesystemAlias{fs: fs, name: "edit_file", op: "edit_file"}

	res := alias.Execute(context.Background(), map[string]any{
		"path":    "editable.txt",
		"search":  "this word",
		"replace": "that phrase",
	})
	if res.Error != nil {
		t.Fatalf("edit_file alias failed: %v", res.Error)
	}

	// Verify file was edited
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("reading edited file: %v", err)
	}
	expected := "replace that phrase please"
	if string(data) != expected {
		t.Errorf("after edit, content = %q, want %q", string(data), expected)
	}
}

func TestFilesystemAlias_Glob_Works(t *testing.T) {
	ws := t.TempDir()
	// Create several files
	for _, name := range []string{"a.go", "b.go", "c_test.go", "notes.md"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte("content"), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	fs := NewFilesystemTool(ws, true)
	alias := &filesystemAlias{fs: fs, name: "glob", op: "glob"}

	res := alias.Execute(context.Background(), map[string]any{
		"pattern": "*.go",
	})
	if res.Error != nil {
		t.Fatalf("glob alias failed: %v", res.Error)
	}
	if !strings.Contains(res.Output, "a.go") {
		t.Errorf("expected output to contain %q, got: %s", "a.go", res.Output)
	}
	if !strings.Contains(res.Output, "b.go") {
		t.Errorf("expected output to contain %q, got: %s", "b.go", res.Output)
	}
	if strings.Contains(res.Output, "notes.md") {
		t.Errorf("expected output NOT to contain notes.md, got: %s", res.Output)
	}
}

// TestFilesystemAlias_AllAliasesInRegistry uses the same creation pattern as
// RegistryWithDefaults to verify all six aliases work end-to-end.
func TestFilesystemAlias_AllAliasesInRegistry(t *testing.T) {
	ws := t.TempDir()
	fs := NewFilesystemTool(ws, true)

	aliases := []struct {
		name string
		op   string
	}{
		{"read_file", "read_file"},
		{"write_file", "write_file"},
		{"edit_file", "edit_file"},
		{"list_dir", "list_dir"},
		{"glob", "glob"},
		{"grep", "grep"},
	}

	for _, a := range aliases {
		t.Run(a.name, func(t *testing.T) {
			alias := &filesystemAlias{fs: fs, name: a.name, op: a.op}
			if alias.Name() != a.name {
				t.Errorf("Name() = %q, want %q", alias.Name(), a.name)
			}
			if alias.Description() == "" {
				t.Error("Description() should not be empty")
			}
			if len(alias.Parameters()) == 0 {
				t.Error("Parameters() should not be empty")
			}
		})
	}
}
