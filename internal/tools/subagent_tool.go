package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/subagent"
)

// SubagentRunner is the interface for running a subagent task.
type SubagentRunner interface {
	SimpleRun(ctx context.Context, prompt string) (string, error)
}

// DelegatingRunner is the interface for running a subagent with a role, model
// override, and nesting-depth tracking. It is implemented by subagent.Runner
// and used by the delegate_subagent tool so an orchestrator can actually spawn
// a child subagent.
type DelegatingRunner interface {
	RunWithCallback(ctx context.Context, prompt string, cfg subagent.Config, asyncCallback func(subagent.AsyncResult), onProgress subagent.ProgressFunc) (*subagent.SubResult, error)
}

// subagentTask represents a single parallel task.
type subagentTask struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description,omitempty"`
}

// subagentResult holds the result of a single subagent execution.
type subagentResult struct {
	index  int
	desc   string
	output string
	err    error
}

// ParallelSubagentTool fans out independent tasks to subagents in parallel.
type ParallelSubagentTool struct {
	runner SubagentRunner
}

// NewParallelSubagentTool creates a new ParallelSubagentTool.
func NewParallelSubagentTool(runner SubagentRunner) *ParallelSubagentTool {
	return &ParallelSubagentTool{runner: runner}
}

func (t *ParallelSubagentTool) Name() string {
	return "parallel_subagent"
}

func (t *ParallelSubagentTool) Description() string {
	return "parallel_subagent: run multiple subagent tasks in parallel for independent work (research, file analysis, etc.)."
}

func (t *ParallelSubagentTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "tasks",
			Type:        ParamArray,
			Description: fmt.Sprintf("Tasks to run in parallel. Each has: prompt (instruction), description (label). At most %d tasks per call; split larger work across several calls.", MaxParallelTasks),
			Required:    true,
		},
		{
			Name:        "concurrency",
			Type:        ParamInteger,
			Description: "Max concurrent tasks (default 5).",
			Default:     5.0,
		},
	}
}

// parseTasksArg handles tasks as either []any or JSON string (LLMs may serialize arrays).
// Falls back to replacing single quotes with double quotes for LLMs that output
// single-quoted JSON-like strings (a common LLM serialization quirk).
func parseTasksArg(v any) ([]any, error) {
	if arr, ok := v.([]any); ok {
		return arr, nil
	}
	if s, ok := v.(string); ok && s != "" {
		var parsed []any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			return parsed, nil
		}
		// Try replacing single quotes with double quotes — LLMs often output
		// JSON-like strings with single quotes instead of proper JSON double quotes.
		if err := json.Unmarshal([]byte(strings.ReplaceAll(s, "'", "\"")), &parsed); err == nil {
			return parsed, nil
		}
	}
	return nil, fmt.Errorf("'tasks' must be a non-empty array of objects with 'prompt' field")
}

func (t *ParallelSubagentTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.runner == nil {
		return ToolResult{Error: fmt.Errorf("subagent runner not configured")}
	}

	tasksRaw, err := parseTasksArg(args["tasks"])
	if err != nil {
		return ToolResult{Error: err}
	}
	if len(tasksRaw) == 0 {
		return ToolResult{Error: fmt.Errorf("'tasks' must be a non-empty array of objects with 'prompt' field")}
	}
	if len(tasksRaw) > MaxParallelTasks {
		return ToolResult{Error: fmt.Errorf("too many tasks: %d exceeds the maximum of %d; split the work across several parallel_subagent calls",
			len(tasksRaw), MaxParallelTasks)}
	}

	concurrency := 5
	if cRaw, ok := args["concurrency"]; ok {
		switch v := cRaw.(type) {
		case float64:
			concurrency = int(v)
		case int:
			concurrency = v
		}
	}
	if concurrency < 1 {
		concurrency = 1
	}

	tasks := make([]subagentTask, 0, len(tasksRaw))
	for i, raw := range tasksRaw {
		taskMap, ok := raw.(map[string]any)
		if !ok {
			return ToolResult{Error: fmt.Errorf("task %d: expected object with 'prompt' field", i)}
		}
		prompt, _ := taskMap["prompt"].(string)
		if prompt == "" {
			return ToolResult{Error: fmt.Errorf("task %d: missing or empty 'prompt' field", i)}
		}
		desc, _ := taskMap["description"].(string)
		tasks = append(tasks, subagentTask{Prompt: prompt, Description: desc})
	}

	cctx, ok := ctx.(context.Context)
	if !ok {
		cctx = context.Background()
	}
	if err := spawnGate(cctx, t.Name()); err != nil {
		return ToolResult{Error: err}
	}
	// Every task runs one level deeper than this call, so the fan-out counts
	// against the same nesting budget delegate_subagent uses.
	cctx = childContext(cctx)

	sem := make(chan struct{}, concurrency)
	results := make(chan subagentResult, len(tasks))
	var wg sync.WaitGroup

	runner := t.runner
	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, st subagentTask) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-cctx.Done():
				results <- subagentResult{index: idx, desc: st.Description, err: cctx.Err()}
				return
			}
			defer func() { <-sem }()

			select {
			case <-cctx.Done():
				results <- subagentResult{index: idx, desc: st.Description, err: cctx.Err()}
				return
			default:
			}

			output, err := runner.SimpleRun(cctx, st.Prompt)
			results <- subagentResult{index: idx, desc: st.Description, output: output, err: err}
		}(i, task)
	}

	wg.Wait()
	close(results)

	ordered := make([]subagentResult, len(tasks))
	for r := range results {
		ordered[r.index] = r
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Parallel Subagent Results (%d tasks)\n\n", len(tasks)))
	for i, r := range ordered {
		label := r.desc
		if label == "" {
			label = fmt.Sprintf("Task %d", i+1)
		}
		sb.WriteString(fmt.Sprintf("### %s\n\n", label))
		if r.err != nil {
			log.Warn("Parallel subagent task failed", "task", i+1, "error", r.err)
			sb.WriteString(fmt.Sprintf("Error: %s\n\n", r.err))
		} else {
			sb.WriteString(r.output)
			sb.WriteString("\n\n")
		}
	}
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("_Parallel execution complete: %d/%d tasks succeeded_\n", countSuccessSR(ordered), len(tasks)))

	return ToolResult{Output: sb.String()}
}

func countSuccessSR(results []subagentResult) int {
	count := 0
	for _, r := range results {
		if r.err == nil {
			count++
		}
	}
	return count
}
