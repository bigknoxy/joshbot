package tools

import (
	"context"
	"fmt"

	"github.com/bigknoxy/joshbot/internal/subagent"
)

// MaxParallelTasks caps how many tasks one parallel_subagent call may fan out
// to. Each task is a full ReAct loop with tool access, so an uncapped list is a
// cost- and CPU-amplification vector: one tool call from the model turns into N
// unbounded conversations. The concurrency argument only limits how many run at
// once, never how many run in total, which is the number that costs money.
const MaxParallelTasks = 8

// MaxChainSteps caps how many steps one chain_execution call may run. Steps run
// sequentially, so the amplification is linear rather than parallel, but a
// hundred-step chain is still a hundred subagent runs from a single tool call.
const MaxChainSteps = 16

// spawnGate is the runtime half of the leaf-role restriction that
// filterSchemasByRole enforces in the schema. A leaf subagent is not offered
// the spawning tools, but the model may still emit a call by name, so each
// spawning tool refuses here too. The top-level agent carries no role and sits
// at depth 0; only an actual leaf subagent (depth >= 1) is refused.
func spawnGate(ctx context.Context, toolName string) error {
	if depth := subagent.DepthFromContext(ctx); depth >= 1 && subagent.RoleFromContext(ctx) == subagent.RoleLeaf {
		return fmt.Errorf("%s refused: a leaf subagent cannot spawn child subagents", toolName)
	}
	return nil
}

// childContext returns the context the spawned children run under: one level
// deeper than the caller. Without this, parallel_subagent and chain_execution
// spawned children at the parent's own depth, so their children escaped the
// nesting budget that Runner.Run enforces and delegate_subagent respects.
func childContext(ctx context.Context) context.Context {
	return subagent.WithDepth(ctx, subagent.DepthFromContext(ctx)+1)
}
