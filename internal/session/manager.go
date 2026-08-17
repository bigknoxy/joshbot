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
	// ErrKeyLockBusy is returned when a session key already has
	// MaxConcurrentTurnsPerKey turns in flight and a further turn would exceed
	// that cap. It is the backpressure a key's per-turn lock applies to a flood
	// on one conversation, rather than that one sender queuing an unbounded
	// number of waiters and stalling the bus dispatcher for every other user
	// (issue #245).
	ErrKeyLockBusy = errors.New("session key has too many concurrent turns in flight")
)

// Manager handles session persistence.
type Manager struct {
	sessionsDir string
	mu          sync.RWMutex

	// keys serialises whole turns per session id. mu guards a single Load or
	// Save; keys guards the load→modify→save span that spans several of them.
	keys *keyLock
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
		keys:        newKeyLock(),
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
	// Flush to disk before the rename. Without this the rename can become
	// visible while the data behind it has not landed, which on power loss
	// publishes an empty or truncated file over a good one — the exact
	// outcome the atomic write exists to prevent.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to sync temporary file: %w", err)
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

// Archiver is implemented by session stores that can preserve messages which
// are about to be removed from the live session.
//
// It is an optional interface, checked with a type assertion, so that
// alternative SessionManager implementations (including test doubles) are not
// forced to implement it. A store that does not implement it simply drops the
// summarized messages when a compaction is applied.
type Archiver interface {
	Archive(ctx context.Context, sessionID string, msgs []Message) error
}

// archiveFilePath returns the append-only archive for a session's compacted
// history.
func (m *Manager) archiveFilePath(sessionID string) string {
	return filepath.Join(m.sessionsDir, fmt.Sprintf("%s.history.jsonl", sessionID))
}

// Archive appends msgs to the session's history archive.
//
// Compaction replaces a run of messages with a summary, which shrinks the live
// session file. Without this the original messages would be destroyed: the user
// asked for a smaller context window, not for their conversation to be deleted.
// The archive is append-only and never read back by the agent.
func (m *Manager) Archive(ctx context.Context, sessionID string, msgs []Message) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ErrContextCancelled
	default:
	}

	if len(msgs) == 0 {
		return nil
	}

	var buf strings.Builder
	for _, msg := range msgs {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to marshal archived message: %w", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Append rather than rewrite: the archive is the only remaining copy, so a
	// truncating write that failed part-way would lose it.
	f, err := os.OpenFile(m.archiveFilePath(sessionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, sessionFileMode)
	if err != nil {
		return fmt.Errorf("failed to open archive file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(buf.String()); err != nil {
		return fmt.Errorf("failed to append to archive file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync archive file: %w", err)
	}
	return nil
}

// Load loads a session from disk.
func (m *Manager) Load(ctx context.Context, sessionID string) (*Session, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
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
		// The first quarantine copy is the valuable one: it holds the
		// conversation as it stood before any lines were dropped. Later loads
		// read the already-repaired file, so overwriting would replace the
		// original evidence with a strictly poorer copy of it.
		if _, statErr := os.Stat(quarantine); statErr == nil {
			log.Warnf("session %s: %d unreadable line(s) skipped; earlier quarantine at %s kept",
				sessionID, corrupt, quarantine)
		} else if err := writeFileAtomic(quarantine, data, sessionFileMode); err != nil {
			log.Warnf("session %s: %d unreadable line(s) skipped; failed to quarantine original: %v",
				sessionID, corrupt, err)
		} else {
			log.Warnf("session %s: %d unreadable line(s) skipped; original preserved at %s",
				sessionID, corrupt, quarantine)
		}
	}

	// First message determines created_at, last message determines updated_at.
	// An empty file still falls through to the metadata load below rather than
	// returning early: the sidecar carries the checkpoint, model override and
	// personality, and a session can legitimately hold those with no messages
	// yet. Returning early here dropped all of them silently.
	createdAt := time.Now().UTC()
	updatedAt := createdAt
	if len(messages) > 0 {
		createdAt = messages[0].Timestamp
		updatedAt = messages[len(messages)-1].Timestamp
	} else {
		messages = []Message{}
	}

	sess := &Session{
		ID:        sessionID,
		Messages:  messages,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}

	// Load optional metadata file for conversation topic/context.
	//
	// A missing sidecar is the normal case and stays silent. Anything else is
	// reported: the sidecar carries the model override, personality and
	// checkpoint, and losing it produces no visible error at all — the user's
	// /model appears to take, reverts on the next load, and takes again. The
	// load itself stays lenient, matching the transcript path above.
	metaPath := m.metadataFilePath(sessionID)
	metaData, metaErr := os.ReadFile(metaPath)
	if metaErr != nil && !os.IsNotExist(metaErr) {
		log.Warnf("session %s: metadata sidecar unreadable (%v); model override, personality and checkpoint not restored",
			sessionID, metaErr)
	}
	if metaErr == nil {
		var meta struct {
			ConversationTopic   string            `json:"conversation_topic,omitempty"`
			ConversationContext map[string]string `json:"conversation_context,omitempty"`
			ModelOverride       string            `json:"model_override,omitempty"`
			Personality         string            `json:"personality,omitempty"`
			Checkpoint          *Checkpoint       `json:"checkpoint,omitempty"`
		}
		if err := json.Unmarshal(metaData, &meta); err != nil {
			log.Warnf("session %s: metadata sidecar unparseable (%v); model override, personality and checkpoint not restored",
				sessionID, err)
		} else {
			sess.ConversationTopic = meta.ConversationTopic
			sess.ConversationContext = meta.ConversationContext
			sess.ModelOverride = meta.ModelOverride
			sess.Personality = meta.Personality
			sess.Checkpoint = meta.Checkpoint
		}
	}

	return sess, nil
}

// Save atomically saves a session to disk.
func (m *Manager) Save(ctx context.Context, s *Session) error {
	if s == nil {
		return errors.New("session is nil")
	}
	if err := ValidateSessionID(s.ID); err != nil {
		return err
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

	// Save conversation metadata separately if present. When every field is
	// empty the sidecar is removed instead of written: a stale sidecar would
	// otherwise re-inject a cleared model override or personality on the next
	// Load, so `/personality none` or `/model x --global` (which clears the
	// session override) would silently do nothing after a restart.
	if s.ConversationTopic != "" || len(s.ConversationContext) > 0 || s.ModelOverride != "" || s.Personality != "" || s.Checkpoint != nil {
		meta := struct {
			ConversationTopic   string            `json:"conversation_topic,omitempty"`
			ConversationContext map[string]string `json:"conversation_context,omitempty"`
			ModelOverride       string            `json:"model_override,omitempty"`
			Personality         string            `json:"personality,omitempty"`
			Checkpoint          *Checkpoint       `json:"checkpoint,omitempty"`
		}{
			ConversationTopic:   s.ConversationTopic,
			ConversationContext: s.ConversationContext,
			ModelOverride:       s.ModelOverride,
			Personality:         s.Personality,
			Checkpoint:          s.Checkpoint,
		}
		metaData, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("failed to marshal session metadata: %w", err)
		}
		if err := writeFileAtomic(m.metadataFilePath(s.ID), metaData, sessionFileMode); err != nil {
			return err
		}
	} else if err := os.Remove(m.metadataFilePath(s.ID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale session metadata: %w", err)
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
		// isSessionFile excludes the sidecars that also end in ".jsonl" —
		// notably the compaction archive, which was otherwise reported as a
		// session named "<id>.history".
		if sessionID, ok := isSessionFile(entry.Name()); ok {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}

	return sessionIDs, nil
}

// Delete removes a session from disk.
func (m *Manager) Delete(ctx context.Context, sessionID string) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
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

	// The compaction archive holds the earlier part of this same conversation,
	// so an explicit delete must take it too. Leaving it behind also meant the
	// next conversation under this channel:senderID inherited it.
	_ = os.Remove(m.archiveFilePath(sessionID))

	// The quarantine copy holds the same conversation as the file just
	// deleted. Leaving it behind would mean "delete this session" silently
	// kept a verbatim transcript on disk. Quarantine survives an unreadable
	// load, not an explicit delete.
	_ = os.Remove(m.quarantineFilePath(sessionID))

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
