package session

import (
	"bufio"
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
	// ErrSessionSuperseded is returned by Save when the session on disk has a
	// newer generation than the one being written — a /new reset landed while
	// this turn was running, and writing would resurrect the cleared
	// transcript (#319).
	ErrSessionSuperseded = errors.New("session superseded by a reset")
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

// logFilePath returns the file path for a session's append-only event log.
func (m *Manager) logFilePath(sessionID string) string {
	return filepath.Join(m.sessionsDir, fmt.Sprintf("%s.events.jsonl", sessionID))
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
			ConversationTopic    string            `json:"conversation_topic,omitempty"`
			ConversationContext  map[string]string `json:"conversation_context,omitempty"`
			ModelOverride        string            `json:"model_override,omitempty"`
			Personality          string            `json:"personality,omitempty"`
			Checkpoint           *Checkpoint       `json:"checkpoint,omitempty"`
			Generation           int               `json:"generation,omitempty"`
			FallbackNoticed      string            `json:"fallback_noticed,omitempty"`
			PendingSkillsNoticed []string          `json:"pending_skills_noticed,omitempty"`
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
			sess.Generation = meta.Generation
			sess.FallbackNoticed = meta.FallbackNoticed
			sess.PendingSkillsNoticed = meta.PendingSkillsNoticed
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

	// Refuse a write from a generation the reset has moved past. A /new is not
	// serialised against a running turn on purpose — a reset that queues behind
	// a stuck turn is not a reset — so this is the only thing standing between
	// the cleared transcript and the in-flight turn that loaded it (#319).
	if onDisk := m.readGenerationLocked(s.ID); onDisk > s.Generation {
		return fmt.Errorf("%w: on disk %d, writing %d", ErrSessionSuperseded, onDisk, s.Generation)
	}

	// Load the existing session to compute what changed (for event log).
	// This is done under the lock to avoid races with concurrent saves.
	var oldSess *Session
	if data, err := os.ReadFile(m.sessionFilePath(s.ID)); err == nil {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		messages := make([]Message, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var msg Message
			if err := json.Unmarshal([]byte(line), &msg); err == nil {
				messages = append(messages, msg)
			}
		}
		// Also load metadata to detect changes
		metaData, _ := os.ReadFile(m.metadataFilePath(s.ID))
		var meta struct {
			ConversationTopic   string            `json:"conversation_topic,omitempty"`
			ConversationContext map[string]string `json:"conversation_context,omitempty"`
			ModelOverride       string            `json:"model_override,omitempty"`
			Personality         string            `json:"personality,omitempty"`
			Checkpoint          *Checkpoint       `json:"checkpoint,omitempty"`
			Generation          int               `json:"generation,omitempty"`
			FallbackNoticed     string            `json:"fallback_noticed,omitempty"`
		}
		_ = json.Unmarshal(metaData, &meta)

		oldSess = &Session{
			ID:                  s.ID,
			Messages:            messages,
			ConversationTopic:   meta.ConversationTopic,
			ConversationContext: meta.ConversationContext,
			ModelOverride:       meta.ModelOverride,
			Personality:         meta.Personality,
			Checkpoint:          meta.Checkpoint,
			Generation:          meta.Generation,
			FallbackNoticed:     meta.FallbackNoticed,
		}
	}

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
	// Generation counts as presence: a reset clears every other field, so
	// dropping the sidecar here would lose the fence and let the very turn the
	// reset was racing write its transcript back on the next Save.
	if s.ConversationTopic != "" || len(s.ConversationContext) > 0 || s.ModelOverride != "" || s.Personality != "" || s.Checkpoint != nil || s.Generation > 0 || s.FallbackNoticed != "" || len(s.PendingSkillsNoticed) > 0 {
		meta := struct {
			ConversationTopic    string            `json:"conversation_topic,omitempty"`
			ConversationContext  map[string]string `json:"conversation_context,omitempty"`
			ModelOverride        string            `json:"model_override,omitempty"`
			Personality          string            `json:"personality,omitempty"`
			Checkpoint           *Checkpoint       `json:"checkpoint,omitempty"`
			Generation           int               `json:"generation,omitempty"`
			FallbackNoticed      string            `json:"fallback_noticed,omitempty"`
			PendingSkillsNoticed []string          `json:"pending_skills_noticed,omitempty"`
		}{
			ConversationTopic:    s.ConversationTopic,
			ConversationContext:  s.ConversationContext,
			ModelOverride:        s.ModelOverride,
			Personality:          s.Personality,
			Checkpoint:           s.Checkpoint,
			Generation:           s.Generation,
			FallbackNoticed:      s.FallbackNoticed,
			PendingSkillsNoticed: s.PendingSkillsNoticed,
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

	// Append events to the event log for changes detected
	if err := m.appendEventsLocked(ctx, s, oldSess); err != nil {
		// Event log failure is logged but doesn't fail the save — the snapshot
		// is the source of truth for reads. The event log is an audit trail.
		log.Warnf("session %s: failed to append event log: %v", s.ID, err)
	}

	return nil
}

// appendEventsLocked computes the diff between old and new session state and
// appends corresponding events to the append-only log. Caller must hold m.mu.
func (m *Manager) appendEventsLocked(ctx context.Context, newSess, oldSess *Session) error {
	if oldSess == nil {
		// New session: record a generation bump if generation > 0
		if newSess.Generation > 0 {
			if err := m.appendEventLocked(ctx, newSess.ID, Event{
				Version:   1,
				Kind:      EventGenerationBump,
				Timestamp: time.Now().UTC(),
				SessionID: newSess.ID,
				Data:      mustMarshal(EventGenerationBumpData{Generation: newSess.Generation}),
			}); err != nil {
				return err
			}
		}
		// Record all messages as added
		for _, msg := range newSess.Messages {
			if err := m.appendEventLocked(ctx, newSess.ID, Event{
				Version:   1,
				Kind:      EventMessageAdded,
				Timestamp: msg.Timestamp,
				SessionID: newSess.ID,
				Data:      mustMarshal(EventMessageAddedData{Message: msg}),
			}); err != nil {
				return err
			}
		}
		// Record metadata changes
		if newSess.ConversationTopic != "" {
			if err := m.appendEventLocked(ctx, newSess.ID, Event{
				Version:   1,
				Kind:      EventTopicChanged,
				Timestamp: time.Now().UTC(),
				SessionID: newSess.ID,
				Data:      mustMarshal(EventTopicChangedData{Topic: newSess.ConversationTopic}),
			}); err != nil {
				return err
			}
		}
		if newSess.ModelOverride != "" {
			if err := m.appendEventLocked(ctx, newSess.ID, Event{
				Version:   1,
				Kind:      EventModelOverrideChanged,
				Timestamp: time.Now().UTC(),
				SessionID: newSess.ID,
				Data:      mustMarshal(EventModelOverrideChangedData{ModelOverride: newSess.ModelOverride}),
			}); err != nil {
				return err
			}
		}
		if newSess.Personality != "" {
			if err := m.appendEventLocked(ctx, newSess.ID, Event{
				Version:   1,
				Kind:      EventPersonalityChanged,
				Timestamp: time.Now().UTC(),
				SessionID: newSess.ID,
				Data:      mustMarshal(EventPersonalityChangedData{Personality: newSess.Personality}),
			}); err != nil {
				return err
			}
		}
		if newSess.FallbackNoticed != "" {
			if err := m.appendEventLocked(ctx, newSess.ID, Event{
				Version:   1,
				Kind:      EventFallbackNoticedChanged,
				Timestamp: time.Now().UTC(),
				SessionID: newSess.ID,
				Data:      mustMarshal(EventFallbackNoticedChangedData{FallbackNoticed: newSess.FallbackNoticed}),
			}); err != nil {
				return err
			}
		}
		return nil
	}

	// Existing session: detect what changed
	// 1. New messages added (append-only at the end)
	if len(newSess.Messages) > len(oldSess.Messages) {
		for i := len(oldSess.Messages); i < len(newSess.Messages); i++ {
			msg := newSess.Messages[i]
			if err := m.appendEventLocked(ctx, newSess.ID, Event{
				Version:   1,
				Kind:      EventMessageAdded,
				Timestamp: msg.Timestamp,
				SessionID: newSess.ID,
				Data:      mustMarshal(EventMessageAddedData{Message: msg}),
			}); err != nil {
				return err
			}
		}
	}

	// 2. Compaction: detected when old session had messages that new session
	// doesn't, and the first message is a compaction record.
	if len(oldSess.Messages) > len(newSess.Messages) && len(newSess.Messages) > 0 && newSess.Messages[0].Compaction {
		// Find how many messages were replaced
		prefixLen := 0
		for i := 0; i < len(oldSess.Messages); i++ {
			if i >= len(newSess.Messages) || oldSess.Messages[i].Compaction != newSess.Messages[i].Compaction || oldSess.Messages[i].Content != newSess.Messages[i].Content {
				prefixLen = i
				break
			}
		}
		if prefixLen == 0 {
			prefixLen = len(oldSess.Messages) - len(newSess.Messages) + 1
		}
		archivedCount := prefixLen - 1 // minus the compaction record itself
		if err := m.appendEventLocked(ctx, newSess.ID, Event{
			Version:   1,
			Kind:      EventCompaction,
			Timestamp: time.Now().UTC(),
			SessionID: newSess.ID,
			Data: mustMarshal(EventCompactionData{
				Summary:       newSess.Messages[0].Content,
				PrefixLen:     prefixLen,
				ArchivedCount: archivedCount,
			}),
		}); err != nil {
			return err
		}
	}

	// 3. Reset: detected when generation increased
	if newSess.Generation > oldSess.Generation {
		// The archive file name is not directly available here, but we record the bump
		if err := m.appendEventLocked(ctx, newSess.ID, Event{
			Version:   1,
			Kind:      EventReset,
			Timestamp: time.Now().UTC(),
			SessionID: newSess.ID,
			Data:      mustMarshal(EventResetData{}),
		}); err != nil {
			return err
		}
		if err := m.appendEventLocked(ctx, newSess.ID, Event{
			Version:   1,
			Kind:      EventGenerationBump,
			Timestamp: time.Now().UTC(),
			SessionID: newSess.ID,
			Data:      mustMarshal(EventGenerationBumpData{Generation: newSess.Generation}),
		}); err != nil {
			return err
		}
	}

	// 4. Topic changed
	if newSess.ConversationTopic != oldSess.ConversationTopic && newSess.ConversationTopic != "" {
		if err := m.appendEventLocked(ctx, newSess.ID, Event{
			Version:   1,
			Kind:      EventTopicChanged,
			Timestamp: time.Now().UTC(),
			SessionID: newSess.ID,
			Data:      mustMarshal(EventTopicChangedData{Topic: newSess.ConversationTopic}),
		}); err != nil {
			return err
		}
	}

	// 5. Model override changed
	if newSess.ModelOverride != oldSess.ModelOverride {
		if err := m.appendEventLocked(ctx, newSess.ID, Event{
			Version:   1,
			Kind:      EventModelOverrideChanged,
			Timestamp: time.Now().UTC(),
			SessionID: newSess.ID,
			Data:      mustMarshal(EventModelOverrideChangedData{ModelOverride: newSess.ModelOverride}),
		}); err != nil {
			return err
		}
	}

	// 6. Personality changed
	if newSess.Personality != oldSess.Personality {
		if err := m.appendEventLocked(ctx, newSess.ID, Event{
			Version:   1,
			Kind:      EventPersonalityChanged,
			Timestamp: time.Now().UTC(),
			SessionID: newSess.ID,
			Data:      mustMarshal(EventPersonalityChangedData{Personality: newSess.Personality}),
		}); err != nil {
			return err
		}
	}

	// 7. Fallback noticed changed
	if newSess.FallbackNoticed != oldSess.FallbackNoticed {
		if err := m.appendEventLocked(ctx, newSess.ID, Event{
			Version:   1,
			Kind:      EventFallbackNoticedChanged,
			Timestamp: time.Now().UTC(),
			SessionID: newSess.ID,
			Data:      mustMarshal(EventFallbackNoticedChangedData{FallbackNoticed: newSess.FallbackNoticed}),
		}); err != nil {
			return err
		}
	}

	return nil
}

// appendEventLocked appends a single event to the session's event log.
// Caller must hold m.mu.
//
// Cross-process append interleaving:
// On POSIX compliant filesystems, writes opened with O_APPEND are atomic up to
// PIPE_BUF (typically 4096 bytes). Individual JSON event lines longer than 4096
// bytes could theoretically interleave if two separate processes append to the
// same session event log simultaneously without holding an OS-level flock.
// In practice, joshbot sessions are keyed by channel:senderID and serialized
// within process via SessionLock. If cross-process append interleaving occurs,
// lines will fail json.Unmarshal or schema verification, causing LoadFromLog
// to fail closed and quarantine the torn log (.events.jsonl.corrupt) rather than
// silently skipping damaged events.
func (m *Manager) appendEventLocked(ctx context.Context, sessionID string, event Event) error {
	select {
	case <-ctx.Done():
		return ErrContextCancelled
	default:
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	f, err := os.OpenFile(m.logFilePath(sessionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, sessionFileMode)
	if err != nil {
		return fmt.Errorf("failed to open event log: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(string(data) + "\n"); err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync event log: %w", err)
	}
	return nil
}

// mustMarshal marshals v to JSON, panicking on error. Used for event data
// where the types are known to be marshalable.
func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("event data marshal failed: %v", err))
	}
	return data
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

	// The event log and any quarantine copy also belong to this session.
	_ = os.Remove(m.logFilePath(sessionID))
	_ = os.Remove(m.logFilePath(sessionID) + ".corrupt")

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

	// Try to load existing session (loads from event log if present, or legacy snapshot)
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

// readGenerationLocked returns the generation recorded in the session's
// metadata sidecar, or 0 when there is none. Callers must hold m.mu.
//
// An unreadable or unparseable sidecar reads as generation 0, which fails
// *open* on purpose: this gate exists to drop a stale write, and refusing
// every write because a sidecar was momentarily unreadable would lose live
// conversation to protect a reset that may never have happened.
func (m *Manager) readGenerationLocked(sessionID string) int {
	path := m.metadataFilePath(sessionID)
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path built from a validated session ID
	if err != nil {
		return 0
	}
	var meta struct {
		Generation int `json:"generation,omitempty"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return 0
	}
	return meta.Generation
}

// ResetConversation clears a session's transcript and per-session overrides and bumps its
// generation, so a turn still running against the old transcript cannot write
// it back (#319). It returns the cleared session.
//
// Reset deliberately does not take the per-key turn lock: a reset that queues
// behind the stuck turn the user is trying to escape is not a reset. The
// generation fence in Save is what makes that safe.
func (m *Manager) ResetConversation(ctx context.Context, sessionID string) (*Session, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ErrContextCancelled
	default:
	}

	sess, err := m.Load(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			return nil, err
		}
		sess = NewSession(sessionID)
	}

	sess.ClearMessages()
	sess.ModelOverride = ""
	sess.Personality = ""
	sess.FallbackNoticed = ""
	sess.PendingSkillsNoticed = nil
	sess.Generation++

	if err := m.Save(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// bumpGenerationLocked replaces the session's metadata sidecar with one
// carrying only the next generation, discarding the conversation metadata that
// belonged to the transcript being put away. Callers must hold m.mu.
func (m *Manager) bumpGenerationLocked(sessionID string) error {
	next := m.readGenerationLocked(sessionID) + 1
	path := m.metadataFilePath(sessionID)
	if path == "" {
		return nil
	}
	data, err := json.Marshal(struct {
		Generation int `json:"generation,omitempty"`
	}{Generation: next})
	if err != nil {
		return fmt.Errorf("failed to marshal session metadata: %w", err)
	}
	if err := writeFileAtomic(path, data, sessionFileMode); err != nil {
		return fmt.Errorf("failed to write session metadata: %w", err)
	}
	return nil
}

// LoadFromLog loads a session by replaying the append-only event log.
// This is the new primary read path that asserts model-visible <=> logged.
// Legacy sessions without an event log fall back to Load().
func (m *Manager) LoadFromLog(ctx context.Context, sessionID string) (*Session, error) {
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

	// Check if event log exists
	logPath := m.logFilePath(sessionID)
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		// No event log: fall back to legacy Load (reads snapshot)
		return m.Load(ctx, sessionID)
	}

	// Read and replay event log
	f, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open event log: %w", err)
	}
	defer f.Close()

	sess := NewSession(sessionID)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Unknown record: fail closed, quarantine the log
			quarantine := m.logFilePath(sessionID) + ".corrupt"
			_ = os.Rename(logPath, quarantine)
			return nil, fmt.Errorf("session %s: event log line %d unparseable (%v); log quarantined at %s",
				sessionID, lineNum, err, quarantine)
		}

		// Validate version
		if event.Version != 1 {
			quarantine := m.logFilePath(sessionID) + ".corrupt"
			_ = os.Rename(logPath, quarantine)
			return nil, fmt.Errorf("session %s: event log line %d has unknown version %d; log quarantined at %s",
				sessionID, lineNum, event.Version, quarantine)
		}

		// Apply event to session
		if err := m.applyEvent(sess, event); err != nil {
			quarantine := m.logFilePath(sessionID) + ".corrupt"
			_ = os.Rename(logPath, quarantine)
			return nil, fmt.Errorf("session %s: event log line %d apply failed (%v); log quarantined at %s",
				sessionID, lineNum, err, quarantine)
		}
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("failed to read event log: %w", err)
	}

	// Verify projection matches snapshot (assert model-visible <=> logged)
	snapshot, err := m.loadSnapshotLocked(sessionID)
	if err != nil && !errors.Is(err, ErrSessionNotFound) {
		return nil, fmt.Errorf("failed to load snapshot for verification: %w", err)
	}
	if snapshot != nil {
		if err := m.verifyProjection(sess, snapshot); err != nil {
			// Projection mismatch: this is an incident, not a silent repair
			return nil, fmt.Errorf("session %s: projection verification failed: %w", sessionID, err)
		}
	}

	return sess, nil
}

// loadSnapshotLocked loads the session from the snapshot files (JSONL + metadata)
// without going through the event log. Used for verification.
func (m *Manager) loadSnapshotLocked(sessionID string) (*Session, error) {
	filePath := m.sessionFilePath(sessionID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	messages := make([]Message, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // Skip corrupt lines (Load does this too)
		}
		messages = append(messages, msg)
	}

	metaPath := m.metadataFilePath(sessionID)
	metaData, metaErr := os.ReadFile(metaPath)
	var meta struct {
		ConversationTopic   string            `json:"conversation_topic,omitempty"`
		ConversationContext map[string]string `json:"conversation_context,omitempty"`
		ModelOverride       string            `json:"model_override,omitempty"`
		Personality         string            `json:"personality,omitempty"`
		Checkpoint          *Checkpoint       `json:"checkpoint,omitempty"`
		Generation          int               `json:"generation,omitempty"`
		FallbackNoticed     string            `json:"fallback_noticed,omitempty"`
	}
	if metaErr == nil {
		_ = json.Unmarshal(metaData, &meta)
	}

	createdAt := time.Now().UTC()
	updatedAt := createdAt
	if len(messages) > 0 {
		createdAt = messages[0].Timestamp
		updatedAt = messages[len(messages)-1].Timestamp
	}

	return &Session{
		ID:                  sessionID,
		Messages:            messages,
		CreatedAt:           createdAt,
		UpdatedAt:           updatedAt,
		ConversationTopic:   meta.ConversationTopic,
		ConversationContext: meta.ConversationContext,
		ModelOverride:       meta.ModelOverride,
		Personality:         meta.Personality,
		Checkpoint:          meta.Checkpoint,
		Generation:          meta.Generation,
		FallbackNoticed:     meta.FallbackNoticed,
	}, nil
}

// applyEvent applies a single event to the session projection.
func (m *Manager) applyEvent(sess *Session, event Event) error {
	switch event.Kind {
	case EventMessageAdded:
		var data EventMessageAddedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("invalid message_added data: %w", err)
		}
		sess.AddMessage(data.Message)

	case EventCompaction:
		var data EventCompactionData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("invalid compaction data: %w", err)
		}
		// Replace prefix with compaction record
		if data.PrefixLen > 0 && data.PrefixLen <= len(sess.Messages) {
			tail := make([]Message, 0, len(sess.Messages)-data.PrefixLen+1)
			tail = append(tail, Message{
				Role:       RoleUser,
				Content:    data.Summary,
				Timestamp:  event.Timestamp,
				Compaction: true,
			})
			tail = append(tail, sess.Messages[data.PrefixLen:]...)
			sess.Messages = tail
		}

	case EventReset:
		sess.ClearMessages()
		sess.ModelOverride = ""
		sess.Personality = ""
		sess.FallbackNoticed = ""
		// Generation is bumped by EventGenerationBump that follows

	case EventGenerationBump:
		var data EventGenerationBumpData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("invalid generation_bump data: %w", err)
		}
		sess.Generation = data.Generation

	case EventTopicChanged:
		var data EventTopicChangedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("invalid topic_changed data: %w", err)
		}
		sess.ConversationTopic = data.Topic

	case EventModelOverrideChanged:
		var data EventModelOverrideChangedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("invalid model_override_changed data: %w", err)
		}
		sess.ModelOverride = data.ModelOverride

	case EventPersonalityChanged:
		var data EventPersonalityChangedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("invalid personality_changed data: %w", err)
		}
		sess.Personality = data.Personality

	case EventFallbackNoticedChanged:
		var data EventFallbackNoticedChangedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("invalid fallback_noticed_changed data: %w", err)
		}
		sess.FallbackNoticed = data.FallbackNoticed

	default:
		return fmt.Errorf("unknown event kind: %s", event.Kind)
	}
	return nil
}

// verifyProjection asserts that the projected session matches the snapshot.
// This replaces the silent repair in dropOrphanedToolResults.
func (m *Manager) verifyProjection(projected, snapshot *Session) error {
	// Compare message count
	if len(projected.Messages) != len(snapshot.Messages) {
		return fmt.Errorf("message count mismatch: projected %d, snapshot %d",
			len(projected.Messages), len(snapshot.Messages))
	}

	// Compare each message
	for i := range projected.Messages {
		p := projected.Messages[i]
		s := snapshot.Messages[i]
		if p.Role != s.Role {
			return fmt.Errorf("message %d role mismatch: projected %s, snapshot %s", i, p.Role, s.Role)
		}
		if p.Content != s.Content {
			return fmt.Errorf("message %d content mismatch", i)
		}
		if p.Compaction != s.Compaction {
			return fmt.Errorf("message %d compaction flag mismatch", i)
		}
		// Tool calls comparison (simplified)
		if len(p.ToolCalls) != len(s.ToolCalls) {
			return fmt.Errorf("message %d tool call count mismatch", i)
		}
	}

	// Compare metadata
	if projected.ConversationTopic != snapshot.ConversationTopic {
		return fmt.Errorf("topic mismatch: projected %q, snapshot %q",
			projected.ConversationTopic, snapshot.ConversationTopic)
	}
	if projected.ModelOverride != snapshot.ModelOverride {
		return fmt.Errorf("model override mismatch: projected %q, snapshot %q",
			projected.ModelOverride, snapshot.ModelOverride)
	}
	if projected.Personality != snapshot.Personality {
		return fmt.Errorf("personality mismatch: projected %q, snapshot %q",
			projected.Personality, snapshot.Personality)
	}
	if projected.Generation != snapshot.Generation {
		return fmt.Errorf("generation mismatch: projected %d, snapshot %d",
			projected.Generation, snapshot.Generation)
	}
	if projected.FallbackNoticed != snapshot.FallbackNoticed {
		return fmt.Errorf("fallback noticed mismatch: projected %q, snapshot %q",
			projected.FallbackNoticed, snapshot.FallbackNoticed)
	}

	return nil
}

// Replay returns a human-readable rendering of the event log for a session.
func (m *Manager) Replay(ctx context.Context, sessionID string) (string, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", ErrContextCancelled
	default:
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check if session exists at all
	if _, err := os.Stat(m.sessionFilePath(sessionID)); os.IsNotExist(err) {
		return "", ErrSessionNotFound
	}

	logPath := m.logFilePath(sessionID)
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return "", fmt.Errorf("no event log for session %s (legacy session)", sessionID)
	}

	f, err := os.Open(logPath)
	if err != nil {
		return "", fmt.Errorf("failed to open event log: %w", err)
	}
	defer f.Close()

	var out strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			out.WriteString(fmt.Sprintf("LINE %d: UNPARSEABLE: %v\n", lineNum, err))
			continue
		}

		out.WriteString(fmt.Sprintf("LINE %d: %s %s\n", lineNum, event.Timestamp.Format(time.RFC3339), event.Kind))

		switch event.Kind {
		case EventMessageAdded:
			var data EventMessageAddedData
			if err := json.Unmarshal(event.Data, &data); err == nil {
				out.WriteString(fmt.Sprintf("  role=%s compaction=%v content=%q\n",
					data.Message.Role, data.Message.Compaction, truncateForReplay(data.Message.Content)))
				for _, tc := range data.Message.ToolCalls {
					out.WriteString(fmt.Sprintf("  -> tool %s(%s)\n", tc.Name, string(tc.Arguments)))
				}
			}
		case EventCompaction:
			var data EventCompactionData
			if err := json.Unmarshal(event.Data, &data); err == nil {
				out.WriteString(fmt.Sprintf("  summary=%q prefix_len=%d archived=%d\n",
					truncateForReplay(data.Summary), data.PrefixLen, data.ArchivedCount))
			}
		case EventReset:
			out.WriteString("  (session reset)\n")
		case EventGenerationBump:
			var data EventGenerationBumpData
			if err := json.Unmarshal(event.Data, &data); err == nil {
				out.WriteString(fmt.Sprintf("  generation=%d\n", data.Generation))
			}
		case EventTopicChanged:
			var data EventTopicChangedData
			if err := json.Unmarshal(event.Data, &data); err == nil {
				out.WriteString(fmt.Sprintf("  topic=%q\n", data.Topic))
			}
		case EventModelOverrideChanged:
			var data EventModelOverrideChangedData
			if err := json.Unmarshal(event.Data, &data); err == nil {
				out.WriteString(fmt.Sprintf("  model_override=%q\n", data.ModelOverride))
			}
		case EventPersonalityChanged:
			var data EventPersonalityChangedData
			if err := json.Unmarshal(event.Data, &data); err == nil {
				out.WriteString(fmt.Sprintf("  personality=%q\n", data.Personality))
			}
		case EventFallbackNoticedChanged:
			var data EventFallbackNoticedChangedData
			if err := json.Unmarshal(event.Data, &data); err == nil {
				out.WriteString(fmt.Sprintf("  fallback_noticed=%q\n", data.FallbackNoticed))
			}
		default:
			out.WriteString(fmt.Sprintf("  UNKNOWN KIND: %s\n", event.Kind))
		}
		out.WriteString("\n")
	}

	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("failed to read event log: %w", err)
	}

	return out.String(), nil
}

// truncateForReplay truncates a string for human-readable replay output.
func truncateForReplay(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
