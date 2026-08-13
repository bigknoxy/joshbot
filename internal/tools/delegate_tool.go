package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/bigknoxy/joshbot/internal/subagent"
)

// DelegateSubagentTool lets an orchestrator subagent spawn a child subagent
// with a role, an optional model override, and nesting-depth tracking. It is
// the missing piece that makes the orchestrator role real: without it, an
// orchestrator's system prompt says it can delegate but no tool exists to do so.
type DelegateSubagentTool struct {
	runner DelegatingRunner
}

// NewDelegateSubagentTool creates a new DelegateSubagentTool.
func NewDelegateSubagentTool(runner DelegatingRunner) *DelegateSubagentTool {
	return &DelegateSubagentTool{runner: runner}
}

func (t *DelegateSubagentTool) Name() string {
	return "delegate_subagent"
}

func (t *DelegateSubagentTool) Description() string {
	return "delegate_subagent: spawn a child subagent to complete a focused task, optionally with a different model. Only orchestrator subagents may use this. The child runs an isolated ReAct loop with tool access and returns its final answer."
}

func (t *DelegateSubagentTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "prompt",
			Type:        ParamString,
			Description: "The task for the child subagent to complete.",
			Required:    true,
		},
		{
			Name:        "role",
			Type:        ParamString,
			Description: "Role for the child: 'leaf' (cannot spawn further subagents) or 'orchestrator' (can). Default 'leaf'.",
			Enum:        []string{"leaf", "orchestrator"},
		},
		{
			Name:        "model",
			Type:        ParamString,
			Description: "Optional model override for the child subagent. Empty uses the parent's model.",
		},
	}
}

func (t *DelegateSubagentTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.runner == nil {
		return ToolResult{Error: fmt.Errorf("subagent runner not configured")}
	}

	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return ToolResult{Error: fmt.Errorf("'prompt' must be a non-empty string")}
	}

	role := subagent.RoleLeaf
	if r, _ := args["role"].(string); r == "orchestrator" {
		role = subagent.RoleOrchestrator
	}
	model, _ := args["model"].(string)

	cctx, ok := ctx.(context.Context)
	if !ok {
		cctx = context.Background()
	}

	// Defense in depth: a leaf subagent is not offered delegate_subagent in its
	// schema, but the model may still attempt to call it by name. Refuse here at
	// runtime too. The gate is shared with parallel_subagent and chain_execution
	// so all three spawning tools cannot drift apart.
	if err := spawnGate(cctx, t.Name()); err != nil {
		return ToolResult{Error: err}
	}

	// The child runs one level deeper than the current subagent. The depth is
	// carried on the context so nested delegate_subagent calls accumulate.
	childCtx := childContext(cctx)

	cfg := subagent.Config{
		Role:  role,
		Model: model,
	}
	res, err := t.runner.RunWithCallback(childCtx, prompt, cfg, nil, nil)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("delegate_subagent failed: %w", err)}
	}
	if res == nil {
		return ToolResult{Output: "Child subagent returned no output."}
	}
	return ToolResult{Output: res.Output}
}
