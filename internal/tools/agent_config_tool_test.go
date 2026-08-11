package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubagentConfigTool_Name(t *testing.T) {
	mgr, _ := NewSubagentConfigManager(t.TempDir())
	tool := NewSubagentConfigTool(mgr)
	if tool.Name() != "subagent_config" {
		t.Fatalf("expected 'subagent_config', got '%s'", tool.Name())
	}
}

func TestSubagentConfigTool_Parameters(t *testing.T) {
	mgr, _ := NewSubagentConfigManager(t.TempDir())
	tool := NewSubagentConfigTool(mgr)
	params := tool.Parameters()
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(params))
	}
}

func TestSubagentConfigTool_ListEmpty(t *testing.T) {
	mgr, _ := NewSubagentConfigManager(t.TempDir())
	tool := NewSubagentConfigTool(mgr)
	result := tool.Execute(context.Background(), map[string]any{
		"operation": "list",
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "No subagent configs found") {
		t.Fatalf("expected 'No subagent configs found', got: %s", result.Output)
	}
}

func TestSubagentConfigTool_DiscoverAndList(t *testing.T) {
	dir := t.TempDir()
	mgr, _ := NewSubagentConfigManager(dir)

	// Create a config file
	cfg := &SubagentConfig{
		Name:        "test-agent",
		Description: "A test agent",
		Model:       "test-model",
		Temperature: 0.5,
		MaxTokens:   300,
	}
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("save error: %v", err)
	}

	// Re-discover
	tool := NewSubagentConfigTool(mgr)
	result := tool.Execute(context.Background(), map[string]any{
		"operation": "discover",
	})
	if result.Error != nil {
		t.Fatalf("discover error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "Discovered 1 subagent config(s)") {
		t.Fatalf("expected discovery message, got: %s", result.Output)
	}

	// List
	result = tool.Execute(context.Background(), map[string]any{
		"operation": "list",
	})
	if result.Error != nil {
		t.Fatalf("list error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "test-agent") {
		t.Fatalf("expected 'test-agent' in list, got: %s", result.Output)
	}
}

func TestSubagentConfigTool_Get(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "code-reviewer.yaml")
	if err := os.WriteFile(cfgPath, []byte("name: code-reviewer\ndescription: Go code reviewer\nmodel: gpt-4\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr, _ := NewSubagentConfigManager(dir)
	if err := mgr.Discover(); err != nil {
		t.Fatal(err)
	}

	tool := NewSubagentConfigTool(mgr)
	result := tool.Execute(context.Background(), map[string]any{
		"operation": "get",
		"name":      "code-reviewer",
	})
	if result.Error != nil {
		t.Fatalf("get error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "code-reviewer") {
		t.Fatalf("expected 'code-reviewer' in output, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Go code reviewer") {
		t.Fatal("expected description in output")
	}
	if !strings.Contains(result.Output, "gpt-4") {
		t.Fatal("expected model in output")
	}
}

func TestSubagentConfigTool_GetNotFound(t *testing.T) {
	mgr, _ := NewSubagentConfigManager(t.TempDir())
	tool := NewSubagentConfigTool(mgr)
	result := tool.Execute(context.Background(), map[string]any{
		"operation": "get",
		"name":      "nonexistent",
	})
	if result.Error == nil {
		t.Fatal("expected error for not found")
	}
	if !strings.Contains(result.Error.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", result.Error)
	}
}

func TestSubagentConfigTool_UnknownOperation(t *testing.T) {
	mgr, _ := NewSubagentConfigManager(t.TempDir())
	tool := NewSubagentConfigTool(mgr)
	result := tool.Execute(context.Background(), map[string]any{
		"operation": "unknown",
	})
	if result.Error == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestSubagentConfigTool_MissingOperation(t *testing.T) {
	mgr, _ := NewSubagentConfigManager(t.TempDir())
	tool := NewSubagentConfigTool(mgr)
	result := tool.Execute(context.Background(), map[string]any{})
	if result.Error == nil {
		t.Fatal("expected error for missing operation")
	}
}

func TestSubagentConfigTool_NilManager(t *testing.T) {
	tool := NewSubagentConfigTool(nil)
	result := tool.Execute(context.Background(), map[string]any{
		"operation": "list",
	})
	if result.Error == nil {
		t.Fatal("expected error for nil manager")
	}
}
