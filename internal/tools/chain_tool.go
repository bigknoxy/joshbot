package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bigknoxy/joshbot/internal/log"
)

// chainStep represents a single step in a chain execution.
type chainStep struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
}

// chainResult holds the result of a single chain step.
type chainResult struct {
	index  int
	desc   string
	name   string
	prompt string
	output string
	err    error
}

// ChainExecutionTool executes multiple subagent tasks sequentially,
// feeding each step's output as context into the next step.
type ChainExecutionTool struct {
	runner SubagentRunner
}

// NewChainExecutionTool creates a new ChainExecutionTool.
func NewChainExecutionTool(runner SubagentRunner) *ChainExecutionTool {
	return &ChainExecutionTool{runner: runner}
}

func (t *ChainExecutionTool) Name() string {
	return "chain_execution"
}

func (t *ChainExecutionTool) Description() string {
	return "chain_execution: execute subagent steps sequentially, feeding each step's output as context to the next."
}

func (t *ChainExecutionTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "steps",
			Type:        ParamArray,
			Description: fmt.Sprintf("Steps to execute sequentially. Each step has: prompt (required), description (label), name (variable for template substitution). At most %d steps per call; split longer chains across several calls.", MaxChainSteps),
			Required:    true,
		},
		{
			Name:        "context",
			Type:        ParamString,
			Description: "Initial context for the first step.",
		},
	}
}

// parseStepsArg handles steps as either []any or JSON string (LLMs may serialize arrays).
// Falls back to replacing single quotes with double quotes for LLMs that output
// single-quoted JSON-like strings (a common LLM serialization quirk).
func parseStepsArg(v any) ([]any, error) {
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
	return nil, fmt.Errorf("'steps' must be a non-empty array of objects with 'prompt' field")
}

// applyTemplates replaces {{name}} placeholders in the prompt with the outputs
// of previously executed named steps.
func applyTemplates(prompt string, vars map[string]string) string {
	result := prompt
	// Sort names by length descending to prevent substring collisions
	// (e.g., {{a}} matching inside {{ab}}).
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) > len(names[j])
	})
	for _, name := range names {
		result = strings.ReplaceAll(result, "{{"+name+"}}", vars[name])
	}
	return result
}

// countChainSuccess counts the number of successful steps.
func countChainSuccess(results []chainResult) int {
	count := 0
	for _, r := range results {
		if r.err == nil {
			count++
		}
	}
	return count
}

func (t *ChainExecutionTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.runner == nil {
		return ToolResult{Error: fmt.Errorf("subagent runner not configured")}
	}

	stepsRaw, err := parseStepsArg(args["steps"])
	if err != nil {
		return ToolResult{Error: err}
	}
	if len(stepsRaw) == 0 {
		return ToolResult{Error: fmt.Errorf("'steps' must be a non-empty array of objects with 'prompt' field")}
	}
	if len(stepsRaw) > MaxChainSteps {
		return ToolResult{Error: fmt.Errorf("too many steps: %d exceeds the maximum of %d; split the work across several chain_execution calls",
			len(stepsRaw), MaxChainSteps)}
	}

	initialContext, _ := args["context"].(string)

	cctx, ok := ctx.(context.Context)
	if !ok {
		cctx = context.Background()
	}
	if err := spawnGate(cctx, t.Name()); err != nil {
		return ToolResult{Error: err}
	}
	// Each step runs one level deeper than this call, so a chain contributes to
	// the same nesting budget delegate_subagent uses.
	cctx = childContext(cctx)

	// Parse steps into typed structs.
	steps := make([]chainStep, 0, len(stepsRaw))
	for i, raw := range stepsRaw {
		stepMap, ok := raw.(map[string]any)
		if !ok {
			return ToolResult{Error: fmt.Errorf("step %d: expected object with 'prompt' field", i)}
		}
		prompt, _ := stepMap["prompt"].(string)
		if prompt == "" {
			return ToolResult{Error: fmt.Errorf("step %d: missing or empty 'prompt' field", i)}
		}
		desc, _ := stepMap["description"].(string)
		name, _ := stepMap["name"].(string)
		steps = append(steps, chainStep{Prompt: prompt, Description: desc, Name: name})
	}

	results := make([]chainResult, 0, len(steps))
	vars := make(map[string]string)      // named variables for template substitution
	accumulatedContext := initialContext // accumulated context fed to the next step

	for i, step := range steps {
		// Check for context cancellation before each step.
		select {
		case <-cctx.Done():
			results = append(results, chainResult{
				index:  i,
				desc:   stepDescription(step, i),
				prompt: step.Prompt,
				err:    cctx.Err(),
			})
			// Mark remaining steps as skipped due to cancellation.
			for j := i + 1; j < len(steps); j++ {
				results = append(results, chainResult{
					index:  j,
					desc:   stepDescription(steps[j], j),
					prompt: steps[j].Prompt,
					err:    fmt.Errorf("skipped: chain cancelled"),
				})
			}
			report := buildChainReport(results, initialContext, steps)
			return ToolResult{Output: report}
		default:
		}

		// Apply template substitution to the step's prompt.
		resolvedPrompt := applyTemplates(step.Prompt, vars)

		// Build the full prompt with accumulated context.
		var fullPrompt string
		if accumulatedContext != "" {
			fullPrompt = fmt.Sprintf("Context: %s\n\nTask: %s", accumulatedContext, resolvedPrompt)
		} else {
			fullPrompt = resolvedPrompt
		}

		desc := stepDescription(step, i)

		log.Debug("Chain step executing", "step", i+1, "description", desc)

		output, err := t.runner.SimpleRun(cctx, fullPrompt)
		if err != nil {
			log.Warn("Chain step failed", "step", i+1, "error", err)
			results = append(results, chainResult{
				index:  i,
				desc:   desc,
				name:   step.Name,
				prompt: resolvedPrompt,
				err:    err,
			})
			// Continue executing subsequent steps — do not update
			// accumulatedContext or vars so later steps use the previous context.
			continue
		}

		results = append(results, chainResult{
			index:  i,
			desc:   desc,
			name:   step.Name,
			prompt: resolvedPrompt,
			output: output,
		})

		// Store output as named variable for template substitution in later steps.
		if step.Name != "" {
			vars[step.Name] = output
		}

		// Feed this step's output as context for the next step.
		accumulatedContext = output
	}

	report := buildChainReport(results, initialContext, steps)
	return ToolResult{Output: report}
}

// stepDescription returns the display label for a step.
func stepDescription(s chainStep, idx int) string {
	if s.Description != "" {
		return s.Description
	}
	return fmt.Sprintf("Step %d", idx+1)
}

// buildChainReport formats the chain execution results as a markdown report.
func buildChainReport(results []chainResult, initialContext string, steps []chainStep) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Chain Execution Results (%d steps)\n\n", len(steps)))

	if initialContext != "" {
		truncated := initialContext
		if len(truncated) > 200 {
			truncated = truncated[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("**Initial Context:** %s\n\n", truncated))
	}

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("### Step %d: %s\n\n", i+1, r.desc))

		// Truncate the prompt preview if it's long.
		promptPreview := r.prompt
		if len(promptPreview) > 100 {
			promptPreview = promptPreview[:100] + "..."
		}
		sb.WriteString(fmt.Sprintf("**Prompt:** %s\n\n", promptPreview))

		if r.err != nil {
			sb.WriteString(fmt.Sprintf("**Error:** %s\n\n", r.err))
		} else {
			sb.WriteString(r.output)
			sb.WriteString("\n\n")
		}
	}

	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("_Chain execution complete: %d/%d steps succeeded_\n", countChainSuccess(results), len(results)))
	return sb.String()
}
