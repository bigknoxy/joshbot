package subagent

import (
	"context"
	"strings"
	"testing"
)

// A subagent run whose context carries a depth beyond the configured maximum
// must be refused rather than spawning an unbounded chain.
func TestRunner_RefusesDepthBeyondMax(t *testing.T) {
	provider := &scriptedToolProvider{turns: []scriptedToolTurn{{content: "done"}}}
	runner := NewRunner(provider, "test-model", WithMaxDepth(2))

	// Depth 3 exceeds max 2 -> refused.
	ctx := WithDepth(context.Background(), 3)
	_, err := runner.Run(ctx, "task", Config{})
	if err == nil {
		t.Fatal("expected an error when depth exceeds the maximum")
	}
	if !strings.Contains(err.Error(), "nesting depth") {
		t.Errorf("error %q should mention nesting depth", err)
	}
}

// A subagent run at or below the maximum depth must proceed normally.
func TestRunner_AllowsDepthWithinMax(t *testing.T) {
	provider := &scriptedToolProvider{turns: []scriptedToolTurn{{content: "done"}}}
	runner := NewRunner(provider, "test-model", WithMaxDepth(2))

	ctx := WithDepth(context.Background(), 2)
	res, err := runner.Run(ctx, "task", Config{})
	if err != nil {
		t.Fatalf("unexpected error at depth within max: %v", err)
	}
	if res == nil || res.Output == "" {
		t.Fatal("expected a result at depth within max")
	}
}

// The default max depth is DefaultMaxDepth, and a run with no depth on the
// context (depth 0) is always allowed.
func TestRunner_DefaultMaxDepthAllowsTopLevel(t *testing.T) {
	provider := &scriptedToolProvider{turns: []scriptedToolTurn{{content: "done"}}}
	runner := NewRunner(provider, "test-model")

	res, err := runner.Run(context.Background(), "task", Config{})
	if err != nil {
		t.Fatalf("top-level run should always be allowed: %v", err)
	}
	if res == nil || res.Output == "" {
		t.Fatal("expected a result")
	}
}

// The orchestrator system prompt must mention the delegate tool and the depth
// limit, so the model knows it can spawn children and how deep it may go.
func TestBuildSystemPrompt_OrchestratorMentionsDelegateAndDepth(t *testing.T) {
	runner := NewRunner(nil, "test-model")
	prompt := runner.buildSystemPrompt(RoleOrchestrator, 20, 2)
	if !strings.Contains(prompt, "delegate_subagent") {
		t.Error("orchestrator prompt should mention the delegate_subagent tool")
	}
	if !strings.Contains(prompt, "nesting depth of 2") {
		t.Error("orchestrator prompt should state the nesting depth limit")
	}
}

// The leaf system prompt must forbid spawning.
func TestBuildSystemPrompt_LeafForbidsSpawning(t *testing.T) {
	runner := NewRunner(nil, "test-model")
	prompt := runner.buildSystemPrompt(RoleLeaf, 20, 2)
	if !strings.Contains(prompt, "cannot spawn subagents") {
		t.Error("leaf prompt should forbid spawning subagents")
	}
}
