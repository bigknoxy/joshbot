package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// MockLogger is a simple logger for testing.
type MockLogger struct {
	infos []string
	warns []string
}

func (m *MockLogger) Info(msg string, args ...interface{}) {
	m.infos = append(m.infos, msg)
}

func (m *MockLogger) Warn(msg string, args ...interface{}) {
	m.warns = append(m.warns, msg)
}

func TestNewSession(t *testing.T) {
	sess := NewSession("test-session-id")

	if sess.ID != "test-session-id" {
		t.Errorf("expected ID 'test-session-id', got %q", sess.ID)
	}

	if len(sess.Messages) != 0 {
		t.Errorf("expected empty messages, got %d", len(sess.Messages))
	}

	if sess.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}

	if sess.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
}

func TestSessionAddMessage(t *testing.T) {
	sess := NewSession("test-session")
	initialUpdatedAt := sess.UpdatedAt

	time.Sleep(10 * time.Millisecond) // Ensure timestamp differs

	msg := Message{
		Role:      RoleUser,
		Content:   "Hello, world!",
		Timestamp: time.Now(),
	}
	sess.AddMessage(msg)

	if len(sess.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(sess.Messages))
	}

	if sess.Messages[0].Role != RoleUser {
		t.Errorf("expected role user, got %v", sess.Messages[0].Role)
	}

	if sess.Messages[0].Content != "Hello, world!" {
		t.Errorf("expected content 'Hello, world!', got %q", sess.Messages[0].Content)
	}

	if !sess.UpdatedAt.After(initialUpdatedAt) {
		t.Error("expected UpdatedAt to be updated")
	}
}

func TestSessionGetMessages(t *testing.T) {
	sess := NewSession("test-session")

	msg1 := Message{Role: RoleUser, Content: "Hello"}
	msg2 := Message{Role: RoleAssistant, Content: "Hi there!"}
	sess.AddMessage(msg1)
	sess.AddMessage(msg2)

	messages := sess.GetMessages()

	if len(messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(messages))
	}
}

func TestSessionLastMessages(t *testing.T) {
	sess := NewSession("test-session")

	for i := 0; i < 10; i++ {
		sess.AddMessage(Message{
			Role:    RoleUser,
			Content: "Message " + string(rune('0'+i)),
		})
	}

	// Test getting last n messages
	last3 := sess.LastMessages(3)
	if len(last3) != 3 {
		t.Errorf("expected 3 messages, got %d", len(last3))
	}
	if last3[0].Content != "Message 7" {
		t.Errorf("expected 'Message 7', got %q", last3[0].Content)
	}

	// Test when n > len(messages)
	all := sess.LastMessages(100)
	if len(all) != 10 {
		t.Errorf("expected 10 messages, got %d", len(all))
	}

	// Test when n <= 0
	empty := sess.LastMessages(0)
	if len(empty) != 0 {
		t.Errorf("expected 0 messages, got %d", len(empty))
	}

	negative := sess.LastMessages(-1)
	if len(negative) != 0 {
		t.Errorf("expected 0 messages, got %d", len(negative))
	}
}

func TestSessionJSONMarshaling(t *testing.T) {
	sess := NewSession("test-session")
	sess.AddMessage(Message{
		Role:      RoleUser,
		Content:   "Test message",
		Timestamp: time.Now(),
	})

	// Test marshaling
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("failed to marshal session: %v", err)
	}

	// Test unmarshaling
	var loaded Session
	err = json.Unmarshal(data, &loaded)
	if err != nil {
		t.Fatalf("failed to unmarshal session: %v", err)
	}

	if loaded.ID != sess.ID {
		t.Errorf("expected ID %q, got %q", sess.ID, loaded.ID)
	}

	if len(loaded.Messages) != len(sess.Messages) {
		t.Errorf("expected %d messages, got %d", len(sess.Messages), len(loaded.Messages))
	}
}

func TestMessageToJSONL(t *testing.T) {
	msg := Message{
		Role:      RoleUser,
		Content:   "Test content",
		Timestamp: time.Now(),
	}

	data, err := MessageToJSONL(msg)
	if err != nil {
		t.Fatalf("failed to convert message to JSONL: %v", err)
	}

	// Verify it's valid JSON
	var parsed Message
	err = json.Unmarshal(data, &parsed)
	if err != nil {
		t.Fatalf("failed to parse JSONL data: %v", err)
	}

	if parsed.Role != msg.Role {
		t.Errorf("expected role %v, got %v", msg.Role, parsed.Role)
	}
	if parsed.Content != msg.Content {
		t.Errorf("expected content %q, got %q", msg.Content, parsed.Content)
	}
}

func TestMessageFromJSONL(t *testing.T) {
	jsonData := []byte(`{"role":"user","content":"Hello","timestamp":"2024-01-01T00:00:00Z"}`)

	msg, err := MessageFromJSONL(jsonData)
	if err != nil {
		t.Fatalf("failed to parse message from JSONL: %v", err)
	}

	if msg.Role != RoleUser {
		t.Errorf("expected role user, got %v", msg.Role)
	}
	if msg.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", msg.Content)
	}
}

func TestMessageFromJSONLError(t *testing.T) {
	invalidJSON := []byte(`{invalid json`)

	_, err := MessageFromJSONL(invalidJSON)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestRoleConstants(t *testing.T) {
	tests := []struct {
		got  Role
		want string
	}{
		{RoleUser, "user"},
		{RoleAssistant, "assistant"},
		{RoleSystem, "system"},
		{RoleTool, "tool"},
	}

	for _, tt := range tests {
		if string(tt.got) != tt.want {
			t.Errorf("expected %q, got %q", tt.want, tt.got)
		}
	}
}

func TestToolCallJSON(t *testing.T) {
	tc := ToolCall{
		ID:        "call_123",
		Name:      "filesystem",
		Arguments: json.RawMessage(`{"operation":"read_file","path":"test.txt"}`),
		Result:    "file content",
	}

	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("failed to marshal tool call: %v", err)
	}

	var loaded ToolCall
	err = json.Unmarshal(data, &loaded)
	if err != nil {
		t.Fatalf("failed to unmarshal tool call: %v", err)
	}

	if loaded.ID != tc.ID {
		t.Errorf("expected ID %q, got %q", tc.ID, loaded.ID)
	}
	if loaded.Name != tc.Name {
		t.Errorf("expected Name %q, got %q", tc.Name, loaded.Name)
	}
}

// Manager tests

func TestNewManager(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	if manager.sessionsDir != tmpDir {
		t.Errorf("expected sessions dir %q, got %q", tmpDir, manager.sessionsDir)
	}
}

func TestNewManagerDefaultDir(t *testing.T) {
	// This test uses the default directory
	// We just verify it doesn't panic
	manager, err := NewManager("")
	if err != nil {
		t.Skipf("Skipping default dir test: %v", err)
	}

	if manager == nil {
		t.Error("expected non-nil manager")
	}
}

func TestManagerSessionFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	path := manager.sessionFilePath("my-session")
	expected := filepath.Join(tmpDir, "my-session.jsonl")

	if path != expected {
		t.Errorf("expected path %q, got %q", expected, path)
	}
}

func TestManagerSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()

	// Create and save a session
	sess := NewSession("test-session")
	sess.AddMessage(Message{
		Role:      RoleUser,
		Content:   "Hello",
		Timestamp: time.Now(),
	})
	sess.AddMessage(Message{
		Role:      RoleAssistant,
		Content:   "Hi there!",
		Timestamp: time.Now(),
	})

	err = manager.Save(ctx, sess)
	if err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Load the session
	loaded, err := manager.Load(ctx, "test-session")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}

	if loaded.ID != sess.ID {
		t.Errorf("expected ID %q, got %q", sess.ID, loaded.ID)
	}

	if len(loaded.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(loaded.Messages))
	}

	if loaded.Messages[0].Content != "Hello" {
		t.Errorf("expected first message 'Hello', got %q", loaded.Messages[0].Content)
	}
}

func TestManagerLoadNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	_, err := manager.Load(ctx, "non-existent")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestManagerLoadEmptySession(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	// Create an empty file
	filePath := filepath.Join(tmpDir, "empty-session.jsonl")
	os.WriteFile(filePath, []byte(""), 0644)

	loaded, err := manager.Load(ctx, "empty-session")
	if err != nil {
		t.Fatalf("failed to load empty session: %v", err)
	}

	if len(loaded.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(loaded.Messages))
	}
}

func TestManagerDelete(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	// Save a session first
	sess := NewSession("to-delete")
	manager.Save(ctx, sess)

	// Delete it
	err := manager.Delete(ctx, "to-delete")
	if err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}

	// Verify it's gone
	_, err = manager.Load(ctx, "to-delete")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound after delete, got %v", err)
	}
}

func TestManagerDeleteNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	err := manager.Delete(ctx, "non-existent")
	if err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestManagerList(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	// Create some sessions
	manager.Save(ctx, NewSession("session-1"))
	manager.Save(ctx, NewSession("session-2"))
	manager.Save(ctx, NewSession("session-3"))

	// List them
	ids, err := manager.List(ctx)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}

	if len(ids) != 3 {
		t.Errorf("expected 3 session IDs, got %d: %v", len(ids), ids)
	}
}

func TestManagerListEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	ids, err := manager.List(ctx)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}

	if len(ids) != 0 {
		t.Errorf("expected 0 session IDs, got %d", len(ids))
	}
}

func TestManagerGetOrCreate(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	// Create a new session
	sess1, err := manager.GetOrCreate(ctx, "new-session")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	if sess1.ID != "new-session" {
		t.Errorf("expected ID 'new-session', got %q", sess1.ID)
	}

	// Get the same session
	sess2, err := manager.GetOrCreate(ctx, "new-session")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}

	if sess2.ID != "new-session" {
		t.Errorf("expected ID 'new-session', got %q", sess2.ID)
	}
}

func TestManagerGetOrCreateWithUUID(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	// Create session with empty ID (should generate UUID)
	sess, err := manager.GetOrCreate(ctx, "")
	if err != nil {
		t.Fatalf("failed to create session with empty ID: %v", err)
	}

	if sess.ID == "" {
		t.Error("expected non-empty session ID")
	}
}

func TestManagerSaveNilSession(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	err := manager.Save(ctx, nil)
	if err == nil {
		t.Error("expected error for nil session")
	}
}

func TestManagerLoadEmptyID(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	_, err := manager.Load(ctx, "")
	if err != ErrInvalidSessionID {
		t.Errorf("expected ErrInvalidSessionID, got %v", err)
	}
}

func TestManagerDeleteEmptyID(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	err := manager.Delete(ctx, "")
	if err != ErrInvalidSessionID {
		t.Errorf("expected ErrInvalidSessionID, got %v", err)
	}
}

func TestManagerConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()
	var wg sync.WaitGroup

	// Concurrent saves
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sess := NewSession("concurrent-session")
			sess.AddMessage(Message{
				Role:    RoleUser,
				Content: "Message " + string(rune('0'+i)),
			})
			manager.Save(ctx, sess)
		}(i)
	}

	wg.Wait()

	// Verify session exists
	loaded, err := manager.Load(ctx, "concurrent-session")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}

	// Note: Due to race conditions, we might have lost some writes
	// but we should have at least some messages
	if len(loaded.Messages) == 0 {
		t.Error("expected at least some messages in session")
	}
}

func TestManagerContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := manager.Load(ctx, "test")
	if err != ErrContextCancelled {
		t.Errorf("expected ErrContextCancelled, got %v", err)
	}

	err = manager.Save(ctx, NewSession("test"))
	if err != ErrContextCancelled {
		t.Errorf("expected ErrContextCancelled, got %v", err)
	}

	_, err = manager.List(ctx)
	if err != ErrContextCancelled {
		t.Errorf("expected ErrContextCancelled, got %v", err)
	}

	err = manager.Delete(ctx, "test")
	if err != ErrContextCancelled {
		t.Errorf("expected ErrContextCancelled, got %v", err)
	}
}

func TestManagerSessionsDir(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	if manager.SessionsDir() != tmpDir {
		t.Errorf("expected %q, got %q", tmpDir, manager.SessionsDir())
	}
}

func TestManagerSaveWithToolCalls(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	sess := NewSession("tool-calls-session")
	sess.AddMessage(Message{
		Role:    RoleAssistant,
		Content: "Let me check that file",
		ToolCalls: []ToolCall{
			{
				ID:        "call_1",
				Name:      "filesystem",
				Arguments: json.RawMessage(`{"operation":"read_file","path":"test.txt"}`),
			},
		},
		Timestamp: time.Now(),
	})

	err := manager.Save(ctx, sess)
	if err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := manager.Load(ctx, "tool-calls-session")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}

	if len(loaded.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(loaded.Messages))
	}

	if len(loaded.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(loaded.Messages[0].ToolCalls))
	}

	if loaded.Messages[0].ToolCalls[0].Name != "filesystem" {
		t.Errorf("expected tool name 'filesystem', got %q", loaded.Messages[0].ToolCalls[0].Name)
	}
}

func TestManagerAtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	manager, _ := NewManager(tmpDir)

	ctx := context.Background()

	// Save a session
	sess := NewSession("atomic-test")
	sess.AddMessage(Message{
		Role:      RoleUser,
		Content:   "Testing atomic writes",
		Timestamp: time.Now(),
	})

	err := manager.Save(ctx, sess)
	if err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Verify temp file was cleaned up
	files, _ := os.ReadDir(tmpDir)
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".tmp" {
			t.Error("temp file should have been cleaned up")
		}
	}
}
