package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/session"
)

// recordingAgent remembers the message it was asked to process.
type recordingAgent struct {
	msgs []bus.InboundMessage
}

func (m *recordingAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	m.msgs = append(m.msgs, msg)
	return "reply: " + msg.Content, nil
}

// useTempHome points the recorded-session file somewhere of the test's own
// and restores it afterwards.
func useTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	old := cliHomeDir
	cliHomeDir = func() string { return home }
	t.Cleanup(func() { cliHomeDir = old })
	return home
}

// TestSessionsSwitchRecordsTheChosenSession is the point of the feature: the
// choice outlives the command that made it, so the next conversation can pick
// it up.
func TestSessionsSwitchRecordsTheChosenSession(t *testing.T) {
	home := useTempHome(t)

	if err := writeCurrentSession(home, "cli:someone_else"); err != nil {
		t.Fatalf("writeCurrentSession: %v", err)
	}
	if got := currentCLISessionKey(); got != "cli:someone_else" {
		t.Errorf("the chosen session must be picked up, got %q", got)
	}

	// The file is owner-only: it names a conversation.
	info, err := os.Stat(filepath.Join(home, currentSessionFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the chosen session file must be owner-only, got %o", perm)
	}
}

// TestCurrentCLISessionKeyDefaultsToTheHistoricalSession: with nothing
// chosen, the CLI talks in cli:cli_user, which is what it always did.
func TestCurrentCLISessionKeyDefaultsToTheHistoricalSession(t *testing.T) {
	useTempHome(t)

	if got := currentCLISessionKey(); got != defaultCLIKey {
		t.Errorf("with nothing chosen the session must be %q, got %q", defaultCLIKey, got)
	}
}

// TestCLISessionKeyRejectsAMalformedKey: a key that names no channel or no
// sender is not a session, and must not send the conversation somewhere
// unexpected.
func TestCLISessionKeyRejectsAMalformedKey(t *testing.T) {
	for _, key := range []string{"", ":sender", "channel:"} {
		if got := cliSessionKey(key); got != defaultCLIKey {
			t.Errorf("key %q must fall back to %q, got %q", key, defaultCLIKey, got)
		}
	}
	if got := cliSessionKey("cli:someone_else"); got != "cli:someone_else" {
		t.Errorf("a full key must be kept, got %q", got)
	}
	if got := cliSessionKey("someone_else"); got != "cli:someone_else" {
		t.Errorf("a bare sender must be read as a cli session, got %q", got)
	}
}

// TestClearCurrentSessionGoesBackToTheDefault: without a way back, the new
// command would be a one-way door.
func TestClearCurrentSessionGoesBackToTheDefault(t *testing.T) {
	home := useTempHome(t)

	if err := writeCurrentSession(home, "cli:someone_else"); err != nil {
		t.Fatalf("writeCurrentSession: %v", err)
	}
	if err := clearCurrentSession(home); err != nil {
		t.Fatalf("clearCurrentSession: %v", err)
	}
	if got := currentCLISessionKey(); got != defaultCLIKey {
		t.Errorf("after a reset the session must be %q, got %q", defaultCLIKey, got)
	}
	// Clearing twice is not an error: the file is already gone.
	if err := clearCurrentSession(home); err != nil {
		t.Errorf("clearing an absent choice must not fail: %v", err)
	}
}

// TestRunAgentLoopTalksInTheChosenSession is the acceptance criterion that
// matters: resuming a session restores the conversation, so the next message
// continues it. The loop must send the message to the chosen session, not to
// cli:cli_user.
func TestRunAgentLoopTalksInTheChosenSession(t *testing.T) {
	home := useTempHome(t)
	if err := writeCurrentSession(home, "cli:someone_else"); err != nil {
		t.Fatalf("writeCurrentSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var out bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &recordingAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &out, mock, nil, false); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(mock.msgs) != 1 {
		t.Fatalf("the loop must process exactly one message, got %d", len(mock.msgs))
	}
	got := mock.msgs[0]
	if got.Channel != "cli" || got.SenderID != "someone_else" {
		t.Errorf("the message must go to the chosen session, got channel=%q sender=%q", got.Channel, got.SenderID)
	}
}

// TestRunAgentLoopUsesTheDefaultSessionWhenNothingIsChosen: the historical
// behaviour must not change for the people who never use the new command.
func TestRunAgentLoopUsesTheDefaultSessionWhenNothingIsChosen(t *testing.T) {
	useTempHome(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var out bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &recordingAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &out, mock, nil, false); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(mock.msgs) != 1 {
		t.Fatalf("the loop must process exactly one message, got %d", len(mock.msgs))
	}
	if got := mock.msgs[0]; got.Channel != "cli" || got.SenderID != "cli_user" {
		t.Errorf("the default session must be cli:cli_user, got channel=%q sender=%q", got.Channel, got.SenderID)
	}
}

// TestSessionRecapShowsTheChosenSession: the recap is how the user tells
// which conversation they are in, so it must come from the chosen session.
func TestSessionRecapShowsTheChosenSession(t *testing.T) {
	home := useTempHome(t)
	if err := writeCurrentSession(home, "cli:someone_else"); err != nil {
		t.Fatalf("writeCurrentSession: %v", err)
	}

	dir := t.TempDir()
	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	sess := session.NewSession("cli:someone_else")
	sess.AddMessage(session.Message{Role: session.RoleUser, Content: "what is the plan", Timestamp: time.Now()})
	sess.AddMessage(session.Message{Role: session.RoleAssistant, Content: "Ship phase three.", Timestamp: time.Now()})
	if err := mgr.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var out bytes.Buffer
	printSessionRecap(context.Background(), mgr, &out)
	if !strings.Contains(out.String(), "Ship phase three.") {
		t.Errorf("the recap must come from the chosen session, got:\n%s", out.String())
	}
}
