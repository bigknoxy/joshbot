package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bigknoxy/joshbot/internal/log"
)

var (
	// ErrSessionNotFound is returned when a session is not found.
	ErrSessionNotFound = errors.New("session not found")
	// ErrInvalidSessionID is returned when a session ID is invalid.
	ErrInvalidSessionID = errors.New("invalid session ID")
	// ErrContextCancelled is returned when the context is cancelled.
	ErrContextCancelled = errors.New("context cancelled")
)

// Manager handles session persistence.
type Manager struct {
	sessionsDir string
	mu          sync.RWMutex
}

// NewManager creates a new session manager with the given sessions directory.
// If sessionsDir is empty, it defaults to ~/.joshbot/sessions.
func NewManager(sessionsDir string) (*Manager, error) {
	if sessionsDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		sessionsDir = filepath.Join(homeDir, ".joshbot", "sessions")
	}

	// Ensure the sessions directory exists
	if err := os.MkdirAll(sessionsDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory: %w", err)
	}

	return &Manager{
		sessionsDir: sessionsDir,
	}, nil
}

// sessionFilePath returns the file path for a session's JSONL messages.
func (m *Manager) sessionFilePath(sessionID string) string {
	return filepath.Join(m.sessionsDir, fmt.Sprintf("%s.jsonl", sessionID))
}

// metadataFilePath returns the file path for session metadata.
func (m *Manager) metadataFilePath(sessionID string) string {
	return filepath.Join(m.sessionsDir, fmt.Sprintf("%s.meta.json", sessionID))
}

// quarantineFilePath returns the file path a corrupt session is preserved at.
func (m *Manager) quarantineFilePath(sessionID string) string {
	return filepath.Join(m.sessionsDir, fmt.Sprintf("%s.jsonl.corrupt", sessionID))
}

// sessionFileMode is deliberately owner-only: session files hold the full
// conversation, which routinely contains credentials, personal data and tool
// output. 0644 would expose every conversation to any local account.
const sessionFileMode = 0o600

// writeFileAtomic writes data to path via a uniquely named temp file in the
// same directory, then renames it over the target.
//
// The temp file name must be unique per writer: `path + ".tmp"` is shared by
// every process using the same sessions directory (the gateway and a
// concurrent `agent -m` run), so two writers interleave into one temp file and
// the surviving rename publishes a torn mix of both. os.CreateTemp gives each
// writer its own file, which makes the rename genuinely atomic across
// processes, not just within one.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to set temporary file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to write temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("failed to rename temporary file: %w", err)
	}
	return nil
}

// Load loads a session from disk.
func (m *Manager) Load(ctx context.Context, sessionID string) (*Session, error) {
	if sessionID == "" {
		return nil, ErrInvalidSessionID
	}

	select {
	case <-ctx.Done():
		return nil, ErrContextCancelled
	default:
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	filePath := m.sessionFilePath(sessionID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	// Parse the JSONL file - each line is a message.
	//
	// A single unparseable line must not end the conversation. There is exactly
	// one session per channel:senderID and no CLI to delete it, so failing the
	// whole load means that user gets an error on every subsequent message with
	// no recovery path. Instead, skip the damaged lines, preserve the original
	// file for inspection, and carry on with everything that did parse.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	messages := make([]Message, 0, len(lines))
	corrupt := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			corrupt++
			continue
		}
		messages = append(messages, msg)
	}

	if corrupt > 0 {
		quarantine := m.quarantineFilePath(sessionID)
		if err := writeFileAtomic(quarantine, data, sessionFileMode); err != nil {
			log.Warnf("session %s: %d unreadable line(s) skipped; failed to quarantine original: %v",
				sessionID, corrupt, err)
		} else {
			log.Warnf("session %s: %d unreadable line(s) skipped; original preserved at %s",
				sessionID, corrupt, quarantine)
		}
	}

	if len(messages) == 0 {
		// Return empty session if file exists but is empty
		return &Session{
			ID:        sessionID,
			Messages:  []Message{},
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}, nil
	}

	// First message determines created_at, last message determines updated_at
	createdAt := messages[0].Timestamp
	updatedAt := messages[len(messages)-1].Timestamp

	sess := &Session{
		ID:        sessionID,
		Messages:  messages,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}

	// Load optional metadata file for conversation topic/context
	metaPath := m.metadataFilePath(sessionID)
	if metaData, err := os.ReadFile(metaPath); err == nil {
		var meta struct {
			ConversationTopic   string            `json:"conversation_topic,omitempty"`
			ConversationContext map[string]string `json:"conversation_context,omitempty"`
		}
		if err := json.Unmarshal(metaData, &meta); err == nil {
			sess.ConversationTopic = meta.ConversationTopic
			sess.ConversationContext = meta.ConversationContext
		}
	}

	return sess, nil
}

// Save atomically saves a session to disk.
func (m *Manager) Save(ctx context.Context, s *Session) error {
	if s == nil {
		return errors.New("session is nil")
	}

	select {
	case <-ctx.Done():
		return ErrContextCancelled
	default:
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Serialize messages to JSONL format
	var lines []string
	for _, msg := range s.Messages {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to marshal message: %w", err)
		}
		lines = append(lines, string(data))
	}

	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}

	// Atomic write: write to a unique temp file, then rename
	if err := writeFileAtomic(m.sessionFilePath(s.ID), []byte(content), sessionFileMode); err != nil {
		return err
	}

	// Save conversation metadata separately if present
	if s.ConversationTopic != "" || len(s.ConversationContext) > 0 {
		meta := struct {
			ConversationTopic   string            `json:"conversation_topic,omitempty"`
			ConversationContext map[string]string `json:"conversation_context,omitempty"`
		}{
			ConversationTopic:   s.ConversationTopic,
			ConversationContext: s.ConversationContext,
		}
		metaData, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("failed to marshal session metadata: %w", err)
		}
		if err := writeFileAtomic(m.metadataFilePath(s.ID), metaData, sessionFileMode); err != nil {
			return err
		}
	}

	return nil
}

// List returns all session IDs.
func (m *Manager) List(ctx context.Context) ([]string, error) {
	select {
	case <-ctx.Done():
		return nil, ErrContextCancelled
	default:
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	entries, err := os.ReadDir(m.sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read sessions directory: %w", err)
	}

	var sessionIDs []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".jsonl") {
			sessionID := strings.TrimSuffix(name, ".jsonl")
			if sessionID != "" {
				sessionIDs = append(sessionIDs, sessionID)
			}
		}
	}

	return sessionIDs, nil
}

// Delete removes a session from disk.
func (m *Manager) Delete(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return ErrInvalidSessionID
	}

	select {
	case <-ctx.Done():
		return ErrContextCancelled
	default:
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := m.sessionFilePath(sessionID)
	err := os.Remove(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("failed to delete session: %w", err)
	}

	// Clean up metadata file if it exists
	if metaPath := m.metadataFilePath(sessionID); metaPath != "" {
		_ = os.Remove(metaPath)
	}

	return nil
}

// GetOrCreate returns an existing session or creates a new one.
func (m *Manager) GetOrCreate(ctx context.Context, sessionID string) (*Session, error) {
	if sessionID == "" {
		// Generate a new UUID if no session ID provided
		sessionID = uuid.New().String()
	}

	select {
	case <-ctx.Done():
		return nil, ErrContextCancelled
	default:
	}

	// Try to load existing session
	session, err := m.Load(ctx, sessionID)
	if err == nil {
		return session, nil
	}

	if err != ErrSessionNotFound {
		return nil, err
	}

	// Session doesn't exist, create a new one
	session = NewSession(sessionID)
	return session, nil
}

// SessionsDir returns the sessions directory path.
func (m *Manager) SessionsDir() string {
	return m.sessionsDir
}
