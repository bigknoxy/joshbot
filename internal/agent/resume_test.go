package agent

import (
	"context"
	"errors"
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

// TestResumeCommand_RealSessionManager proves the checkpoint survives a real
// Save/Load round-trip through the on-disk session manager, not just the
// in-memory mock. This is the regression that made /resume non-functional in
// production: the checkpoint was set in memory but never persisted, so a real
// Load always returned nil and /resume always said "No checkpoint found".
func TestResumeCommand_RealSessionManager(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	// A provider that returns tool calls every turn — the loop hits the
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
		},
	}

	sm, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, sm, newMockLogger())
	agent.SetMaxIterations(2)

	msg := bus.InboundMessage{
		SenderID:  "user",
		Channel:   "cli",
		Content:   "do something repeatedly",
		Timestamp: time.Now(),
	}
	if _, err := agent.Process(context.Background(), msg); err != nil {
		t.Fatalf("Process returned %v", err)
	}

	// The checkpoint must be on disk now — a fresh Load (as a restart would
	// do) must see it.
	sess, err := sm.Load(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if sess.Checkpoint == nil {
		t.Fatal("checkpoint did not survive a real Save/Load round-trip; /resume is broken")
	}

	// Now /resume must actually resume, not report "no checkpoint".
	provider.turns = []scriptedTurn{{content: "Task continued successfully."}}
	resp, err := agent.Process(context.Background(), cmdMsg("/resume"))
	if err != nil {
		t.Fatalf("Process(/resume) returned %v", err)
	}
	if !strings.Contains(resp, "Resuming from checkpoint") {
		t.Errorf("expected 'Resuming from checkpoint' in response, got: %s", resp)
	}
}

// iterationLimitAgent builds an agent whose provider never stops calling tools,
// so reactLoop always reaches the max-iteration checkpoint path.
func iterationLimitAgent(t *testing.T, sm SessionManager) *Agent {
	t.Helper()
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()

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
		},
	}
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, sm, newMockLogger())
	agent.SetMaxIterations(2)
	return agent
}

// TestIterationLimitOffersResumeOnlyWhenCheckpointPersisted is the #244
// regression. The reply told the user to type /resume even when the checkpoint
// save had failed, and /resume then answered "No checkpoint found" — the
// interrupted task was gone with no error anywhere. The suggestion must track
// whether the checkpoint actually landed.
func TestIterationLimitOffersResumeOnlyWhenCheckpointPersisted(t *testing.T) {
	const resumeHint = "/resume"

	t.Run("saved", func(t *testing.T) {
		sm := newMockSessionManager()
		agent := iterationLimitAgent(t, sm)

		resp, err := agent.Process(context.Background(), bus.InboundMessage{
			SenderID: "user", Channel: "cli", Content: "loop forever", Timestamp: time.Now(),
		})
		if err != nil {
			t.Fatalf("Process returned %v", err)
		}
		if !strings.Contains(resp, "max iteration limit") {
			t.Fatalf("expected the iteration-limit reply, got: %s", resp)
		}
		if !strings.Contains(resp, resumeHint) {
			t.Errorf("checkpoint saved, so the reply must offer %s; got: %s", resumeHint, resp)
		}
	})

	t.Run("save fails", func(t *testing.T) {
		sm := newMockSessionManager()
		agent := iterationLimitAgent(t, sm)
		sm.mu.Lock()
		sm.saveErr = errors.New("disk full")
		sm.mu.Unlock()

		resp, err := agent.Process(context.Background(), bus.InboundMessage{
			SenderID: "user", Channel: "cli", Content: "loop forever", Timestamp: time.Now(),
		})
		if err != nil {
			t.Fatalf("Process returned %v", err)
		}
		if !strings.Contains(resp, "max iteration limit") {
			t.Fatalf("expected the iteration-limit reply, got: %s", resp)
		}
		if strings.Contains(resp, resumeHint) {
			t.Errorf("checkpoint save failed, so the reply must not offer %s; got: %s", resumeHint, resp)
		}
	})

	// No session manager at all is the second way not to have a checkpoint. An
	// `err == nil` gate reads it as success, and /resume then answers "session
	// manager not initialized" — the same dead end as a failed save.
	t.Run("no session manager", func(t *testing.T) {
		sm := newMockSessionManager()
		agent := iterationLimitAgent(t, sm)
		sess, err := sm.GetOrCreate(context.Background(), "cli:user")
		if err != nil {
			t.Fatalf("GetOrCreate: %v", err)
		}
		agent.sessions = nil

		resp, err := agent.reactLoop(context.Background(), nil, sess, "cli", "user", "loop forever", &compactionState{})
		if err != nil {
			t.Fatalf("reactLoop returned %v", err)
		}
		if !strings.Contains(resp, "max iteration limit") {
			t.Fatalf("expected the iteration-limit reply, got: %s", resp)
		}
		if strings.Contains(resp, resumeHint) {
			t.Errorf("no session manager, so the reply must not offer %s; got: %s", resumeHint, resp)
		}
	})
}
