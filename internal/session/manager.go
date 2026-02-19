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

// sessionFilePath returns the file path for a session.
func (m *Manager) sessionFilePath(sessionID string) string {
	return filepath.Join(m.sessionsDir, fmt.Sprintf("%s.jsonl", sessionID))
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

	// Parse the JSONL file - each line is a message
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	messages := make([]Message, 0, len(lines))

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("failed to parse message at line %d: %w", i+1, err)
		}
		messages = append(messages, msg)
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

	return &Session{
		ID:        sessionID,
		Messages:  messages,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
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

	// Atomic write: write to temp file, then rename
	filePath := m.sessionFilePath(s.ID)
	tmpFile := filePath + ".tmp"

	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write temporary file: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		// Clean up temp file on failure
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename temporary file: %w", err)
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
