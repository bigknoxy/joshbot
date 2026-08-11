package tools

import (
	"context"
	"strings"
	"testing"
)

// TestParallelSubagent_Integration_Header verifies that the output contains the
// "Parallel Subagent Results" header line from the Execute output format.
func TestParallelSubagent_Integration_Header(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"ping": "pong",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{"prompt": "ping"}},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "Parallel Subagent Results") {
		t.Fatalf("expected output to contain 'Parallel Subagent Results' header, got:\n%s", result.Output)
	}
}

// TestParallelSubagent_Integration_TwoTasks verifies that 2 simulated
// file-reading tasks are executed and their results appear in the output.
func TestParallelSubagent_Integration_TwoTasks(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"read etc/hostname":   "my-host\n",
			"read etc/os-release": "NAME=\"Linux\"\nVERSION=\"1\"\n",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"prompt":      "read etc/hostname",
				"description": "Hostname file",
			},
			map[string]any{
				"prompt":      "read etc/os-release",
				"description": "OS release file",
			},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if !strings.Contains(result.Output, "my-host") {
		t.Fatal("expected output to contain 'my-host' from /etc/hostname content")
	}
	if !strings.Contains(result.Output, "Linux") {
		t.Fatal("expected output to contain 'Linux' from /etc/os-release content")
	}
}

// TestParallelSubagent_Integration_DescriptionsAsLabels verifies that the
// description field is used as the markdown section header label.
func TestParallelSubagent_Integration_DescriptionsAsLabels(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"audit": "security audit complete",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{
			"prompt":      "audit the system",
			"description": "Security Audit",
		}},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "### Security Audit") {
		t.Fatalf("expected section header '### Security Audit', got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "security audit complete") {
		t.Fatalf("expected result content 'security audit complete', got:\n%s", result.Output)
	}
}

// TestParallelSubagent_Integration_FallbackToTaskN verifies that when no
// description field is provided, the label falls back to "Task N" (1-indexed).
func TestParallelSubagent_Integration_FallbackToTaskN(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"greet": "hello there",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{"prompt": "greet"}},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "### Task 1") {
		t.Fatalf("expected fallback label '### Task 1', got:\n%s", result.Output)
	}

	// Also verify with multiple tasks to ensure numbering is correct
	runner2 := &mockSubagentRunner{
		responses: map[string]string{
			"first":  "result one",
			"second": "result two",
		},
	}
	tool2 := NewParallelSubagentTool(runner2)
	result2 := tool2.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"prompt": "first"},
			map[string]any{"prompt": "second"},
		},
	})
	if result2.Error != nil {
		t.Fatalf("unexpected error: %v", result2.Error)
	}
	if !strings.Contains(result2.Output, "### Task 1") {
		t.Fatal("expected fallback label '### Task 1' for first task")
	}
	if !strings.Contains(result2.Output, "### Task 2") {
		t.Fatal("expected fallback label '### Task 2' for second task")
	}
}

// TestParallelSubagent_Integration_MixedLabels verifies that tasks with and
// without descriptions produce the correct labels when mixed together.
func TestParallelSubagent_Integration_MixedLabels(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"analyze logs": "log analysis output",
			"check config": "config check output",
			"run tests":    "test run output",
			"validate":     "validation output",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"prompt":      "analyze logs",
				"description": "Log Analysis",
			},
			map[string]any{"prompt": "check config"},
			map[string]any{
				"prompt":      "run tests",
				"description": "Test Suite",
			},
			map[string]any{"prompt": "validate"},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// Tasks with descriptions should use them as labels
	if !strings.Contains(result.Output, "### Log Analysis") {
		t.Fatal("expected '### Log Analysis' label")
	}
	if !strings.Contains(result.Output, "### Test Suite") {
		t.Fatal("expected '### Test Suite' label")
	}
	// Tasks without descriptions should fall back to "Task N"
	if !strings.Contains(result.Output, "### Task 2") {
		t.Fatal("expected fallback '### Task 2' for second task (no description)")
	}
	if !strings.Contains(result.Output, "### Task 4") {
		t.Fatal("expected fallback '### Task 4' for fourth task (no description)")
	}
	// All results should be present
	if !strings.Contains(result.Output, "log analysis output") {
		t.Fatal("expected 'log analysis output'")
	}
	if !strings.Contains(result.Output, "config check output") {
		t.Fatal("expected 'config check output'")
	}
	if !strings.Contains(result.Output, "test run output") {
		t.Fatal("expected 'test run output'")
	}
	if !strings.Contains(result.Output, "validation output") {
		t.Fatal("expected 'validation output'")
	}
}

// TestParallelSubagent_Integration_SingleQuoteJSON tests the parseTasksArg
// single-quote fallback path. LLMs sometimes emit JSON with single quotes
// instead of proper JSON double quotes; parseTasksArg handles this by
// replacing ' with " and retrying the unmarshal.
func TestParallelSubagent_Integration_SingleQuoteJSON(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"hello": "world result",
		},
	}
	tool := NewParallelSubagentTool(runner)

	// Pass tasks as a single-quoted JSON string — a common LLM serialization quirk.
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": "[{'prompt': 'say hello'}]",
	})
	if result.Error != nil {
		t.Fatalf("unexpected error with single-quoted JSON: %v", result.Error)
	}
	if !strings.Contains(result.Output, "world result") {
		t.Fatalf("expected output to contain 'world result', got:\n%s", result.Output)
	}
	// Should fall back to "Task 1" since no description was given
	if !strings.Contains(result.Output, "### Task 1") {
		t.Fatalf("expected fallback label '### Task 1', got:\n%s", result.Output)
	}
}

// TestParallelSubagent_Integration_SingleQuoteJSONWithDescriptions verifies
// that single-quoted JSON with description fields is parsed correctly.
func TestParallelSubagent_Integration_SingleQuoteJSONWithDescriptions(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"readme":  "README content",
			"license": "MIT License",
		},
	}
	tool := NewParallelSubagentTool(runner)

	result := tool.Execute(context.Background(), map[string]any{
		"tasks": "[{'prompt': 'get readme', 'description': 'README file'}, {'prompt': 'get license', 'description': 'License file'}]",
	})
	if result.Error != nil {
		t.Fatalf("unexpected error with single-quoted JSON: %v", result.Error)
	}
	if !strings.Contains(result.Output, "### README file") {
		t.Fatalf("expected label '### README file', got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "### License file") {
		t.Fatalf("expected label '### License file', got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "README content") {
		t.Fatal("expected 'README content' in output")
	}
	if !strings.Contains(result.Output, "MIT License") {
		t.Fatal("expected 'MIT License' in output")
	}
}

// TestParallelSubagent_Integration_ProperJSON verifies that standard JSON
// strings (with double quotes) also work via parseTasksArg.
func TestParallelSubagent_Integration_ProperJSON(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"status": "all good",
		},
	}
	tool := NewParallelSubagentTool(runner)

	result := tool.Execute(context.Background(), map[string]any{
		"tasks": `[{"prompt": "check status"}]`,
	})
	if result.Error != nil {
		t.Fatalf("unexpected error with proper JSON: %v", result.Error)
	}
	if !strings.Contains(result.Output, "all good") {
		t.Fatalf("expected 'all good', got:\n%s", result.Output)
	}
}

// TestParallelSubagent_Integration_SummaryLine verifies that the output
// footer contains a summary line showing success count.
func TestParallelSubagent_Integration_SummaryLine(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"task1": "result1",
			"task2": "result2",
		},
	}
	tool := NewParallelSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"prompt": "task1"},
			map[string]any{"prompt": "task2"},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "2/2 tasks succeeded") {
		t.Fatalf("expected summary '2/2 tasks succeeded', got:\n%s", result.Output)
	}
	// The footer should include a horizontal rule
	if !strings.Contains(result.Output, "---") {
		t.Fatal("expected horizontal rule '---' in output")
	}
}
