package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bigknoxy/joshbot/internal/subagent"
)

// countingRunner records how many subagent runs were actually started and at
// what nesting depth each one saw. Counting is the point: a width limit that
// reports an error but still runs the work has not limited anything.
type countingRunner struct {
	mu     sync.Mutex
	calls  int
	depths []int
}

func (r *countingRunner) SimpleRun(ctx context.Context, prompt string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.depths = append(r.depths, subagent.DepthFromContext(ctx))
	return "ok", nil
}

func (r *countingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *countingRunner) seenDepths() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.depths...)
}

func tasksArg(n int) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"prompt": fmt.Sprintf("task %d", i)})
	}
	return out
}

// An oversized fan-out is refused before any subagent starts. Without the cap a
// single model tool call spawns one full ReAct loop per task, so the regression
// this catches is unbounded LLM cost from one turn.
func TestParallelSubagentRefusesMoreTasksThanTheCapAndRunsNone(t *testing.T) {
	runner := &countingRunner{}
	tool := NewParallelSubagentTool(runner)

	res := tool.Execute(context.Background(), map[string]any{"tasks": tasksArg(MaxParallelTasks + 1)})

	if res.Error == nil {
		t.Fatalf("expected an error for %d tasks (cap %d), got output: %s", MaxParallelTasks+1, MaxParallelTasks, res.Output)
	}
	if !strings.Contains(res.Error.Error(), fmt.Sprintf("maximum of %d", MaxParallelTasks)) {
		t.Errorf("error should name the cap so the model can split the work, got: %v", res.Error)
	}
	if n := runner.count(); n != 0 {
		t.Errorf("%d subagent(s) ran for a refused call; the cap must be enforced before any work starts", n)
	}
}

// The cap is a maximum, not a smaller limit: exactly MaxParallelTasks runs.
func TestParallelSubagentRunsExactlyTheCap(t *testing.T) {
	runner := &countingRunner{}
	tool := NewParallelSubagentTool(runner)

	res := tool.Execute(context.Background(), map[string]any{"tasks": tasksArg(MaxParallelTasks)})

	if res.Error != nil {
		t.Fatalf("a call at exactly the cap must be allowed, got: %v", res.Error)
	}
	if n := runner.count(); n != MaxParallelTasks {
		t.Errorf("ran %d subagents, want %d", n, MaxParallelTasks)
	}
}

// Fanned-out children run one level deeper than the call. Without this they
// spawned at the parent's own depth, so their own delegate_subagent calls
// escaped the nesting budget Runner.Run enforces.
func TestParallelSubagentRunsChildrenOneLevelDeeper(t *testing.T) {
	runner := &countingRunner{}
	tool := NewParallelSubagentTool(runner)

	ctx := subagent.WithRole(subagent.WithDepth(context.Background(), 1), subagent.RoleOrchestrator)
	if res := tool.Execute(ctx, map[string]any{"tasks": tasksArg(2)}); res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	for i, d := range runner.seenDepths() {
		if d != 2 {
			t.Errorf("task %d ran at depth %d, want 2 (parent depth 1 + 1)", i, d)
		}
	}
}

// A leaf subagent loses the spawning tools from its schema, but the model can
// still emit the call by name. The runtime gate is what actually stops it.
func TestParallelSubagentRefusesALeafSubagentAtRuntime(t *testing.T) {
	runner := &countingRunner{}
	tool := NewParallelSubagentTool(runner)

	ctx := subagent.WithRole(subagent.WithDepth(context.Background(), 1), subagent.RoleLeaf)
	res := tool.Execute(ctx, map[string]any{"tasks": tasksArg(2)})

	if res.Error == nil {
		t.Fatal("a leaf subagent must not be able to fan out, got no error")
	}
	if n := runner.count(); n != 0 {
		t.Errorf("%d subagent(s) ran for a refused leaf call", n)
	}
}

// The top-level agent carries no role and sits at depth 0. It is not a leaf
// subagent and must keep working — a gate that also blocked it would break
// parallel_subagent for every ordinary turn.
func TestParallelSubagentAllowsTheTopLevelAgent(t *testing.T) {
	runner := &countingRunner{}
	tool := NewParallelSubagentTool(runner)

	if res := tool.Execute(context.Background(), map[string]any{"tasks": tasksArg(2)}); res.Error != nil {
		t.Fatalf("the top-level agent must be allowed to fan out, got: %v", res.Error)
	}
	if n := runner.count(); n != 2 {
		t.Errorf("ran %d subagents, want 2", n)
	}
}

func stepsArg(n int) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"prompt": fmt.Sprintf("step %d", i)})
	}
	return out
}

// chain_execution runs steps sequentially, so an uncapped list is a linear but
// still unbounded amplification: one tool call, N subagent runs.
func TestChainExecutionRefusesMoreStepsThanTheCapAndRunsNone(t *testing.T) {
	runner := &countingRunner{}
	tool := NewChainExecutionTool(runner)

	res := tool.Execute(context.Background(), map[string]any{"steps": stepsArg(MaxChainSteps + 1)})

	if res.Error == nil {
		t.Fatalf("expected an error for %d steps (cap %d), got output: %s", MaxChainSteps+1, MaxChainSteps, res.Output)
	}
	if !strings.Contains(res.Error.Error(), fmt.Sprintf("maximum of %d", MaxChainSteps)) {
		t.Errorf("error should name the cap, got: %v", res.Error)
	}
	if n := runner.count(); n != 0 {
		t.Errorf("%d step(s) ran for a refused chain", n)
	}
}

func TestChainExecutionRunsExactlyTheCap(t *testing.T) {
	runner := &countingRunner{}
	tool := NewChainExecutionTool(runner)

	if res := tool.Execute(context.Background(), map[string]any{"steps": stepsArg(MaxChainSteps)}); res.Error != nil {
		t.Fatalf("a chain at exactly the cap must be allowed, got: %v", res.Error)
	}
	if n := runner.count(); n != MaxChainSteps {
		t.Errorf("ran %d steps, want %d", n, MaxChainSteps)
	}
}

func TestChainExecutionRunsStepsOneLevelDeeper(t *testing.T) {
	runner := &countingRunner{}
	tool := NewChainExecutionTool(runner)

	ctx := subagent.WithRole(subagent.WithDepth(context.Background(), 1), subagent.RoleOrchestrator)
	if res := tool.Execute(ctx, map[string]any{"steps": stepsArg(2)}); res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}

	for i, d := range runner.seenDepths() {
		if d != 2 {
			t.Errorf("step %d ran at depth %d, want 2 (parent depth 1 + 1)", i, d)
		}
	}
}

func TestChainExecutionRefusesALeafSubagentAtRuntime(t *testing.T) {
	runner := &countingRunner{}
	tool := NewChainExecutionTool(runner)

	ctx := subagent.WithRole(subagent.WithDepth(context.Background(), 1), subagent.RoleLeaf)
	res := tool.Execute(ctx, map[string]any{"steps": stepsArg(2)})

	if res.Error == nil {
		t.Fatal("a leaf subagent must not be able to run a chain, got no error")
	}
	if n := runner.count(); n != 0 {
		t.Errorf("%d step(s) ran for a refused leaf call", n)
	}
}

// An orchestrator subagent is the role the spawning tools exist for: depth >= 1
// must not be refused on its own.
func TestSpawnGateAllowsAnOrchestratorSubagent(t *testing.T) {
	ctx := subagent.WithRole(subagent.WithDepth(context.Background(), 1), subagent.RoleOrchestrator)
	if err := spawnGate(ctx, "parallel_subagent"); err != nil {
		t.Errorf("an orchestrator must be allowed to spawn, got: %v", err)
	}
}
