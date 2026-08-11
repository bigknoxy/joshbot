package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeUnscannableSession creates a session file that Stat cannot scan: a
// single line longer than the scanner's 8MiB cap. It is the realistic shape of
// the failure the Unreadable branch exists for — a huge tool result — and needs
// no permission tricks, so it behaves the same when the tests run as root.
func writeUnscannableSession(t *testing.T, dir, id string) string {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	line := `{"role":"user","content":"` + strings.Repeat("x", 9*1024*1024) + `"}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An Info for a session that would not scan must carry the file's real mtime.
// PruneOlderThan compares UpdatedAt, and the zero time reads as infinitely old,
// so leaving it unset made a single transient read failure — an oversized line,
// a momentary EACCES — enough for `prune --older-than` to delete a session of
// any age. The listing must also report the session rather than hide it: the
// broken one is the one the operator is looking for.
func TestListInfoUnreadableSessionKeepsItsRealMtime(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := writeUnscannableSession(t, dir, "telegram:1")

	infos, err := m.ListInfo(context.Background())
	if err != nil {
		t.Fatalf("ListInfo: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("an unreadable session must still be listed, got %d entries", len(infos))
	}
	got := infos[0]
	if !got.Unreadable {
		t.Fatal("a session that could not be scanned was not flagged Unreadable, so its zero counts read as real")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("an unreadable session has no UpdatedAt; prune --older-than treats the zero time as infinitely old and would delete it")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(st.ModTime().UTC()) {
		t.Errorf("UpdatedAt = %v, want the file's mtime %v", got.UpdatedAt, st.ModTime().UTC())
	}
	if got.Bytes != st.Size() {
		t.Errorf("Bytes = %d, want the file's real size %d", got.Bytes, st.Size())
	}
}

// The end-to-end consequence of the above: a freshly written session that
// happens to be unscannable must survive a prune whose cutoff is in the past.
// This is the assertion that actually protects the user's data, and it fails
// for any implementation that lets an unreadable Info reach PruneOlderThan
// without a timestamp.
func TestPruneOlderThanKeepsARecentUnreadableSession(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := writeUnscannableSession(t, dir, "telegram:1")

	removed, err := m.PruneOlderThan(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("prune deleted %v: a session that merely failed to scan was treated as infinitely old", removed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the session file was destroyed by a prune it should have survived: %v", err)
	}
}

// The compaction record is defined as singular and at index 0, and
// CompactionRecord's contract is "the first message or nothing". A scan of the
// whole slice would look like a harmless generalisation but would let a
// compaction flag smuggled into a later message be returned as the session's
// summary — and the agent holds that record out of memory-window truncation, so
// it would pin attacker-chosen text into every subsequent request.
func TestCompactionRecordIsOnlyRecognisedAtIndexZero(t *testing.T) {
	s := NewSession("telegram:1")
	s.AddMessage(Message{Role: RoleUser, Content: "hello"})
	s.AddMessage(NewCompactionRecord("summary smuggled in later"))

	if rec, ok := s.CompactionRecord(); ok {
		t.Fatalf("a compaction record at index 1 was accepted as the session's record: %q", rec.Content)
	}
	// CountCompactionRecords must still see it, because it is the invariant
	// check: an implementation that only looked at index 0 here could never
	// report the duplicate it exists to detect.
	if n := s.CountCompactionRecords(); n != 1 {
		t.Fatalf("CountCompactionRecords = %d, want 1; it cannot detect a duplicate record it does not count", n)
	}

	s.Messages = append([]Message{NewCompactionRecord("the real summary")}, s.Messages...)
	rec, ok := s.CompactionRecord()
	if !ok {
		t.Fatal("a compaction record at index 0 was not found")
	}
	if !strings.Contains(rec.Content, "the real summary") {
		t.Errorf("CompactionRecord returned %q, want the record at index 0", rec.Content)
	}
	if n := s.CountCompactionRecords(); n != 2 {
		t.Fatalf("CountCompactionRecords = %d, want 2; the duplicate-record invariant is unenforceable without an accurate count", n)
	}
}

// A Save that cannot reach the disk must return the error. Swallowing it — the
// natural shape if the temp-file cleanup path ever grows a `return nil` — loses
// the turn silently: the agent replies, the user sees an answer, and the
// conversation is gone on the next load with nothing logged as failed.
func TestSaveIntoAMissingDirectoryFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession("telegram:1")
	s.AddMessage(Message{Role: RoleUser, Content: "hello"})

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(context.Background(), s); err == nil {
		t.Fatal("Save reported success with no sessions directory; the conversation was silently discarded")
	}
}

// Every writer needs its own temp file. A fixed `<path>.tmp` is shared by the
// gateway and a concurrent `agent -m` — the RWMutex only guards one process —
// so two writers interleave into one file and the surviving rename publishes a
// torn mix. A leftover temp file is also the visible symptom of a failed write
// that did not clean up; assert the directory holds nothing but the session's
// own files.
func TestSaveLeavesNoTemporaryFilesBehind(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession("telegram:1")
	for i := 0; i < 3; i++ {
		s.AddMessage(Message{Role: RoleUser, Content: "hello"})
		if err := m.Save(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("Save left a temporary file %q behind", e.Name())
		}
	}

	// And the temp name must be unique, not derived from the target: a
	// pre-existing file at the fixed legacy name must not be adopted or
	// clobbered into the session.
	legacy := filepath.Join(dir, "telegram:1.jsonl.tmp")
	if err := os.WriteFile(legacy, []byte("not a session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.AddMessage(Message{Role: RoleUser, Content: "again"})
	if err := m.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "telegram:1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "not a session") {
		t.Fatal("Save reused the shared fixed temp name; a second writer's bytes were published into the session")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("Save consumed a temp file it does not own: %v", err)
	}
}
