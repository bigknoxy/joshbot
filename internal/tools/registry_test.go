package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// mockTool is a simple tool implementation for testing.
type mockTool struct {
	name        string
	description string
	parameters  []Parameter
	executeFn   func(ctx interface{}, args map[string]any) ToolResult
}

func (m *mockTool) Name() string            { return m.name }
func (m *mockTool) Description() string     { return m.description }
func (m *mockTool) Parameters() []Parameter { return m.parameters }
func (m *mockTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if m.executeFn != nil {
		return m.executeFn(ctx, args)
	}
	return ToolResult{Output: "executed"}
}

// mockLogger is a simple logger for testing.
type mockLogger struct {
	infos []string
}

func (m *mockLogger) Info(msg string, args ...interface{}) {
	m.infos = append(m.infos, msg)
}

func TestNewRegistry(t *testing.T) {
	registry := NewRegistry()

	if registry == nil {
		t.Fatal("expected non-nil registry")
	}

	if registry.Count() != 0 {
		t.Errorf("expected 0 tools, got %d", registry.Count())
	}
}

func TestRegistryRegister(t *testing.T) {
	registry := NewRegistry()

	tool := &mockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  []Parameter{},
	}

	err := registry.Register(tool)
	if err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	if registry.Count() != 1 {
		t.Errorf("expected 1 tool, got %d", registry.Count())
	}
}

func TestRegistryRegisterNilTool(t *testing.T) {
	registry := NewRegistry()

	err := registry.Register(nil)
	if err == nil {
		t.Error("expected error for nil tool")
	}
}

func TestRegistryRegisterEmptyName(t *testing.T) {
	registry := NewRegistry()

	tool := &mockTool{
		name:        "",
		description: "A test tool",
		parameters:  []Parameter{},
	}

	err := registry.Register(tool)
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestRegistryRegisterDuplicate(t *testing.T) {
	registry := NewRegistry()

	tool1 := &mockTool{
		name:        "duplicate_tool",
		description: "First tool",
		parameters:  []Parameter{},
	}

	tool2 := &mockTool{
		name:        "duplicate_tool",
		description: "Second tool",
		parameters:  []Parameter{},
	}

	err := registry.Register(tool1)
	if err != nil {
		t.Fatalf("failed to register first tool: %v", err)
	}

	err = registry.Register(tool2)
	if err == nil {
		t.Error("expected error for duplicate registration")
	}
}

func TestRegistryUnregister(t *testing.T) {
	registry := NewRegistry()

	tool := &mockTool{
		name:        "unregister_test",
		description: "Test tool",
		parameters:  []Parameter{},
	}

	registry.Register(tool)
	if registry.Count() != 1 {
		t.Fatalf("expected 1 tool, got %d", registry.Count())
	}

	registry.Unregister("unregister_test")
	if registry.Count() != 0 {
		t.Errorf("expected 0 tools, got %d", registry.Count())
	}
}

func TestRegistryUnregisterNonExistent(t *testing.T) {
	registry := NewRegistry()

	// Should not panic
	registry.Unregister("non_existent")
}

func TestRegistryGet(t *testing.T) {
	registry := NewRegistry()

	tool := &mockTool{
		name:        "get_test",
		description: "Test tool",
		parameters:  []Parameter{},
	}

	registry.Register(tool)

	retrieved, ok := registry.Get("get_test")
	if !ok {
		t.Error("expected to find tool")
	}

	if retrieved.Name() != "get_test" {
		t.Errorf("expected tool name 'get_test', got %q", retrieved.Name())
	}
}

func TestRegistryGetNonExistent(t *testing.T) {
	registry := NewRegistry()

	_, ok := registry.Get("non_existent")
	if ok {
		t.Error("expected not to find tool")
	}
}

func TestRegistryList(t *testing.T) {
	registry := NewRegistry()

	registry.Register(&mockTool{name: "tool1", description: "Tool 1", parameters: []Parameter{}})
	registry.Register(&mockTool{name: "tool2", description: "Tool 2", parameters: []Parameter{}})
	registry.Register(&mockTool{name: "tool3", description: "Tool 3", parameters: []Parameter{}})

	names := registry.List()
	if len(names) != 3 {
		t.Errorf("expected 3 tool names, got %d", len(names))
	}
}

func TestRegistryCount(t *testing.T) {
	registry := NewRegistry()

	if registry.Count() != 0 {
		t.Errorf("expected 0, got %d", registry.Count())
	}

	registry.Register(&mockTool{name: "tool1", description: "Tool 1", parameters: []Parameter{}})
	if registry.Count() != 1 {
		t.Errorf("expected 1, got %d", registry.Count())
	}

	registry.Register(&mockTool{name: "tool2", description: "Tool 2", parameters: []Parameter{}})
	if registry.Count() != 2 {
		t.Errorf("expected 2, got %d", registry.Count())
	}

	registry.Unregister("tool1")
	if registry.Count() != 1 {
		t.Errorf("expected 1, got %d", registry.Count())
	}
}

func TestRegistryExecute(t *testing.T) {
	registry := NewRegistry()

	tool := &mockTool{
		name:        "execute_test",
		description: "Test tool",
		parameters:  []Parameter{},
		executeFn: func(ctx interface{}, args map[string]any) ToolResult {
			return ToolResult{Output: "executed successfully"}
		},
	}

	registry.Register(tool)

	result, err := registry.Execute(context.Background(), "execute_test", map[string]any{})
	if err != nil {
		t.Fatalf("failed to execute tool: %v", err)
	}

	if result != "executed successfully" {
		t.Errorf("expected 'executed successfully', got %q", result)
	}
}

func TestRegistryExecuteNonExistent(t *testing.T) {
	registry := NewRegistry()

	_, err := registry.Execute(context.Background(), "non_existent", map[string]any{})
	if err == nil {
		t.Error("expected error for non-existent tool")
	}
}

func TestRegistryExecuteWithArgs(t *testing.T) {
	registry := NewRegistry()

	var receivedArgs map[string]any
	tool := &mockTool{
		name:        "args_test",
		description: "Test tool",
		parameters:  []Parameter{},
		executeFn: func(ctx interface{}, args map[string]any) ToolResult {
			receivedArgs = args
			return ToolResult{Output: "done"}
		},
	}

	registry.Register(tool)

	testArgs := map[string]any{
		"command": "echo hello",
		"timeout": 30,
	}

	_, err := registry.Execute(context.Background(), "args_test", testArgs)
	if err != nil {
		t.Fatalf("failed to execute tool: %v", err)
	}

	if receivedArgs["command"] != "echo hello" {
		t.Errorf("expected command 'echo hello', got %v", receivedArgs["command"])
	}
}

func TestRegistryExecuteWithError(t *testing.T) {
	registry := NewRegistry()

	tool := &mockTool{
		name:        "error_test",
		description: "Test tool",
		parameters:  []Parameter{},
		executeFn: func(ctx interface{}, args map[string]any) ToolResult {
			return ToolResult{Error: ErrPermissionDenied}
		},
	}

	registry.Register(tool)

	_, err := registry.Execute(context.Background(), "error_test", map[string]any{})
	if err == nil {
		t.Error("expected error from tool execution")
	}
}

func TestRegistryGetSchemas(t *testing.T) {
	registry := NewRegistry()

	tool := &mockTool{
		name:        "schema_test",
		description: "A test tool with parameters",
		parameters: []Parameter{
			{
				Name:        "command",
				Type:        ParamString,
				Description: "The command to execute",
				Required:    true,
			},
			{
				Name:        "timeout",
				Type:        ParamInteger,
				Description: "Timeout in seconds",
				Required:    false,
				Default:     60,
			},
		},
	}

	registry.Register(tool)

	schemas := registry.GetSchemas()
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema, got %d", len(schemas))
	}

	if schemas[0].Function.Name != "schema_test" {
		t.Errorf("expected name 'schema_test', got %q", schemas[0].Function.Name)
	}

	if schemas[0].Function.Description != "A test tool with parameters" {
		t.Errorf("unexpected description: %q", schemas[0].Function.Description)
	}
}

func TestRegistryGetSchemasMultiple(t *testing.T) {
	registry := NewRegistry()

	registry.Register(&mockTool{name: "tool1", description: "Tool 1", parameters: []Parameter{}})
	registry.Register(&mockTool{name: "tool2", description: "Tool 2", parameters: []Parameter{}})
	registry.Register(&mockTool{name: "tool3", description: "Tool 3", parameters: []Parameter{}})

	schemas := registry.GetSchemas()
	if len(schemas) != 3 {
		t.Errorf("expected 3 schemas, got %d", len(schemas))
	}
}

func TestRegistryGetToolDocs(t *testing.T) {
	registry := NewRegistry()

	tool := &mockTool{
		name:        "doc_test",
		description: "A test tool for documentation",
		parameters: []Parameter{
			{
				Name:        "arg1",
				Type:        ParamString,
				Description: "First argument",
				Required:    true,
			},
			{
				Name:        "arg2",
				Type:        ParamBoolean,
				Description: "Second argument",
				Required:    false,
			},
		},
	}

	registry.Register(tool)

	docs := registry.GetToolDocs()

	if len(docs) == 0 {
		t.Error("expected non-empty documentation")
	}

	// Check for expected content
	expectedContents := []string{
		"doc_test",
		"A test tool for documentation",
		"arg1",
		"arg2",
	}

	for _, expected := range expectedContents {
		if !contains(docs, expected) {
			t.Errorf("expected documentation to contain %q", expected)
		}
	}
}

func TestRegistryGetToolDocsEmpty(t *testing.T) {
	registry := NewRegistry()

	docs := registry.GetToolDocs()

	if !contains(docs, "Available Tools") {
		t.Error("expected header in empty registry docs")
	}
}

func TestRegistryWithLogger(t *testing.T) {
	logger := &mockLogger{}
	registry := NewRegistry(WithLogger(logger))

	registry.Register(&mockTool{name: "logged_tool", description: "Test", parameters: []Parameter{}})

	if len(logger.infos) != 1 {
		t.Errorf("expected 1 log entry, got %d", len(logger.infos))
	}
}

func TestDefaultRegistry(t *testing.T) {
	registry := DefaultRegistry()

	if registry == nil {
		t.Fatal("expected non-nil registry")
	}

	// Default registry starts empty
	if registry.Count() != 0 {
		t.Errorf("expected 0 tools, got %d", registry.Count())
	}
}

func TestRegistryWithDefaults(t *testing.T) {
	registry := RegistryWithDefaults(
		"/tmp/test-workspace",
		true, // restrict to workspace
		30,   // exec timeout
		10,   // web timeout
		nil,  // no message sender
	)

	// Should have filesystem, shell, and web tools
	if registry.Count() < 3 {
		t.Errorf("expected at least 3 tools, got %d", registry.Count())
	}

	// Check for expected tools
	_, ok := registry.Get("filesystem")
	if !ok {
		t.Error("expected filesystem tool")
	}

	_, ok = registry.Get("shell")
	if !ok {
		t.Error("expected shell tool")
	}

	_, ok = registry.Get("web")
	if !ok {
		t.Error("expected web tool")
	}
}

func TestToolResultString(t *testing.T) {
	// Test with output
	result := ToolResult{Output: "test output"}
	if result.String() != "test output" {
		t.Errorf("expected 'test output', got %q", result.String())
	}

	// Test with error
	resultWithError := ToolResult{Error: ErrPermissionDenied}
	if resultWithError.String() == "" {
		t.Error("expected non-empty string for error result")
	}
}

func TestGenerateSchema(t *testing.T) {
	params := []Parameter{
		{
			Name:        "command",
			Type:        ParamString,
			Description: "The command to run",
			Required:    true,
		},
		{
			Name:        "timeout",
			Type:        ParamInteger,
			Description: "Timeout in seconds",
			Required:    false,
			Default:     60,
		},
		{
			Name:        "enabled",
			Type:        ParamBoolean,
			Description: "Enable feature",
			Required:    false,
		},
	}

	schemaStr := GenerateSchema(params)

	// Verify it's valid JSON
	var schema map[string]any
	err := json.Unmarshal([]byte(schemaStr), &schema)
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	// Check type
	if schema["type"] != "object" {
		t.Errorf("expected type 'object', got %v", schema["type"])
	}

	// Check properties
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}

	if _, ok := props["command"]; !ok {
		t.Error("expected 'command' property")
	}

	// Check required
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("expected required array")
	}

	if len(required) != 1 || required[0] != "command" {
		t.Errorf("expected ['command'], got %v", required)
	}
}

func TestParameterTypeConstants(t *testing.T) {
	tests := []struct {
		got  ParameterType
		want string
	}{
		{ParamString, "string"},
		{ParamInteger, "integer"},
		{ParamNumber, "number"},
		{ParamBoolean, "boolean"},
		{ParamArray, "array"},
		{ParamObject, "object"},
	}

	for _, tt := range tests {
		if string(tt.got) != tt.want {
			t.Errorf("expected %q, got %q", tt.want, tt.got)
		}
	}
}

func TestToolToProviderTool(t *testing.T) {
	tool := &mockTool{
		name:        "provider_test",
		description: "Test tool for provider conversion",
		parameters: []Parameter{
			{
				Name:        "arg1",
				Type:        ParamString,
				Description: "First argument",
				Required:    true,
				Enum:        []string{"a", "b", "c"},
			},
		},
	}

	// This tests the internal conversion function
	result := toolToProviderTool(tool)

	if result.Type != "function" {
		t.Errorf("expected type 'function', got %q", result.Type)
	}

	if result.Function.Name != "provider_test" {
		t.Errorf("expected name 'provider_test', got %q", result.Function.Name)
	}

	if result.Function.Description != "Test tool for provider conversion" {
		t.Errorf("unexpected description: %q", result.Function.Description)
	}
}

// Integration-like tests

func TestRegistryRoundTrip(t *testing.T) {
	registry := NewRegistry()

	// Register tools
	registry.Register(&mockTool{name: "tool1", description: "Tool 1", parameters: []Parameter{}})
	registry.Register(&mockTool{name: "tool2", description: "Tool 2", parameters: []Parameter{}})

	// Get schemas
	schemas := registry.GetSchemas()
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas, got %d", len(schemas))
	}

	// List tools
	names := registry.List()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}

	// Execute
	_, err := registry.Execute(context.Background(), "tool1", map[string]any{})
	if err != nil {
		t.Fatalf("failed to execute: %v", err)
	}

	// Unregister
	registry.Unregister("tool1")
	if registry.Count() != 1 {
		t.Errorf("expected 1 tool, got %d", registry.Count())
	}
}

func TestConcurrentRegistryAccess(t *testing.T) {
	registry := NewRegistry()

	// Note: This test verifies that concurrent access doesn't panic
	// The registry uses RWMutex, so it should be safe
	done := make(chan bool)

	go func() {
		for i := 0; i < 100; i++ {
			registry.Register(&mockTool{
				name:        "concurrent_tool_" + string(rune(i)),
				description: "Test",
				parameters:  []Parameter{},
			})
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			registry.List()
			registry.GetSchemas()
		}
		done <- true
	}()

	<-done
	<-done
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Error variables for testing
var (
	ErrPermissionDenied = &testError{"permission denied"}
)

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
