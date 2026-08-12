package subagent

import (
	"context"
	"fmt"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// recordingProvider records the tool schemas each chat request carried and
// returns a simple answer so the subagent loop terminates after one turn.
type recordingProvider struct {
	toolNames []string
}

func (p *recordingProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	p.toolNames = nil
	for _, t := range req.Tools {
		p.toolNames = append(p.toolNames, t.Function.Name)
	}
	return &providers.ChatResponse{Choices: []providers.Choice{{Message: providers.Message{Content: "done"}}}}, nil
}

func (p *recordingProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}
func (p *recordingProvider) Transcribe(ctx context.Context, a []byte, prompt string) (string, error) {
	return "", nil
}
func (p *recordingProvider) Name() string             { return "recording" }
func (p *recordingProvider) Config() providers.Config { return providers.DefaultConfig() }

// schemaExecutor returns a fixed set of tool schemas including the
// subagent-spawning tools.
type schemaExecutor struct{}

func (s *schemaExecutor) GetSchemas() []providers.Tool {
	return []providers.Tool{
		{Function: providers.FunctionDefinition{Name: "shell"}},
		{Function: providers.FunctionDefinition{Name: "filesystem"}},
		{Function: providers.FunctionDefinition{Name: "delegate_subagent"}},
		{Function: providers.FunctionDefinition{Name: "parallel_subagent"}},
		{Function: providers.FunctionDefinition{Name: "chain_execution"}},
	}
}

func (s *schemaExecutor) ExecuteWithContext(ctx context.Context, name string, args map[string]any, channel, channelID string, callback func(AsyncResult)) (ToolResult, bool) {
	return ToolResult{Output: "ok"}, false
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// A leaf-role subagent must NOT be offered the subagent-spawning tools. The
// "leaf cannot spawn" contract is currently prompt-only; this test pins the
// code-level filter.
func TestRunner_LeafSchemaExcludesDelegateTools(t *testing.T) {
	provider := &recordingProvider{}
	runner := NewRunner(provider, "test-model", WithTools(&schemaExecutor{}))

	cfg := Config{Role: RoleLeaf}
	if _, err := runner.Run(context.Background(), "task", cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, forbidden := range []string{"delegate_subagent", "parallel_subagent", "chain_execution"} {
		if containsName(provider.toolNames, forbidden) {
			t.Errorf("leaf subagent was offered %q; leaf must not be able to spawn subagents", forbidden)
		}
	}
	for _, allowed := range []string{"shell", "filesystem"} {
		if !containsName(provider.toolNames, allowed) {
			t.Errorf("leaf subagent should still have %q", allowed)
		}
	}
}

// An orchestrator-role subagent MUST be offered the delegate tool.
func TestRunner_OrchestratorSchemaIncludesDelegateTools(t *testing.T) {
	provider := &recordingProvider{}
	runner := NewRunner(provider, "test-model", WithTools(&schemaExecutor{}))

	cfg := Config{Role: RoleOrchestrator}
	if _, err := runner.Run(context.Background(), "task", cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, want := range []string{"delegate_subagent", "parallel_subagent", "chain_execution", "shell"} {
		if !containsName(provider.toolNames, want) {
			t.Errorf("orchestrator subagent should be offered %q", want)
		}
	}
}
