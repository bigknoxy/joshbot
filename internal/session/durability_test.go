package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestManager returns a Manager rooted in a fresh temp dir.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func saveOneMessage(t *testing.T, m *Manager, id, content string) {
	t.Helper()
	sess := NewSession(id)
	sess.AddMessage(Message{Role: RoleUser, Content: content, Timestamp: time.Now().UTC()})
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// Session files hold full conversation content; they must not be readable by
// any other local account.
func TestSaveUsesOwnerOnlyPermissions(t *testing.T) {
	m := newTestManager(t)

	sess := NewSession("perm-session")
	sess.AddMessage(Message{Role: RoleUser, Content: "hello", Timestamp: time.Now().UTC()})
	sess.SetTopic("a topic") // forces the metadata file to be written too
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, path := range []string{
		m.sessionFilePath("perm-session"),
		m.metadataFilePath("perm-session"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != sessionFileMode {
			t.Errorf("%s has mode %04o, want %04o", path, got, sessionFileMode)
		}
	}
}

// A damaged line must not end the conversation: everything that parses is
// preserved and the original file is quarantined rather than deleted.
func TestLoadSkipsCorruptLinesAndQuarantines(t *testing.T) {
	m := newTestManager(t)
	id := "torn-session"

	good1 := `{"role":"user","content":"first","timestamp":"2026-01-01T00:00:00Z"}`
	torn := `{"role":"assistant","content":"half-writ`
	good2 := `{"role":"user","content":"third","timestamp":"2026-01-01T00:00:02Z"}`
	raw := strings.Join([]string{good1, torn, good2}, "\n") + "\n"

	if err := os.WriteFile(m.sessionFilePath(id), []byte(raw), sessionFileMode); err != nil {
		t.Fatalf("seed session file: %v", err)
	}

	sess, err := m.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load returned an error on a torn file; the session must still load: %v", err)
	}

	if len(sess.Messages) != 2 {
		t.Fatalf("expected the 2 parseable messages, got %d", len(sess.Messages))
	}
	if sess.Messages[0].Content != "first" || sess.Messages[1].Content != "third" {
		t.Errorf("wrong messages survived: %+v", sess.Messages)
	}

	// The original bytes are preserved for inspection, not discarded.
	quarantined, err := os.ReadFile(m.quarantineFilePath(id))
	if err != nil {
		t.Fatalf("expected the original to be quarantined: %v", err)
	}
	if string(quarantined) != raw {
		t.Error("quarantined file does not match the original bytes")
	}
	info, err := os.Stat(m.quarantineFilePath(id))
	if err != nil {
		t.Fatalf("stat quarantine: %v", err)
	}
	if got := info.Mode().Perm(); got != sessionFileMode {
		t.Errorf("quarantine file has mode %04o, want %04o", got, sessionFileMode)
	}
}

// A fully unparseable file still loads as an empty session rather than
// permanently erroring for that channel:senderID.
func TestLoadFullyCorruptFileYieldsEmptySession(t *testing.T) {
	m := newTestManager(t)
	id := "junk-session"

	if err := os.WriteFile(m.sessionFilePath(id), []byte("not json at all\nnor this\n"), sessionFileMode); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sess, err := m.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sess.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(sess.Messages))
	}
	if _, err := os.Stat(m.quarantineFilePath(id)); err != nil {
		t.Errorf("expected quarantine file: %v", err)
	}
}

// A clean file is never quarantined.
func TestLoadCleanFileDoesNotQuarantine(t *testing.T) {
	m := newTestManager(t)
	saveOneMessage(t, m, "clean-session", "hi")

	if _, err := m.Load(context.Background(), "clean-session"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(m.quarantineFilePath("clean-session")); !os.IsNotExist(err) {
		t.Error("a clean session must not produce a quarantine file")
	}
}

// Two managers over the same directory model two processes (gateway and
// `agent -m`). A shared fixed temp-file name lets their writes interleave into
// one file; unique temp names keep every published file individually complete.
func TestConcurrentWritersNeverPublishATornFile(t *testing.T) {
	dir := t.TempDir()
	const id = "shared-session"

	// Each writer is a separate Manager, so they share no mutex — exactly the
	// gateway/`agent -m` situation.
	var writers sync.WaitGroup
	for _, marker := range []string{"alpha", "beta"} {
		m, err := NewManager(dir)
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		sess := NewSession(id)
		for i := 0; i < 40; i++ {
			sess.AddMessage(Message{
				Role:      RoleUser,
				Content:   marker + strings.Repeat("x", 4096),
				Timestamp: time.Now().UTC(),
			})
		}

		writers.Add(1)
		go func(m *Manager, sess *Session, marker string) {
			defer writers.Done()
			for i := 0; i < 25; i++ {
				if err := m.Save(context.Background(), sess); err != nil {
					t.Errorf("Save(%s): %v", marker, err)
					return
				}
			}
		}(m, sess, marker)
	}

	// Read the published file throughout; it must always parse cleanly and
	// must never contain content from both writers at once.
	reader, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	stop := make(chan struct{})
	var readerDone sync.WaitGroup
	readerDone.Add(1)
	go func() {
		defer readerDone.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			sess, err := reader.Load(context.Background(), id)
			if err == ErrSessionNotFound {
				continue
			}
			if err != nil {
				t.Errorf("Load saw a torn file: %v", err)
				return
			}
			var sawAlpha, sawBeta bool
			for _, msg := range sess.Messages {
				if strings.HasPrefix(msg.Content, "alpha") {
					sawAlpha = true
				}
				if strings.HasPrefix(msg.Content, "beta") {
					sawBeta = true
				}
			}
			if sawAlpha && sawBeta {
				t.Error("published session file mixes two writers' content")
				return
			}
		}
	}()

	writers.Wait()
	close(stop)
	readerDone.Wait()

	// A torn write would have been quarantined; nothing should have been.
	if _, err := os.Stat(m0Quarantine(dir, id)); !os.IsNotExist(err) {
		t.Error("concurrent writers produced an unparseable session file")
	}

	// No temp files may be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func m0Quarantine(dir, id string) string {
	return filepath.Join(dir, id+".jsonl.corrupt")
}
