package session

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEventLogAppendedOnSave(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()

	sess := NewSession("cli:test1")
	sess.AddMessage(Message{
		Role:      RoleUser,
		Content:   "hello",
		Timestamp: time.Now().UTC(),
	})
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify events file exists
	eventsPath := mgr.logFilePath("cli:test1")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", eventsPath, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d event lines, want 1", len(lines))
	}
	var ev Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("Unmarshal event: %v", err)
	}
	if ev.Kind != EventMessageAdded {
		t.Errorf("ev.Kind = %v, want %v", ev.Kind, EventMessageAdded)
	}

	// Append second message
	sess.AddMessage(Message{
		Role:      RoleAssistant,
		Content:   "hi there",
		Timestamp: time.Now().UTC(),
	})
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	data, err = os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", eventsPath, err)
	}
	lines = strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d event lines, want 2", len(lines))
	}

	// Load via LoadFromLog
	loaded, err := mgr.LoadFromLog(ctx, "cli:test1")
	if err != nil {
		t.Fatalf("LoadFromLog: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("loaded.Messages count = %d, want 2", len(loaded.Messages))
	}
	if loaded.Messages[0].Content != "hello" || loaded.Messages[1].Content != "hi there" {
		t.Errorf("unexpected message contents: %+v", loaded.Messages)
	}
}

func TestEventLogLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()

	// Write snapshot manually without event log
	sess := NewSession("cli:legacy")
	sess.AddMessage(Message{
		Role:      RoleUser,
		Content:   "legacy message",
		Timestamp: time.Now().UTC(),
	})
	if err := writeFileAtomic(mgr.sessionFilePath("cli:legacy"), []byte(`{"role":"user","content":"legacy message"}`+"\n"), sessionFileMode); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	// LoadFromLog should fall back cleanly
	loaded, err := mgr.LoadFromLog(ctx, "cli:legacy")
	if err != nil {
		t.Fatalf("LoadFromLog legacy: %v", err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "legacy message" {
		t.Fatalf("unexpected loaded legacy session: %+v", loaded)
	}
}

func TestEventLogFailClosedOnUnknownRecord(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()

	sess := NewSession("cli:corrupt")
	sess.AddMessage(Message{
		Role:      RoleUser,
		Content:   "msg",
		Timestamp: time.Now().UTC(),
	})
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Corrupt event log with unparseable line
	eventsPath := mgr.logFilePath("cli:corrupt")
	if err := os.WriteFile(eventsPath, []byte("NOT JSON\n"), sessionFileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = mgr.LoadFromLog(ctx, "cli:corrupt")
	if err == nil {
		t.Fatal("expected LoadFromLog to fail on corrupt event line")
	}

	// Verify original was quarantined
	quarantine := eventsPath + ".corrupt"
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("expected quarantine file %s to exist: %v", quarantine, err)
	}
}

func TestEventLogCompactionAndResetFolding(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()

	sess := NewSession("cli:fold")
	sess.AddMessage(Message{Role: RoleUser, Content: "msg 1", Timestamp: time.Now().UTC()})
	sess.AddMessage(Message{Role: RoleAssistant, Content: "msg 2", Timestamp: time.Now().UTC()})
	sess.AddMessage(Message{Role: RoleUser, Content: "msg 3", Timestamp: time.Now().UTC()})
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Perform compaction: replace first 2 messages with compaction envelope
	sess.Messages = []Message{
		{
			Role:       RoleUser,
			Content:    CompactionEnvelope("summary of msg 1 and 2"),
			Compaction: true,
			Timestamp:  time.Now().UTC(),
		},
		{
			Role:      RoleUser,
			Content:   "msg 3",
			Timestamp: time.Now().UTC(),
		},
	}
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatalf("Save compacted: %v", err)
	}

	// Replay projection and assert consistency
	loaded, err := mgr.LoadFromLog(ctx, "cli:fold")
	if err != nil {
		t.Fatalf("LoadFromLog compacted: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(loaded.Messages))
	}
	if !loaded.Messages[0].Compaction {
		t.Errorf("expected message 0 to have Compaction=true")
	}

	// Test Reset via ResetConversation
	resetSess, err := mgr.ResetConversation(ctx, "cli:fold")
	if err != nil {
		t.Fatalf("ResetConversation: %v", err)
	}
	if len(resetSess.Messages) != 0 {
		t.Fatalf("expected 0 messages after reset, got %d", len(resetSess.Messages))
	}
	if resetSess.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", resetSess.Generation)
	}

	// Load from log after reset
	loadedReset, err := mgr.LoadFromLog(ctx, "cli:fold")
	if err != nil {
		t.Fatalf("LoadFromLog after reset: %v", err)
	}
	if len(loadedReset.Messages) != 0 {
		t.Fatalf("expected 0 messages after reset from log, got %d", len(loadedReset.Messages))
	}
	if loadedReset.Generation != 1 {
		t.Fatalf("expected generation 1 from log, got %d", loadedReset.Generation)
	}
}

func TestReplayHumanReadable(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()

	sess := NewSession("cli:replay")
	sess.AddMessage(Message{Role: RoleUser, Content: "hello replay", Timestamp: time.Now().UTC()})
	sess.ConversationTopic = "Greetings"
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	replay, err := mgr.Replay(ctx, "cli:replay")
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !strings.Contains(replay, "message_added") {
		t.Errorf("replay missing message_added: %s", replay)
	}
	if !strings.Contains(replay, "hello replay") {
		t.Errorf("replay missing content: %s", replay)
	}
}
