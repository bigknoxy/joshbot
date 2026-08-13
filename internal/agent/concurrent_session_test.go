package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

// slowEchoProvider answers with the last user message, after a delay wide
// enough that two concurrent turns are certainly overlapping when they save.
//
// Without the delay the race is real but rarely observed: the load→save span is
// microseconds, so an unsynchronised run passes most of the time and the test
// reports "fixed" on a build that is not. The delay makes the failure
// deterministic rather than probabilistic.
type slowEchoProvider struct{ delay time.Duration }

func (p *slowEchoProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	last := ""
	for _, m := range req.Messages {
		if m.Role == providers.RoleUser {
			last = m.Content
		}
	}
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &providers.ChatResponse{Choices: []providers.Choice{{
		Message: providers.Message{Role: providers.RoleAssistant, Content: "reply to " + last},
	}}}, nil
}

func (p *slowEchoProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, providers.ErrStreamingUnsupported
}

func (p *slowEchoProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", nil
}
func (p *slowEchoProvider) Name() string             { return "slow-echo" }
func (p *slowEchoProvider) Config() providers.Config { return providers.Config{} }

// TestConcurrentTurnsOnOneSessionKeyKeepEveryMessage is the regression test for
// #236.
//
// It uses the real disk-backed session.Manager on purpose: the bug is in the
// read-modify-write cycle against the session file, and the in-memory test
// double has neither the cycle nor the file. Remove Agent.Process's
// SessionLocker acquisition and this fails — each turn loads the same prefix
// and the later save publishes a session missing the earlier turn entirely.
func TestConcurrentTurnsOnOneSessionKeyKeepEveryMessage(t *testing.T) {
	InvalidatePromptCache()

	dir := t.TempDir()
	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	a := NewAgent(cfg, &slowEchoProvider{delay: 50 * time.Millisecond},
		&mockToolExecutor{}, mgr, newMockLogger())

	const turns = 4
	var wg sync.WaitGroup
	for i := 0; i < turns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := bus.InboundMessage{
				Channel:   "api",
				SenderID:  "shared-user",
				Content:   fmt.Sprintf("turn-%d", i),
				Timestamp: time.Now(),
			}
			if _, err := a.Process(context.Background(), msg); err != nil {
				t.Errorf("Process(turn-%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// Read back through a fresh manager so the assertion is about what landed
	// on disk, not about anything cached in the one under test.
	fresh, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager (fresh): %v", err)
	}
	sess, err := fresh.Load(context.Background(), "api:shared-user")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for i := 0; i < turns; i++ {
		want := fmt.Sprintf("turn-%d", i)
		found := false
		for _, m := range sess.Messages {
			if strings.TrimSpace(m.Content) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("user message %q is missing from the saved session; "+
				"a concurrent turn overwrote it (session has %d messages)",
				want, len(sess.Messages))
		}
	}
}

// TestResumeDoesNotDeadlockAgainstItsOwnSessionLock pins the reason
// handleResumeCommand calls the unlocked a.process rather than a.Process.
//
// /resume runs the resumed turn back through the ReAct loop. With a
// non-reentrant per-key lock held by Process, routing that through Process
// again would block forever on a lock this goroutine already owns — and the
// symptom is a hang, which a test without a deadline reports as a timeout in
// some unrelated package rather than as this bug.
func TestResumeDoesNotDeadlockAgainstItsOwnSessionLock(t *testing.T) {
	InvalidatePromptCache()

	mgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	a := NewAgent(cfg, &slowEchoProvider{}, &mockToolExecutor{}, mgr, newMockLogger())

	seed, err := mgr.GetOrCreate(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	seed.Checkpoint = &session.Checkpoint{
		Iteration:     3,
		MaxIterations: 50,
		CreatedAt:     time.Now(),
		UserMessage:   "the interrupted task",
	}
	if err := mgr.Save(context.Background(), seed); err != nil {
		t.Fatalf("Save: %v", err)
	}

	done := make(chan string, 1)
	go func() {
		resp, err := a.Process(context.Background(), cmdMsg("/resume"))
		if err != nil {
			t.Errorf("Process(/resume): %v", err)
		}
		done <- resp
	}()

	select {
	case resp := <-done:
		if !strings.Contains(resp, "Resuming from checkpoint") {
			t.Errorf("expected a resume confirmation, got: %s", resp)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("/resume deadlocked on its own session lock")
	}
}
