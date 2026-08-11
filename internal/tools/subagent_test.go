package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/subagent"
)

type mockSubagentRunner struct {
	responses map[string]string
	delay     time.Duration
}

func (m *mockSubagentRunner) SimpleRun(ctx context.Context, prompt string) (string, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	for prefix, resp := range m.responses {
		if strings.Contains(prompt, prefix) {
			return resp, nil
		}
	}
	return "default response", nil
}

func TestParallelSubagentTool_Name(t *testing.T) {
	tool := NewParallelSubagentTool(&mockSubagentRunner{})
	if tool.Name() != "parallel_subagent" {
		t.Fatalf("expected 'parallel_subagent', got '%s'", tool.Name())
	}
}

func TestParallelSubagentTool_Description(t *testing.T) {
	tool := NewParallelSubagentTool(&mockSubagentRunner{})
	if tool.Description() == "" {
		t.Fatal("description should not be empty")
	}
}

func TestParallelSubagentTool_Parameters(t *testing.T) {
	tool := NewParallelSubagentTool(&mockSubagentRunner{})
	params := tool.Parameters()
	if len(params) != 2 {
		t.Fatalf("expected 2 parameters, got %d", len(params))
	}
	names := make(map[string]bool)
	for _, p := range params {
		names[p.Name] = true
	}
	if !names["tasks"] {
		t.Fatal("expected 'tasks' parameter")
	}
	if !names["concurrency"] {
		t.Fatal("expected 'concurrency' parameter")
	}
}

func TestParallelSubagentTool_NilRunner(t *testing.T) {
	tool := NewParallelSubagentTool(nil)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{"prompt": "hello"}},
	})
	if result.Error == nil {
		t.Fatal("expected error for nil runner")
	}
}

func TestParallelSubagentTool_MissingTasks(t *testing.T) {
	tool := NewParallelSubagentTool(&mockSubagentRunner{})
	result := tool.Execute(context.Background(), map[string]any{})
	if result.Error == nil {
		t.Fatal("expected error for missing tasks")
	}
}

func TestParallelSubagentTool_EmptyTasks(t *testing.T) {
	tool := NewParallelSubagentTool(&mockSubagentRunner{})
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{},
	})
	if result.Error == nil {
		t.Fatal("expected error for empty tasks")
	}
}

func TestParallelSubagentTool_SingleTask(t *testing.T) {
	runner := &mockSubagentRunner{responses: map[string]string{"hello": "hello from subagent"}}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{"prompt": "say hello"}},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "hello from subagent") {
		t.Fatalf("expected output to contain 'hello from subagent', got: %s", result.Output)
	}
}

func TestParallelSubagentTool_MultipleTasks(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"task1": "result from task 1",
			"task2": "result from task 2",
			"task3": "result from task 3",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"prompt": "do task1"},
			map[string]any{"prompt": "do task2"},
			map[string]any{"prompt": "do task3"},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "result from task 1") {
		t.Fatal("missing task 1 result")
	}
	if !strings.Contains(result.Output, "result from task 2") {
		t.Fatal("missing task 2 result")
	}
	if !strings.Contains(result.Output, "result from task 3") {
		t.Fatal("missing task 3 result")
	}
}

func TestParallelSubagentTool_WithDescriptions(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"analyze": "analysis result",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{
			"prompt":      "analyze the code",
			"description": "Code analysis",
		}},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "Code analysis") {
		t.Fatalf("expected output to contain task description, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "analysis result") {
		t.Fatalf("expected output to contain analysis result, got: %s", result.Output)
	}
}

func TestParallelSubagentTool_PartialFailure(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"good": "good result",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"prompt": "do good"},
			map[string]any{"prompt": "do bad"},
		},
	})
	if result.Error != nil {
		t.Fatalf("expected no error for partial failure, got: %v", result.Error)
	}
	if !strings.Contains(result.Output, "good result") {
		t.Fatal("should contain successful result")
	}
}

func TestParallelSubagentTool_ConcurrencyLimit(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"do a": "result a",
			"do b": "result b",
			"do c": "result c",
			"do d": "result d",
			"do e": "result e",
		},
		delay: 50 * time.Millisecond,
	}
	tool := NewParallelSubagentTool(runner)
	start := time.Now()
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"prompt": "do a"},
			map[string]any{"prompt": "do b"},
			map[string]any{"prompt": "do c"},
			map[string]any{"prompt": "do d"},
			map[string]any{"prompt": "do e"},
		},
		"concurrency": 3,
	})
	elapsed := time.Since(start)
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("expected concurrency limit to reduce total time, took %v", elapsed)
	}
	if !strings.Contains(result.Output, "result a") || !strings.Contains(result.Output, "Task 5") {
		t.Fatal("should contain all results")
	}
}

func TestParallelSubagentTool_WithContext(t *testing.T) {
	runner := &mockSubagentRunner{delay: 200 * time.Millisecond}
	tool := NewParallelSubagentTool(runner)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := tool.Execute(ctx, map[string]any{
		"tasks": []any{map[string]any{"prompt": "slow task"}},
	})
	if !strings.Contains(result.Output, "context deadline exceeded") {
		t.Fatalf("expected output to mention context error, got: %s", result.Output)
	}
}

func TestGenerateSchema_ParallelSubagent(t *testing.T) {
	params := []Parameter{
		{Name: "test", Type: ParamString, Description: "a test", Required: true},
	}
	schema := GenerateSchema(params)
	if !strings.Contains(schema, "test") {
		t.Fatal("schema should contain parameter name")
	}
}

// Ensure SubagentRunner is compatible with the real Runner
func TestSubagentRunnerInterface_Compatible(t *testing.T) {
	var _ SubagentRunner = (*subagent.Runner)(nil)
}
