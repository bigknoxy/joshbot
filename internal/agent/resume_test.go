package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

func TestResumeCommand_NoCheckpoint(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &scriptedProvider{}
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	resp, err := agent.Process(context.Background(), cmdMsg("/resume"))
	if err != nil {
		t.Fatalf("Process(/resume) returned %v", err)
	}
	if !strings.Contains(resp, "No checkpoint found") {
		t.Errorf("expected 'No checkpoint found' message, got: %s", resp)
	}
}

func TestResumeCommand_WithCheckpoint(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &scriptedProvider{
		turns: []scriptedTurn{
			{content: "Task continued successfully."},
		},
	}
	sm := newMockSessionManager()
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, sm, newMockLogger())

	// Manually insert a checkpoint into the session
	sess, err := sm.GetOrCreate(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sess.Checkpoint = &session.Checkpoint{
		Iteration:     10,
		MaxIterations: 50,
		CreatedAt:     time.Now(),
		UserMessage:   "test task",
	}
	if err := sm.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	resp, err := agent.Process(context.Background(), cmdMsg("/resume"))
	if err != nil {
		t.Fatalf("Process(/resume) returned %v", err)
	}
	// Should mention the checkpoint iteration
	if !strings.Contains(resp, "iteration 10") {
		t.Errorf("expected 'iteration 10' in response, got: %s", resp)
	}
	if !strings.Contains(resp, "Resuming from checkpoint") {
		t.Errorf("expected 'Resuming from checkpoint' in response, got: %s", resp)
	}
}

func TestResumeCommand_ClearsCheckpoint(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &scriptedProvider{
		turns: []scriptedTurn{
			{content: "Resumed work done."},
		},
	}
	sm := newMockSessionManager()
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, sm, newMockLogger())

	// Insert a checkpoint
	sess, err := sm.GetOrCreate(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sess.Checkpoint = &session.Checkpoint{
		Iteration:     5,
		MaxIterations: 50,
		CreatedAt:     time.Now(),
	}
	if err := sm.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// First /resume should work
	resp, err := agent.Process(context.Background(), cmdMsg("/resume"))
	if err != nil {
		t.Fatalf("first /resume returned %v", err)
	}
	if !strings.Contains(resp, "Resuming from checkpoint") {
		t.Errorf("first /resume should resume, got: %s", resp)
	}

	// Second /resume should say no checkpoint (it was cleared)
	resp2, err := agent.Process(context.Background(), cmdMsg("/resume"))
	if err != nil {
		t.Fatalf("second /resume returned %v", err)
	}
	if !strings.Contains(resp2, "No checkpoint found") {
		t.Errorf("second /resume should say no checkpoint, got: %s", resp2)
	}
}

func TestCheckpointSavedOnIterationLimit(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	// Provider that returns tool calls every turn — the loop will hit the
	// iteration limit because it never gets a "no tool calls" response.
	tc := providers.ToolCall{
		ID:   "tc_1",
		Type: "function",
		Function: providers.FunctionCall{
			Name:      "shell",
			Arguments: `{"command":"echo test"}`,
		},
	}
	provider := &scriptedProvider{
		turns: []scriptedTurn{
			{toolCalls: []providers.ToolCall{tc}},
			{toolCalls: []providers.ToolCall{tc}},
			{toolCalls: []providers.ToolCall{tc}},
			{toolCalls: []providers.ToolCall{tc}},
			{toolCalls: []providers.ToolCall{tc}},
		},
	}

	sm := newMockSessionManager()
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, sm, newMockLogger())

	// Set a low iteration limit for testing
	agent.SetMaxIterations(2)

	msg := bus.InboundMessage{
		SenderID:  "user",
		Channel:   "cli",
		Content:   "do something repeatedly",
		Timestamp: time.Now(),
	}

	_, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("Process returned %v", err)
	}

	// Verify checkpoint was saved
	sess, err := sm.GetOrCreate(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if sess.Checkpoint == nil {
		t.Fatal("expected checkpoint to be saved when iteration limit hit")
	}
	if sess.Checkpoint.MaxIterations != 2 {
		t.Errorf("expected MaxIterations=2, got %d", sess.Checkpoint.MaxIterations)
	}
}
