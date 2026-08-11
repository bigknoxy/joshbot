package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// failCallRunner fails on the Nth call (0-indexed) to simulate step failures.
type failCallRunner struct {
	inner   SubagentRunner
	failIdx int
	count   int
}

func (r *failCallRunner) SimpleRun(ctx context.Context, prompt string) (string, error) {
	idx := r.count
	r.count++
	if idx == r.failIdx {
		return "", fmt.Errorf("simulated failure on call %d", idx)
	}
	return r.inner.SimpleRun(ctx, prompt)
}

func TestChainTool_Name(t *testing.T) {
	tool := NewChainExecutionTool(&mockSubagentRunner{})
	if tool.Name() != "chain_execution" {
		t.Fatalf("expected 'chain_execution', got '%s'", tool.Name())
	}
}

func TestChainTool_Description(t *testing.T) {
	tool := NewChainExecutionTool(&mockSubagentRunner{})
	if tool.Description() == "" {
		t.Fatal("description should not be empty")
	}
}

func TestChainTool_Parameters(t *testing.T) {
	tool := NewChainExecutionTool(&mockSubagentRunner{})
	params := tool.Parameters()
	if len(params) != 2 {
		t.Fatalf("expected 2 parameters, got %d", len(params))
	}
	names := make(map[string]bool)
	for _, p := range params {
		names[p.Name] = true
	}
	if !names["steps"] {
		t.Fatal("expected 'steps' parameter")
	}
	if !names["context"] {
		t.Fatal("expected 'context' parameter")
	}
}

func TestChainTool_NilRunner(t *testing.T) {
	tool := NewChainExecutionTool(nil)
	result := tool.Execute(context.Background(), map[string]any{
		"steps": []any{map[string]any{"prompt": "hello"}},
	})
	if result.Error == nil {
		t.Fatal("expected error for nil runner")
	}
}

func TestChainTool_EmptySteps(t *testing.T) {
	tool := NewChainExecutionTool(&mockSubagentRunner{})
	result := tool.Execute(context.Background(), map[string]any{
		"steps": []any{},
	})
	if result.Error == nil {
		t.Fatal("expected error for empty steps")
	}
}

func TestChainTool_SingleStep(t *testing.T) {
	runner := &mockSubagentRunner{
		responses: map[string]string{"greet": "HELLO_FROM_SUBAGENT"},
	}
	tool := NewChainExecutionTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"steps": []any{map[string]any{"prompt": "greet the user"}},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "HELLO_FROM_SUBAGENT") {
		t.Fatalf("expected output to contain 'HELLO_FROM_SUBAGENT', got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Chain Execution Results (1 steps)") {
		t.Fatal("expected step count in header")
	}
	if !strings.Contains(result.Output, "1/1 steps succeeded") {
		t.Fatal("expected success count in footer")
	}
}

func TestChainTool_TwoSteps(t *testing.T) {
	// Using distinct mock response keys that won't appear in accumulated context.
	// The mock's case-sensitive Contains check means uppercase response content
	// won't accidentally match lowercase mock keys from other steps.
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"greet":    "RESULT_GREET",
			"farewell": "RESULT_FAREWELL",
		},
	}
	tool := NewChainExecutionTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"steps": []any{
			map[string]any{"prompt": "greet the user"},
			map[string]any{"prompt": "say farewell"},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "RESULT_GREET") {
		t.Fatal("missing step 1 result")
	}
	if !strings.Contains(result.Output, "RESULT_FAREWELL") {
		t.Fatal("missing step 2 result")
	}
	if !strings.Contains(result.Output, "2/2 steps succeeded") {
		t.Fatal("expected all steps succeeded")
	}
}

func TestChainTool_WithInitialContext(t *testing.T) {
	// The mock matches on prompt substrings. The initial context is prepended
	// to the first step's prompt via "Context: {initial}\n\nTask: {prompt}".
	// By placing a unique keyword only in the context, we verify it was passed.
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"background_info": "RESULT_WITH_CONTEXT",
		},
	}
	tool := NewChainExecutionTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"steps":   []any{map[string]any{"prompt": "do something"}},
		"context": "background_info",
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "RESULT_WITH_CONTEXT") {
		t.Fatalf("expected context to be passed to subagent, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "**Initial Context:**") {
		t.Fatal("expected Initial Context section in output")
	}
	// Verify the initial context value appears (truncated if >200 chars).
	if !strings.Contains(result.Output, "background_info") {
		t.Fatal("expected initial context value in report")
	}
}

func TestChainTool_TemplateSubstitution(t *testing.T) {
	// Step 1 has name="result" so its output is stored as variable {{result}}.
	// Step 2's prompt contains {{result}} which gets replaced with step 1's output.
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"analyze":            "CODE_ANALYSIS_DONE",
			"CODE_ANALYSIS_DONE": "CONFIRMED_ANALYSIS",
		},
	}
	tool := NewChainExecutionTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"steps": []any{
			map[string]any{"prompt": "analyze the code", "name": "result"},
			map[string]any{"prompt": "Double check: {{result}}"},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "CODE_ANALYSIS_DONE") {
		t.Fatal("missing step 1 output")
	}
	// Step 2's prompt becomes "Double check: CODE_ANALYSIS_DONE" after substitution.
	// The mock matches "CODE_ANALYSIS_DONE" in the prompt and returns "CONFIRMED_ANALYSIS".
	if !strings.Contains(result.Output, "CONFIRMED_ANALYSIS") {
		t.Fatal("missing step 2 output — template substitution may not have worked")
	}
}

func TestChainTool_StepFailure(t *testing.T) {
	// First step fails, second should still execute but report partial failure.
	mock := &mockSubagentRunner{
		responses: map[string]string{
			"goodbye": "RESULT_GOODBYE",
		},
	}
	runner := &failCallRunner{inner: mock, failIdx: 0}
	tool := NewChainExecutionTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"steps": []any{
			map[string]any{"prompt": "say hello"},
			map[string]any{"prompt": "say goodbye"},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// Step 1 should show an error.
	if !strings.Contains(result.Output, "simulated failure") {
		t.Fatal("expected step 1 failure message")
	}
	// Step 2 should still execute despite step 1 failure.
	if !strings.Contains(result.Output, "RESULT_GOODBYE") {
		t.Fatal("expected step 2 to still execute despite step 1 failure")
	}
	// Footer should report partial completion.
	if !strings.Contains(result.Output, "1/2 steps succeeded") {
		t.Fatal("expected 1/2 success rate in footer")
	}
}

func TestChainTool_NotAllStepsSucceeded(t *testing.T) {
	// Three steps where the middle one fails. Verify the correct mix in the footer.
	mock := &mockSubagentRunner{
		responses: map[string]string{
			"alpha": "RESULT_ALPHA",
			"gamma": "RESULT_GAMMA",
		},
	}
	runner := &failCallRunner{inner: mock, failIdx: 1}
	tool := NewChainExecutionTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"steps": []any{
			map[string]any{"prompt": "task_alpha"},
			map[string]any{"prompt": "task_beta"},
			map[string]any{"prompt": "task_gamma"},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "RESULT_ALPHA") {
		t.Fatal("missing step 1 result")
	}
	if !strings.Contains(result.Output, "simulated failure") {
		t.Fatal("expected step 2 failure message")
	}
	if !strings.Contains(result.Output, "RESULT_GAMMA") {
		t.Fatal("expected step 3 to execute")
	}
	if !strings.Contains(result.Output, "2/3 steps succeeded") {
		t.Fatal("expected 2/3 success rate in footer")
	}
}

func TestChainTool_TemplateCollision(t *testing.T) {
	// Variable names "a" and "ab" — "ab" is longer so must be replaced first
	// to prevent {{a}} from matching inside {{ab}}.
	vars := map[string]string{
		"a":  "X",
		"ab": "Y",
	}
	prompt := "{{a}} and {{ab}}"
	result := applyTemplates(prompt, vars)
	if result != "X and Y" {
		t.Fatalf("expected 'X and Y', got '%s'", result)
	}
}

func TestChainTool_TemplateNoMatch(t *testing.T) {
	// Unmatched {{templates}} should be left as-is.
	vars := map[string]string{
		"known": "value",
	}
	prompt := "{{known}} and {{unknown}}"
	result := applyTemplates(prompt, vars)
	if result != "value and {{unknown}}" {
		t.Fatalf("expected 'value and {{unknown}}', got '%s'", result)
	}
}

func TestChainTool_parseStepsArg_JSONString(t *testing.T) {
	steps, err := parseStepsArg(`[{"prompt":"hello"},{"prompt":"world"}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
}

func TestChainTool_parseStepsArg_SingleQuotes(t *testing.T) {
	// LLMs often output single-quoted JSON-like strings.
	steps, err := parseStepsArg(`[{'prompt':'hello'}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
}

func TestChainTool_parseStepsArg_Invalid(t *testing.T) {
	_, err := parseStepsArg(42) // not a string and not a []any
	if err == nil {
		t.Fatal("expected error for invalid input type")
	}
}

func TestChainTool_Ordering(t *testing.T) {
	// Verify steps execute in order: output order matches input order.
	runner := &mockSubagentRunner{
		responses: map[string]string{
			"first":  "RESULT_ONE",
			"second": "RESULT_TWO",
			"third":  "RESULT_THREE",
		},
	}
	tool := NewChainExecutionTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"steps": []any{
			map[string]any{"prompt": "do first task"},
			map[string]any{"prompt": "do second task"},
			map[string]any{"prompt": "do third task"},
		},
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	oneIdx := strings.Index(result.Output, "RESULT_ONE")
	twoIdx := strings.Index(result.Output, "RESULT_TWO")
	threeIdx := strings.Index(result.Output, "RESULT_THREE")

	if oneIdx < 0 {
		t.Fatal("missing RESULT_ONE in output")
	}
	if twoIdx < 0 {
		t.Fatal("missing RESULT_TWO in output")
	}
	if threeIdx < 0 {
		t.Fatal("missing RESULT_THREE in output")
	}
	if !(oneIdx < twoIdx && twoIdx < threeIdx) {
		t.Fatal("steps executed out of order: expected RESULT_ONE < RESULT_TWO < RESULT_THREE")
	}
}
