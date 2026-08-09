package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedSession(t *testing.T, m *Manager, id string, msgs ...string) {
	t.Helper()
	sess := NewSession(id)
	for _, c := range msgs {
		sess.AddMessage(Message{Role: RoleUser, Content: c, Timestamp: time.Now().UTC()})
	}
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save(%s): %v", id, err)
	}
}

func TestValidateSessionID(t *testing.T) {
	valid := []string{"cli:cli_user", "telegram:12345", "a", "uuid-1234-5678", "with.dots"}
	for _, id := range valid {
		if err := ValidateSessionID(id); err != nil {
			t.Errorf("ValidateSessionID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{"", "../../etc/passwd", "a/b", `a\b`, "..", ".", "x\x00y", "foo/../bar"}
	for _, id := range invalid {
		if err := ValidateSessionID(id); err == nil {
			t.Errorf("ValidateSessionID(%q) = nil, want an error", id)
		}
	}
}

// A traversing ID must be rejected before any filesystem call, so nothing
// outside the sessions directory is read, written or deleted.
func TestTraversalTouchesNothingOutsideSessionsDir(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	m, err := NewManager(sessionsDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	victim := filepath.Join(root, "victim.jsonl")
	if err := os.WriteFile(victim, []byte("precious\n"), 0600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	const evil = "../victim"
	ctx := context.Background()

	if _, err := m.Load(ctx, evil); err == nil {
		t.Error("Load accepted a traversing session ID")
	}
	if err := m.Delete(ctx, evil); err == nil {
		t.Error("Delete accepted a traversing session ID")
	}
	if _, err := m.Stat(ctx, evil); err == nil {
		t.Error("Stat accepted a traversing session ID")
	}
	if _, err := m.Reset(ctx, evil); err == nil {
		t.Error("Reset accepted a traversing session ID")
	}
	if err := m.Save(ctx, &Session{ID: evil}); err == nil {
		t.Error("Save accepted a traversing session ID")
	}
	if err := m.Archive(ctx, evil, []Message{{Role: RoleUser, Content: "x"}}); err == nil {
		t.Error("Archive accepted a traversing session ID")
	}

	if data, err := os.ReadFile(victim); err != nil || string(data) != "precious\n" {
		t.Errorf("file outside the sessions directory was touched: %q, %v", data, err)
	}
}

// The compaction archive ends in ".jsonl" but is not a session. Before this was
// centralised, List reported it as one named "<id>.history".
func TestListIgnoresSidecars(t *testing.T) {
	m := newTestManager(t)
	seedSession(t, m, "real-session", "hello")

	// Every sidecar shape that lives beside a session.
	for _, name := range []string{
		"real-session.history.jsonl",
		"real-session.jsonl.corrupt",
		"real-session.meta.json",
		"real-session.jsonl.archive-1700000000",
	} {
		if err := os.WriteFile(filepath.Join(m.SessionsDir(), name), []byte("{}\n"), 0600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	ids, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 1 || ids[0] != "real-session" {
		t.Errorf("List returned %v, want exactly [real-session]", ids)
	}

	infos, err := m.ListInfo(context.Background())
	if err != nil {
		t.Fatalf("ListInfo: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != "real-session" {
		t.Errorf("ListInfo returned %+v, want exactly one entry for real-session", infos)
	}
}

func TestListInfoEmptyDirectory(t *testing.T) {
	m := newTestManager(t)
	infos, err := m.ListInfo(context.Background())
	if err != nil {
		t.Fatalf("ListInfo on an empty directory must not error: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected no sessions, got %+v", infos)
	}
}

func TestStatReportsCountsAndFlags(t *testing.T) {
	m := newTestManager(t)
	seedSession(t, m, "counted", "one", "two", "three")

	info, err := m.Stat(context.Background(), "counted")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Messages != 3 {
		t.Errorf("Messages = %d, want 3", info.Messages)
	}
	if info.Bytes <= 0 {
		t.Errorf("Bytes = %d, want positive", info.Bytes)
	}
	if info.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not set")
	}
	if info.Corrupt {
		t.Error("a clean session must not be flagged corrupt")
	}
	if info.Compacted {
		t.Error("a session with no compaction record must not be flagged compacted")
	}
}

// Listing must not have side effects — in particular it must not quarantine.
func TestStatDoesNotQuarantineCorruptSessions(t *testing.T) {
	m := newTestManager(t)
	id := "damaged"
	raw := `{"role":"user","content":"ok","timestamp":"2026-01-01T00:00:00Z"}` + "\n" + `{"role":"assist` + "\n"
	if err := os.WriteFile(m.sessionFilePath(id), []byte(raw), sessionFileMode); err != nil {
		t.Fatalf("seed: %v", err)
	}

	info, err := m.Stat(context.Background(), id)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.Corrupt || info.CorruptLines != 1 {
		t.Errorf("expected 1 corrupt line flagged, got %+v", info)
	}
	if info.Messages != 1 {
		t.Errorf("Messages = %d, want 1", info.Messages)
	}
	if _, err := os.Stat(m.quarantineFilePath(id)); !os.IsNotExist(err) {
		t.Error("Stat quarantined the session; a read-only inventory must not write")
	}
}

func TestStatFlagsCompactedSession(t *testing.T) {
	m := newTestManager(t)
	sess := NewSession("compacted")
	sess.AddMessage(NewCompactionRecord("a summary"))
	sess.AddMessage(Message{Role: RoleUser, Content: "after", Timestamp: time.Now().UTC()})
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := m.Stat(context.Background(), "compacted")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.Compacted {
		t.Error("expected Compacted to be true")
	}
}

func TestStatMissingSession(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Stat(context.Background(), "nope"); err != ErrSessionNotFound {
		t.Errorf("Stat of a missing session = %v, want ErrSessionNotFound", err)
	}
}

// ListInfo is newest-first so the session an operator just broke is at the top.
func TestListInfoSortedNewestFirst(t *testing.T) {
	m := newTestManager(t)
	for _, id := range []string{"oldest", "middle", "newest"} {
		seedSession(t, m, id, "x")
	}
	now := time.Now()
	mustChtime(t, m.sessionFilePath("oldest"), now.Add(-72*time.Hour))
	mustChtime(t, m.sessionFilePath("middle"), now.Add(-24*time.Hour))
	mustChtime(t, m.sessionFilePath("newest"), now.Add(-time.Hour))

	infos, err := m.ListInfo(context.Background())
	if err != nil {
		t.Fatalf("ListInfo: %v", err)
	}
	got := []string{infos[0].ID, infos[1].ID, infos[2].ID}
	want := []string{"newest", "middle", "oldest"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestPruneOlderThanDeletesOnlyTheOldOnes(t *testing.T) {
	m := newTestManager(t)
	for _, id := range []string{"ancient", "stale", "fresh"} {
		seedSession(t, m, id, "x")
	}
	now := time.Now()
	mustChtime(t, m.sessionFilePath("ancient"), now.Add(-90*24*time.Hour))
	mustChtime(t, m.sessionFilePath("stale"), now.Add(-45*24*time.Hour))
	mustChtime(t, m.sessionFilePath("fresh"), now.Add(-time.Hour))

	removed, err := m.PruneOlderThan(context.Background(), now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if len(removed) != 2 || removed[0] != "ancient" || removed[1] != "stale" {
		t.Fatalf("removed = %v, want [ancient stale]", removed)
	}
	if _, err := m.Stat(context.Background(), "fresh"); err != nil {
		t.Errorf("the recent session was deleted: %v", err)
	}
}

// A quarantine file is evidence. Deleting the session must not destroy it.
func TestDeleteRemovesEverySidecar(t *testing.T) {
	m := newTestManager(t)
	id := "with-sidecars"

	sess := NewSession(id)
	sess.AddMessage(Message{Role: RoleUser, Content: "x", Timestamp: time.Now().UTC()})
	sess.SetTopic("a topic")
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(m.quarantineFilePath(id), []byte("damaged\n"), sessionFileMode); err != nil {
		t.Fatalf("seed quarantine: %v", err)
	}
	if err := m.Archive(context.Background(), id, sess.Messages); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if err := m.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(m.metadataFilePath(id)); !os.IsNotExist(err) {
		t.Error("metadata sidecar survived Delete")
	}
	// Every sidecar holds a copy of the same conversation, so an explicit
	// delete must take them all. Quarantine survives an unreadable *load*, not
	// a delete; and an orphaned compaction archive would be inherited by the
	// next conversation under this channel:senderID.
	if _, err := os.Stat(m.quarantineFilePath(id)); !os.IsNotExist(err) {
		t.Errorf("quarantine copy survived Delete (err=%v)", err)
	}
	if _, err := os.Stat(m.archiveFilePath(id)); !os.IsNotExist(err) {
		t.Errorf("compaction archive survived Delete (err=%v)", err)
	}
}

func TestDeleteMissingSession(t *testing.T) {
	m := newTestManager(t)
	seedSession(t, m, "keep-me", "x")

	if err := m.Delete(context.Background(), "does-not-exist"); err != ErrSessionNotFound {
		t.Errorf("Delete of a missing session = %v, want ErrSessionNotFound", err)
	}
	if _, err := m.Stat(context.Background(), "keep-me"); err != nil {
		t.Errorf("an unrelated session was affected: %v", err)
	}
}

// Reset archives rather than deletes: "start fresh" must not mean "destroy the
// conversation".
func TestResetArchivesAndEmptiesSession(t *testing.T) {
	m := newTestManager(t)
	id := "resettable"
	seedSession(t, m, id, "remember this")

	sess := NewSession(id)
	sess.SetTopic("old topic")
	sess.AddMessage(Message{Role: RoleUser, Content: "remember this", Timestamp: time.Now().UTC()})
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	archived, err := m.Reset(context.Background(), id)
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(m.SessionsDir(), archived))
	if err != nil {
		t.Fatalf("archived file missing: %v", err)
	}
	if !strings.Contains(string(data), "remember this") {
		t.Error("archived file does not contain the original conversation")
	}

	// The ID is now empty rather than missing-with-history.
	fresh, err := m.GetOrCreate(context.Background(), id)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if len(fresh.Messages) != 0 {
		t.Errorf("expected an empty session after reset, got %d messages", len(fresh.Messages))
	}
	if fresh.ConversationTopic != "" {
		t.Errorf("conversation metadata survived the reset: %q", fresh.ConversationTopic)
	}

	// The archive must not be reported as a session.
	infos, err := m.ListInfo(context.Background())
	if err != nil {
		t.Fatalf("ListInfo: %v", err)
	}
	for _, info := range infos {
		if info.ID != id {
			t.Errorf("reset archive was listed as a session: %q", info.ID)
		}
	}
}

func TestResetMissingSession(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Reset(context.Background(), "nothing-here"); err != ErrSessionNotFound {
		t.Errorf("Reset of a missing session = %v, want ErrSessionNotFound", err)
	}
}

func mustChtime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
}

// Reset puts the whole conversation away, sidecars included.
//
// The compaction archive belongs to the conversation being archived. Left in
// place, the next conversation under the same channel:senderID inherits it and
// `sessions list` reports an archive of history that is not its own.
func TestResetMovesTheCompactionArchiveAside(t *testing.T) {
	m := newTestManager(t)
	id := "resettable"

	sess := NewSession(id)
	sess.AddMessage(Message{Role: RoleUser, Content: "earlier", Timestamp: time.Now().UTC()})
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := m.Archive(context.Background(), id, sess.Messages); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	archived, err := m.Reset(context.Background(), id)
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if _, err := os.Stat(m.archiveFilePath(id)); !os.IsNotExist(err) {
		t.Errorf("the new empty session inherited the old compaction archive (err=%v)", err)
	}
	moved := filepath.Join(m.sessionsDir, archived+".history")
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("the compaction archive was destroyed rather than moved aside: %v", err)
	}
}
