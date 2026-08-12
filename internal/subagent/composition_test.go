package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// recordingToolProvider replays a fixed sequence of turns AND records the model
// each chat request was sent for. This is the composition seam the mock-based
// delegate tool test cannot reach: it proves the child's per-task model override
// actually reaches the provider, and that the child executes a real tool.
type recordingToolProvider struct {
	scriptedToolProvider
	models []string
}

func (p *recordingToolProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	p.models = append(p.models, req.Model)
	return p.scriptedToolProvider.Chat(ctx, req)
}

// recordingExecutor is a ToolExecutor that records the tools it was asked to
// run and returns a canned output, so the child's tool execution is observable.
type recordingExecutor struct {
	called []string
}

func (r *recordingExecutor) GetSchemas() []providers.Tool {
	return []providers.Tool{
		{Function: providers.FunctionDefinition{Name: "echo"}},
	}
}

func (r *recordingExecutor) ExecuteWithContext(ctx context.Context, name string, args map[string]any, channel, channelID string, callback func(AsyncResult)) (ToolResult, bool) {
	r.called = append(r.called, name)
	return ToolResult{Output: "echoed"}, false
}

// The composition test: run the REAL Runner with a scripted provider that
// (1) requests a tool, (2) sees the tool result, (3) returns the final answer.
// It asserts both that the child actually executed a tool and that the per-task
// model override reached the provider's Chat request — the two seams the mock
// wire-test leaves uncovered.
func TestRunner_CompositionChildRunsToolAndModelOverrideReachesProvider(t *testing.T) {
	provider := &recordingToolProvider{scriptedToolProvider: scriptedToolProvider{
		turns: []scriptedToolTurn{
			// Turn 1: request the echo tool.
			{toolCalls: []providers.ToolCall{{
				ID: "call_1", Type: "function",
				Function: providers.FunctionCall{Name: "echo", Arguments: `{"text":"hi"}`},
			}}},
			// Turn 2: no tool call — done, reports the result.
			{content: "child finished"},
		},
	}}
	exec := &recordingExecutor{}
	runner := NewRunner(provider, "default-model", WithTools(exec))

	res, err := runner.Run(context.Background(), "task", Config{
		Model: "override-model",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil || !strings.Contains(res.Output, "child finished") {
		t.Fatalf("expected the child's final answer, got %q", func() string {
			if res == nil {
				return "<nil>"
			}
			return res.Output
		}())
	}

	// The child must have actually executed the echo tool.
	if len(exec.called) != 1 || exec.called[0] != "echo" {
		t.Errorf("child executed tools %v; want exactly one 'echo' call", exec.called)
	}

	// The per-task model override must reach the provider.
	if len(provider.models) == 0 {
		t.Fatal("provider was never called")
	}
	for _, m := range provider.models {
		if m != "override-model" {
			t.Errorf("child chat request used model %q, want the override 'override-model'", m)
		}
	}
}

// Without an override the child falls back to the runner's default model.
func TestRunner_CompositionChildUsesDefaultModelWithoutOverride(t *testing.T) {
	provider := &recordingToolProvider{scriptedToolProvider: scriptedToolProvider{
		turns: []scriptedToolTurn{{content: "done"}},
	}}
	runner := NewRunner(provider, "default-model")

	if _, err := runner.Run(context.Background(), "task", Config{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(provider.models) == 0 {
		t.Fatal("provider was never called")
	}
	for _, m := range provider.models {
		if m != "default-model" {
			t.Errorf("child chat request used model %q, want the runner default 'default-model'", m)
		}
	}
}
