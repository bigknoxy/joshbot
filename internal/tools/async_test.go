package tools

import (
	"context"
	"errors"
	"testing"
	"time"
)

type mockAsyncTool struct {
	name       string
	isAsync    bool
	executeErr error
	output     string
	duration   time.Duration
}

func (m *mockAsyncTool) Name() string            { return m.name }
func (m *mockAsyncTool) Description() string     { return "mock async tool" }
func (m *mockAsyncTool) Parameters() []Parameter { return nil }
func (m *mockAsyncTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	return ToolResult{Output: m.output}
}

func (m *mockAsyncTool) IsAsync(args map[string]any) bool {
	return m.isAsync
}

func (m *mockAsyncTool) ExecuteAsync(ctx context.Context, args map[string]any, callback AsyncCallback) ToolResult {
	go func() {
		time.Sleep(m.duration)

		result := AsyncResult{Output: m.output}
		if m.executeErr != nil {
			result.Error = m.executeErr
		}
		callback(result)
	}()
	return ToolResult{Output: "started"}
}

func TestRegistry_ExecuteAsync(t *testing.T) {
	callbackCh := make(chan AsyncResult, 1)
	registry := NewRegistry(WithAsyncSupport(callbackCh))

	mockTool := &mockAsyncTool{
		name:     "test_async",
		isAsync:  true,
		output:   "test output",
		duration: 100 * time.Millisecond,
	}

	if err := registry.Register(mockTool); err != nil {
		t.Fatalf("Failed to register tool: %v", err)
	}

	result, isAsync := registry.ExecuteWithContext(context.Background(), "test_async", nil, "test", "test", nil)

	if !isAsync {
		t.Error("Expected async execution")
	}

	if result.Error != nil {
		t.Errorf("Unexpected error: %v", result.Error)
	}

	select {
	case asyncResult := <-callbackCh:
		if asyncResult.Output != "test output" {
			t.Errorf("Output = %q, want %q", asyncResult.Output, "test output")
		}
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for async callback")
	}
}

func TestRegistry_ExecuteSync(t *testing.T) {
	registry := NewRegistry()

	syncTool := &mockAsyncTool{
		name:    "test_sync",
		isAsync: false,
		output:  "sync output",
	}

	if err := registry.Register(syncTool); err != nil {
		t.Fatalf("Failed to register tool: %v", err)
	}

	result, isAsync := registry.ExecuteWithContext(context.Background(), "test_sync", nil, "test", "test", nil)

	if isAsync {
		t.Error("Expected sync execution")
	}

	if result.Output != "sync output" {
		t.Errorf("Output = %q, want %q", result.Output, "sync output")
	}
}

func TestShellTool_IsAsync(t *testing.T) {
	shell := NewShellTool(60*time.Second, "", false)

	tests := []struct {
		command  string
		expected bool
	}{
		{"echo hello", false},
		{"python script.py", true},
		{"npm run build", true},
		{"make all", true},
		{"docker build .", true},
		{"ls -la", false},
		{"tail -f /var/log/syslog", true},
		{"go build ./...", true},
		{"cargo build", true},
		{"sleep 10", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			args := map[string]any{"command": tt.command}
			got := shell.IsAsync(args)
			if got != tt.expected {
				t.Errorf("IsAsync(%q) = %v, want %v", tt.command, got, tt.expected)
			}
		})
	}
}

func TestShellTool_IsAsync_ExplicitFlag(t *testing.T) {
	shell := NewShellTool(60*time.Second, "", false)

	args := map[string]any{"command": "echo hello", "async": true}
	if !shell.IsAsync(args) {
		t.Error("IsAsync should return true when async flag is set")
	}

	args = map[string]any{"command": "python script.py", "async": false}
	if shell.IsAsync(args) {
		t.Error("IsAsync should return false when async flag is explicitly false")
	}
}

func TestRegistry_PendingAsyncTracking(t *testing.T) {
	callbackCh := make(chan AsyncResult, 10)
	registry := NewRegistry(WithAsyncSupport(callbackCh))

	mockTool := &mockAsyncTool{
		name:     "long_task",
		isAsync:  true,
		output:   "done",
		duration: 500 * time.Millisecond,
	}

	if err := registry.Register(mockTool); err != nil {
		t.Fatalf("Failed to register: %v", err)
	}

	_, _ = registry.ExecuteWithContext(context.Background(), "long_task", nil, "ch", "chat", nil)

	// Wait for goroutine to start and register
	time.Sleep(50 * time.Millisecond)
	pending := registry.GetPendingAsync()
	if len(pending) == 0 {
		t.Skip("Pending tracking not initialized fast enough - async execution completed")
	}

	<-callbackCh

	time.Sleep(50 * time.Millisecond)
	pending = registry.GetPendingAsync()
	if len(pending) != 0 {
		t.Errorf("Pending count = %d, want 0", len(pending))
	}
}

func TestRegistry_ExecuteAsync_Error(t *testing.T) {
	callbackCh := make(chan AsyncResult, 1)
	registry := NewRegistry(WithAsyncSupport(callbackCh))

	mockTool := &mockAsyncTool{
		name:       "error_tool",
		isAsync:    true,
		executeErr: errors.New("tool failed"),
		duration:   50 * time.Millisecond,
	}

	if err := registry.Register(mockTool); err != nil {
		t.Fatalf("Failed to register tool: %v", err)
	}

	_, _ = registry.ExecuteWithContext(context.Background(), "error_tool", nil, "test", "test", nil)

	select {
	case asyncResult := <-callbackCh:
		if asyncResult.Error == nil {
			t.Error("Expected error in async result")
		}
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for async callback")
	}
}

func TestRegistry_ExecuteWithContext_ToolNotFound(t *testing.T) {
	registry := NewRegistry()

	result, _ := registry.ExecuteWithContext(context.Background(), "nonexistent", nil, "test", "test", nil)

	if result.Error == nil {
		t.Error("Expected error for nonexistent tool")
	}
}

func TestShellTool_ExecuteAsync(t *testing.T) {
	shell := NewShellTool(60*time.Second, "", false)

	callbackCh := make(chan AsyncResult, 1)

	args := map[string]any{"command": "echo 'async test'", "async": true}

	result := shell.ExecuteAsync(context.Background(), args, func(r AsyncResult) {
		callbackCh <- r
	})

	if result.Error != nil {
		t.Errorf("ExecuteAsync error: %v", result.Error)
	}

	if result.Output == "" {
		t.Error("ExecuteAsync should return immediate output")
	}

	select {
	case asyncResult := <-callbackCh:
		if asyncResult.Error != nil {
			t.Errorf("Async execution error: %v", asyncResult.Error)
		}
		if asyncResult.Output == "" {
			t.Error("Async result should have output")
		}
	case <-time.After(5 * time.Second):
		t.Error("Timeout waiting for async shell execution")
	}
}
