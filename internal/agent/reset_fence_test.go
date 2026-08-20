package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/session"
)

// A reset must not queue behind the turn the user is trying to escape (#319).
//
// The key is held for the whole test, so a /new routed through LockSession
// cannot acquire it and hits its own deadline instead — reported in band as
// ReplyPrefix text. Route it ahead of the lock and it answers immediately.
func TestResetCommandIsNotBlockedByAHeldSessionLock(t *testing.T) {
	InvalidatePromptCache()

	mgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	a := NewAgent(cfg, &slowEchoProvider{}, &mockToolExecutor{}, mgr, newMockLogger())

	release, err := mgr.LockSession(context.Background(), "api:stuck")
	if err != nil {
		t.Fatalf("LockSession: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan string, 1)
	go func() {
		resp, err := a.Process(ctx, bus.InboundMessage{
			Channel: "api", SenderID: "stuck", Content: "/new", Timestamp: time.Now(),
		})
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- resp
	}()

	select {
	case got := <-done:
		if strings.HasPrefix(got, ReplyPrefix) {
			t.Fatalf("/new was blocked by the held lock: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("/new never returned while the session lock was held")
	}
}

// The reset clears the transcript and a turn that was already running must not
// write it back. Process must still report success: a dropped stale write is
// the fence working, not a failure the user should see.
func TestAnInFlightTurnCannotResurrectAResetTranscript(t *testing.T) {
	InvalidatePromptCache()

	dir := t.TempDir()
	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	a := NewAgent(cfg, &slowEchoProvider{delay: 200 * time.Millisecond},
		&mockToolExecutor{}, mgr, newMockLogger())

	turn := make(chan error, 1)
	go func() {
		_, err := a.Process(context.Background(), bus.InboundMessage{
			Channel: "api", SenderID: "raced", Content: "long question", Timestamp: time.Now(),
		})
		turn <- err
	}()

	// Land the reset while that turn is still waiting on the provider.
	time.Sleep(50 * time.Millisecond)
	if _, err := a.Process(context.Background(), bus.InboundMessage{
		Channel: "api", SenderID: "raced", Content: "/new", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("/new: %v", err)
	}

	if err := <-turn; err != nil {
		t.Fatalf("in-flight turn reported an error for a dropped stale write: %v", err)
	}

	fresh, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager (fresh): %v", err)
	}
	sess, err := fresh.Load(context.Background(), "api:raced")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, m := range sess.Messages {
		if strings.Contains(m.Content, "long question") {
			t.Fatalf("the reset transcript was resurrected by the in-flight turn: %d messages", len(sess.Messages))
		}
	}
}
