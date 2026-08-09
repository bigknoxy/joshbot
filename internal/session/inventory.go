package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File suffixes owned by this package.
//
// They are declared together because they interact: List has to distinguish a
// live session from the sidecars that sit beside it, and a suffix that merely
// ends in ".jsonl" would be mistaken for a session. That is not hypothetical —
// the compaction archive introduced in #125 ends in ".history.jsonl", and List
// reported one as a session named "<id>.history" until this was centralised.
const (
	suffixSession    = ".jsonl"
	suffixMetadata   = ".meta.json"
	suffixArchive    = ".history.jsonl"
	suffixQuarantine = ".jsonl.corrupt"
	suffixReset      = ".jsonl.archive-"
)

// Info is a read-only summary of one session on disk.
type Info struct {
	// ID is the session key, e.g. "telegram:12345".
	ID string
	// Messages is the number of parseable messages.
	Messages int
	// Bytes is the size of the live session file.
	Bytes int64
	// UpdatedAt is the file's modification time. It is used rather than the
	// last message's timestamp because a session whose messages all failed to
	// parse still has a meaningful mtime.
	UpdatedAt time.Time
	// Corrupt reports that unparseable lines were found. A quarantine file may
	// also exist from an earlier load.
	Corrupt bool
	// CorruptLines counts the unparseable lines.
	CorruptLines int
	// Compacted reports that the session carries a stored compaction record.
	Compacted bool
	// ArchiveBytes is the size of the compaction archive, if any.
	ArchiveBytes int64
	// Unreadable reports that the session could not be scanned at all, so
	// every count in this struct is unknown rather than zero. It is distinct
	// from Corrupt, which means the file was read and some lines did not
	// parse.
	Unreadable bool
}

// ValidateSessionID rejects identifiers that could escape the sessions
// directory.
//
// Session IDs are "channel:senderID" and the sender part comes from an external
// system, so it is attacker-influenced. A `/` or `..` in one would let a path
// built from it point anywhere the process can write.
func ValidateSessionID(id string) error {
	if id == "" {
		return ErrInvalidSessionID
	}
	if strings.ContainsRune(id, 0) {
		return fmt.Errorf("%w: contains a null byte", ErrInvalidSessionID)
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("%w: contains a path separator", ErrInvalidSessionID)
	}
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return fmt.Errorf("%w: contains a parent-directory reference", ErrInvalidSessionID)
	}
	return nil
}

// isSessionFile reports whether a directory entry is a live session file, and
// returns the session ID.
func isSessionFile(name string) (string, bool) {
	if !strings.HasSuffix(name, suffixSession) {
		return "", false
	}
	// A sidecar that happens to end in .jsonl is not a session.
	if strings.HasSuffix(name, suffixArchive) {
		return "", false
	}
	id := strings.TrimSuffix(name, suffixSession)
	if id == "" || strings.Contains(id, suffixReset) {
		return "", false
	}
	return id, true
}

// scanSession counts messages and unparseable lines without side effects.
//
// Load is not reused here: it quarantines corrupt input, and an inventory
// listing must not write to disk.
func scanSession(path string) (messages, corrupt int, compacted bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Tool results can be large; the default 64KiB token limit would report a
	// long but valid line as corrupt.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			corrupt++
			continue
		}
		if first && msg.Compaction {
			compacted = true
		}
		first = false
		messages++
	}
	if err := sc.Err(); err != nil {
		return messages, corrupt, compacted, err
	}
	return messages, corrupt, compacted, nil
}

// Stat returns a summary of one session without modifying anything.
func (m *Manager) Stat(ctx context.Context, sessionID string) (Info, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return Info{}, err
	}

	select {
	case <-ctx.Done():
		return Info{}, ErrContextCancelled
	default:
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.statLocked(sessionID)
}

func (m *Manager) statLocked(sessionID string) (Info, error) {
	path := m.sessionFilePath(sessionID)
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Info{}, ErrSessionNotFound
		}
		return Info{}, fmt.Errorf("failed to stat session: %w", err)
	}

	messages, corruptLines, compacted, err := scanSession(path)
	if err != nil {
		return Info{}, fmt.Errorf("failed to read session: %w", err)
	}

	info := Info{
		ID:           sessionID,
		Messages:     messages,
		Bytes:        fi.Size(),
		UpdatedAt:    fi.ModTime(),
		CorruptLines: corruptLines,
		Compacted:    compacted,
	}

	// A quarantine file from an earlier load also marks the session as having
	// been damaged, even if every remaining line now parses.
	if _, err := os.Stat(m.quarantineFilePath(sessionID)); err == nil {
		info.Corrupt = true
	}
	if corruptLines > 0 {
		info.Corrupt = true
	}
	if afi, err := os.Stat(m.archiveFilePath(sessionID)); err == nil {
		info.ArchiveBytes = afi.Size()
	}

	return info, nil
}

// ListInfo returns a summary of every session, newest first.
//
// An unreadable session is reported rather than skipped: a listing that hides
// the one broken session is worse than useless, because that is the session the
// operator is looking for.
func (m *Manager) ListInfo(ctx context.Context) ([]Info, error) {
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

	out := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := isSessionFile(entry.Name())
		if !ok {
			continue
		}
		info, err := m.statLocked(id)
		if err != nil {
			// A session that would not scan still gets its real mtime where
			// one is available. Leaving UpdatedAt at the zero time made it
			// look infinitely old, so `prune --older-than` deleted a session
			// that had merely failed to read — a transient EACCES or an
			// oversized line was enough — no matter how recent it was.
			info = Info{ID: id, Corrupt: true, Unreadable: true}
			if st, statErr := os.Stat(m.sessionFilePath(id)); statErr == nil {
				info.UpdatedAt = st.ModTime().UTC()
				info.Bytes = st.Size()
			}
			out = append(out, info)
			continue
		}
		out = append(out, info)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// PruneOlderThan deletes every session last modified before cutoff and returns
// the IDs it removed, sorted.
//
// The cutoff is a time rather than a duration so this stays free of clock
// handling and is trivially testable; the CLI turns "30d" into a time.
func (m *Manager) PruneOlderThan(ctx context.Context, cutoff time.Time) ([]string, error) {
	infos, err := m.ListInfo(ctx)
	if err != nil {
		return nil, err
	}

	var removed []string
	for _, info := range infos {
		if info.UpdatedAt.IsZero() {
			// No usable timestamp at all: age is unknown, and "unknown" must
			// not be treated as "old enough to delete".
			continue
		}
		if !info.UpdatedAt.Before(cutoff) {
			continue
		}
		if err := m.Delete(ctx, info.ID); err != nil {
			return removed, fmt.Errorf("failed to delete %s: %w", info.ID, err)
		}
		removed = append(removed, info.ID)
	}
	sort.Strings(removed)
	return removed, nil
}

// Reset archives a session's file and leaves the ID with no history.
//
// It is a rename rather than a delete: "start fresh" should not mean "destroy
// the conversation". The archived name deliberately does not end in ".jsonl",
// so the archive is never mistaken for a live session by ListInfo.
func (m *Manager) Reset(ctx context.Context, sessionID string) (string, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", ErrContextCancelled
	default:
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	live := m.sessionFilePath(sessionID)
	if _, err := os.Stat(live); err != nil {
		if os.IsNotExist(err) {
			return "", ErrSessionNotFound
		}
		return "", fmt.Errorf("failed to stat session: %w", err)
	}

	archived := live + fmt.Sprintf(".archive-%d", time.Now().UTC().UnixNano())
	if err := os.Rename(live, archived); err != nil {
		return "", fmt.Errorf("failed to archive session: %w", err)
	}

	// Conversation metadata describes the archived conversation, not the new
	// empty one.
	_ = os.Remove(m.metadataFilePath(sessionID))

	// The compaction archive belongs to the conversation being put away. Left
	// in place it would be inherited by the next conversation under the same
	// channel:senderID, so `sessions list` would report an archive of history
	// that has nothing to do with the session it is attached to. Move it
	// alongside the transcript rather than deleting it — "start fresh" should
	// not mean "destroy the conversation".
	if _, err := os.Stat(m.archiveFilePath(sessionID)); err == nil {
		_ = os.Rename(m.archiveFilePath(sessionID), archived+".history")
	}

	return filepath.Base(archived), nil
}
