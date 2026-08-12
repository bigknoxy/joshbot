package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/subagent"
)

// mockDelegatingRunner records the config it was given and returns a canned
// result, so the delegate tool's wiring (role, model, depth) can be asserted
// without a real LLM.
type mockDelegatingRunner struct {
	lastCfg   subagent.Config
	lastCtx   context.Context
	output    string
	err       error
	callCount int
}

func (m *mockDelegatingRunner) RunWithCallback(ctx context.Context, prompt string, cfg subagent.Config, _ func(subagent.AsyncResult), _ subagent.ProgressFunc) (*subagent.SubResult, error) {
	m.callCount++
	m.lastCfg = cfg
	m.lastCtx = ctx
	if m.err != nil {
		return nil, m.err
	}
	return &subagent.SubResult{Output: m.output}, nil
}

func TestDelegateSubagentTool_Name(t *testing.T) {
	tool := NewDelegateSubagentTool(&mockDelegatingRunner{})
	if tool.Name() != "delegate_subagent" {
		t.Fatalf("expected 'delegate_subagent', got %q", tool.Name())
	}
}

func TestDelegateSubagentTool_Parameters(t *testing.T) {
	tool := NewDelegateSubagentTool(&mockDelegatingRunner{})
	params := tool.Parameters()
	names := map[string]bool{}
	for _, p := range params {
		names[p.Name] = true
	}
	for _, want := range []string{"prompt", "role", "model"} {
		if !names[want] {
			t.Errorf("expected parameter %q", want)
		}
	}
}

func TestDelegateSubagentTool_NilRunner(t *testing.T) {
	tool := NewDelegateSubagentTool(nil)
	result := tool.Execute(context.Background(), map[string]any{"prompt": "hi"})
	if result.Error == nil {
		t.Fatal("expected error for nil runner")
	}
}

func TestDelegateSubagentTool_MissingPrompt(t *testing.T) {
	tool := NewDelegateSubagentTool(&mockDelegatingRunner{})
	result := tool.Execute(context.Background(), map[string]any{})
	if result.Error == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func TestDelegateSubagentTool_DefaultsToLeafRole(t *testing.T) {
	runner := &mockDelegatingRunner{output: "child result"}
	tool := NewDelegateSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{"prompt": "do the thing"})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Output, "child result") {
		t.Fatalf("expected child result in output, got %q", result.Output)
	}
	if runner.lastCfg.Role != subagent.RoleLeaf {
		t.Errorf("expected default RoleLeaf, got %v", runner.lastCfg.Role)
	}
}

func TestDelegateSubagentTool_OrchestratorRole(t *testing.T) {
	runner := &mockDelegatingRunner{output: "ok"}
	tool := NewDelegateSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"prompt": "do it",
		"role":   "orchestrator",
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if runner.lastCfg.Role != subagent.RoleOrchestrator {
		t.Errorf("expected RoleOrchestrator, got %v", runner.lastCfg.Role)
	}
}

func TestDelegateSubagentTool_ModelOverride(t *testing.T) {
	runner := &mockDelegatingRunner{output: "ok"}
	tool := NewDelegateSubagentTool(runner)
	result := tool.Execute(context.Background(), map[string]any{
		"prompt": "do it",
		"model":  "poolside/laguna-s-2.1",
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if runner.lastCfg.Model != "poolside/laguna-s-2.1" {
		t.Errorf("expected model override, got %q", runner.lastCfg.Model)
	}
}

// The delegate tool must carry the nesting depth on the context so a chain of
// delegate_subagent calls accumulates depth and the Runner can enforce the limit.
func TestDelegateSubagentTool_IncrementsDepthOnContext(t *testing.T) {
	runner := &mockDelegatingRunner{output: "ok"}
	tool := NewDelegateSubagentTool(runner)

	// Top-level call: depth 0 -> child runs at depth 1.
	result := tool.Execute(context.Background(), map[string]any{"prompt": "do it"})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if got := subagent.DepthFromContext(runner.lastCtx); got != 1 {
		t.Errorf("expected child depth 1, got %d", got)
	}

	// A nested call from an orchestrator subagent already at depth 1 -> child
	// at depth 2. The role must be orchestrator, or the leaf gate refuses it.
	ctx := subagent.WithRole(subagent.WithDepth(context.Background(), 1), subagent.RoleOrchestrator)
	result = tool.Execute(ctx, map[string]any{"prompt": "do it again"})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if got := subagent.DepthFromContext(runner.lastCtx); got != 2 {
		t.Errorf("expected child depth 2, got %d", got)
	}
}

// The delegate tool must refuse a leaf subagent at runtime (defense in depth),
// even though the leaf is not offered the tool in its schema. A leaf subagent
// is at depth >= 1 with RoleLeaf on the context.
func TestDelegateSubagentTool_RefusesLeafSubagent(t *testing.T) {
	runner := &mockDelegatingRunner{output: "ok"}
	tool := NewDelegateSubagentTool(runner)

	ctx := subagent.WithRole(subagent.WithDepth(context.Background(), 1), subagent.RoleLeaf)
	result := tool.Execute(ctx, map[string]any{"prompt": "do it"})
	if result.Error == nil {
		t.Fatal("expected delegate_subagent to refuse a leaf subagent")
	}
	if !strings.Contains(result.Error.Error(), "leaf subagent") {
		t.Errorf("refusal error %q should mention the leaf role", result.Error)
	}
	if runner.callCount != 0 {
		t.Errorf("runner was called %d time(s); a refused leaf must not spawn a child", runner.callCount)
	}
}

// An orchestrator subagent (or the top-level agent with no role) may delegate.
func TestDelegateSubagentTool_AllowsOrchestrator(t *testing.T) {
	runner := &mockDelegatingRunner{output: "ok"}
	tool := NewDelegateSubagentTool(runner)

	ctx := subagent.WithRole(subagent.WithDepth(context.Background(), 2), subagent.RoleOrchestrator)
	result := tool.Execute(ctx, map[string]any{"prompt": "do it"})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if runner.callCount != 1 {
		t.Errorf("expected the orchestrator's child to run, callCount=%d", runner.callCount)
	}
}

// The top-level agent (no role, depth 0) may delegate.
func TestDelegateSubagentTool_AllowsTopLevelAgent(t *testing.T) {
	runner := &mockDelegatingRunner{output: "ok"}
	tool := NewDelegateSubagentTool(runner)

	result := tool.Execute(context.Background(), map[string]any{"prompt": "do it"})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if runner.callCount != 1 {
		t.Errorf("expected the top-level child to run, callCount=%d", runner.callCount)
	}
}

// Ensure DelegatingRunner is satisfied by the real Runner.
func TestDelegatingRunnerInterface_Compatible(t *testing.T) {
	var _ DelegatingRunner = (*subagent.Runner)(nil)
}
