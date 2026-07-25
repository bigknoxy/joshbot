package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/skills"
)

// --- SkillRegistryTool ---

func TestSkillRegistryTool_Name(t *testing.T) {
	tool := NewSkillRegistryTool(nil)
	if got := tool.Name(); got != "skill_registry" {
		t.Errorf("Name() = %q, want 'skill_registry'", got)
	}
}

func TestSkillRegistryTool_Description(t *testing.T) {
	tool := NewSkillRegistryTool(nil)
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() returned empty string")
	}
	if !strings.Contains(desc, "List, create, or delete") {
		t.Errorf("Description() = %q, expected to contain 'List, create, or delete'", desc)
	}
}

func TestSkillRegistryTool_Parameters(t *testing.T) {
	tool := NewSkillRegistryTool(nil)
	params := tool.Parameters()
	if len(params) != 3 {
		t.Fatalf("Parameters() returned %d params, want 3", len(params))
	}
	if params[0].Name != "action" {
		t.Errorf("params[0].Name = %q, want 'action'", params[0].Name)
	}
	if !params[0].Required {
		t.Error("params[0].Required should be true")
	}
	if len(params[0].Enum) != 3 {
		t.Errorf("params[0].Enum has %d values, want 3", len(params[0].Enum))
	}
}

func TestSkillRegistryTool_Execute_UnknownAction(t *testing.T) {
	tool := NewSkillRegistryTool(nil)
	result := tool.Execute(nil, map[string]any{
		"action": "invalid",
	})
	if result.Error == nil {
		t.Error("expected error for unknown action")
	}
}

func TestSkillRegistryTool_Execute_List(t *testing.T) {
	loader := &skills.Loader{}
	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{
		"action": "list",
	})
	// Should not error, just return empty list
	if result.Error != nil {
		t.Logf("list returned error (expected if no skills dir): %v", result.Error)
	}
}

// --- webAlias ---

func TestWebAlias_Name(t *testing.T) {
	alias := &webAlias{name: "web_search"}
	if got := alias.Name(); got != "web_search" {
		t.Errorf("Name() = %q, want 'web_search'", got)
	}
}

func TestWebAlias_Description_AllNames(t *testing.T) {
	names := []string{"web_search", "web_fetch", "web_code", "web_company", "web_research"}
	for _, name := range names {
		alias := &webAlias{name: name}
		desc := alias.Description()
		if desc == "" {
			t.Errorf("Description() for %q returned empty", name)
		}
	}
}

func TestWebAlias_Description_Default(t *testing.T) {
	alias := &webAlias{name: "unknown"}
	desc := alias.Description()
	// Default falls through to web.Description() which may be empty or non-empty
	// Just verify it doesn't panic
	_ = desc
}

// --- Parameter.Parameters ---

func TestParameter_Parameters_WithEnum(t *testing.T) {
	p := Parameter{
		Name:        "test_param",
		Type:        ParamString,
		Description: "A test parameter",
		Required:    true,
		Enum:        []string{"a", "b", "c"},
	}
	result := p.Parameters()
	if result["type"] != "object" {
		t.Errorf("type = %v, want 'object'", result["type"])
	}
	props, ok := result["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties is not a map")
	}
	prop, ok := props["test_param"].(map[string]any)
	if !ok {
		t.Fatal("test_param property not found")
	}
	if prop["type"] != "string" {
		t.Errorf("prop type = %v, want 'string'", prop["type"])
	}
	if prop["description"] != "A test parameter" {
		t.Errorf("prop description = %v, want 'A test parameter'", prop["description"])
	}
	if _, ok := prop["enum"]; !ok {
		t.Error("enum not found in property")
	}
	if _, ok := result["required"]; !ok {
		t.Error("required not found in result")
	}
}

func TestParameter_Parameters_WithoutEnum(t *testing.T) {
	p := Parameter{
		Name:        "simple",
		Type:        ParamString,
		Description: "Simple param",
		Required:    false,
	}
	result := p.Parameters()
	props := result["properties"].(map[string]any)
	prop := props["simple"].(map[string]any)
	if _, ok := prop["enum"]; ok {
		t.Error("enum should not be present")
	}
	if _, ok := result["required"]; ok {
		t.Error("required should not be present")
	}
}

func TestParameter_Parameters_WithDefault(t *testing.T) {
	p := Parameter{
		Name:        "with_default",
		Type:        ParamString,
		Description: "Has default",
		Default:     "default_value",
	}
	result := p.Parameters()
	props := result["properties"].(map[string]any)
	prop := props["with_default"].(map[string]any)
	if prop["default"] != "default_value" {
		t.Errorf("default = %v, want 'default_value'", prop["default"])
	}
}

func TestParameter_Parameters_NoDefault(t *testing.T) {
	p := Parameter{
		Name:        "no_default",
		Type:        ParamString,
		Description: "No default",
	}
	result := p.Parameters()
	props := result["properties"].(map[string]any)
	prop := props["no_default"].(map[string]any)
	if _, ok := prop["default"]; ok {
		t.Error("default should not be present")
	}
}

// --- FilesystemTool listDir ---

func TestFilesystemTool_ListDir_Existing(t *testing.T) {
	tool := NewFilesystemTool(".", false)
	result := tool.Execute(nil, map[string]any{
		"operation": "list_dir",
		"path":      ".",
	})
	if result.Error != nil {
		t.Fatalf("listDir error = %v", result.Error)
	}
	if result.Output == "" {
		t.Error("listDir returned empty output")
	}
	if !strings.Contains(result.Output, "Contents of") {
		t.Errorf("output = %q, expected to contain 'Contents of'", result.Output)
	}
}

func TestFilesystemTool_ListDir_NonExistent(t *testing.T) {
	tool := NewFilesystemTool(".", false)
	result := tool.Execute(nil, map[string]any{
		"operation": "list_dir",
		"path":      "/nonexistent/path/that/does/not/exist",
	})
	if result.Error == nil {
		t.Error("expected error for non-existent directory")
	}
}

// --- ShellTool Description and Parameters ---

func TestShellTool_Description(t *testing.T) {
	tool := NewShellTool(30*time.Second, ".", false)
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() returned empty string")
	}
}

func TestShellTool_Parameters(t *testing.T) {
	tool := NewShellTool(30*time.Second, ".", false)
	params := tool.Parameters()
	if len(params) == 0 {
		t.Error("Parameters() returned empty slice")
	}
	// Find the command parameter
	found := false
	for _, p := range params {
		if p.Name == "command" {
			found = true
			if !p.Required {
				t.Error("command parameter should be required")
			}
		}
	}
	if !found {
		t.Error("command parameter not found")
	}
}

// --- Registry async methods ---

func TestRegistry_CancelAsync_NoAsync(t *testing.T) {
	reg := NewRegistry()
	// Should not panic even without async support
	reg.CancelAsync("nonexistent")
}

func TestRegistry_SetAsyncCallback_NoAsync(t *testing.T) {
	reg := NewRegistry()
	// Should not panic even without async support
	reg.SetAsyncCallback(nil)
}

func TestRegistry_GetAsyncCallbackChannel_NoAsync(t *testing.T) {
	reg := NewRegistry()
	// Should return nil without async support
	ch := reg.GetAsyncCallbackChannel()
	if ch != nil {
		t.Errorf("expected nil channel, got %v", ch)
	}
}

// --- WebTool alias methods ---

func TestWebTool_NewWebToolFromConfig(t *testing.T) {
	// Test that NewWebToolFromConfig doesn't panic
	tool := NewWebToolFromConfig(WebToolConfig{})
	if tool == nil {
		t.Fatal("NewWebToolFromConfig returned nil")
	}
}

func TestWebTool_AliasName(t *testing.T) {
	tool := NewWebTool(30*time.Second, "")
	alias := &webAlias{web: tool, name: "web_search"}
	if got := alias.Name(); got != "web_search" {
		t.Errorf("Name() = %q, want 'web_search'", got)
	}
}

func TestWebTool_AliasParameters(t *testing.T) {
	tool := NewWebTool(30*time.Second, "")
	alias := &webAlias{web: tool, name: "web_fetch"}
	params := alias.Parameters()
	if len(params) == 0 {
		t.Error("Parameters() returned empty slice")
	}
}
