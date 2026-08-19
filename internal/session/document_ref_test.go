package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocumentRefSurvivesRoundTripWithoutTheBytes is the twin of
// TestImageRefSurvivesRoundTripWithoutTheBytes, and the same argument applies
// unchanged: what is stored is a descriptor, never the content.
//
// Session JSONL is deliberately exempt from redaction and protected only by its
// 0600 mode, so persisting megabytes of base64 PDF would be a large amount of
// unredactable content at rest — and it would be reloaded into every subsequent
// request in the memory window, re-billing the document on turns that never
// mentioned it. The descriptor is enough to say "you sent me a 1.2MB PDF called
// report.pdf" on a later turn.
//
// If someone later decides the bytes should persist, this test is where that
// argument has to be won.
func TestDocumentRefSurvivesRoundTripWithoutTheBytes(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()

	const id = "cli:doctest"
	sess, err := mgr.GetOrCreate(ctx, id)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sess.Messages = append(sess.Messages, Message{
		Role:    "user",
		Content: "summarise this",
		Documents: []DocumentRef{{
			Label:  "report.pdf",
			MIME:   "application/pdf",
			Bytes:  1234567,
			SHA256: "deadbeef",
		}},
	})
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := mgr.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var found *Message
	for i := range loaded.Messages {
		if len(loaded.Messages[i].Documents) > 0 {
			found = &loaded.Messages[i]
			break
		}
	}
	if found == nil {
		t.Fatal("the document attachment did not survive the round trip")
	}
	want := DocumentRef{Label: "report.pdf", MIME: "application/pdf", Bytes: 1234567, SHA256: "deadbeef"}
	if found.Documents[0] != want {
		t.Fatalf("DocumentRef = %+v, want %+v", found.Documents[0], want)
	}
	if found.Content != "summarise this" {
		t.Fatalf("the caption must survive alongside the attachment, got %q", found.Content)
	}

	raw := readOnlySessionFile(t, dir)
	if strings.Contains(raw, "%PDF") || strings.Contains(raw, "JVBERi") || strings.Contains(raw, "base64") {
		t.Fatalf("document bytes were written into the session file:\n%s", raw)
	}
	if !strings.Contains(raw, `"sha256":"deadbeef"`) {
		t.Fatalf("the descriptor was not persisted; file was:\n%s", raw)
	}
}

// TestTextOnlyMessageWritesNoDocumentsKey pins the omitempty tag: every session
// file written before documents existed must stay byte-identical.
func TestTextOnlyMessageWritesNoDocumentsKey(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()
	sess, err := mgr.GetOrCreate(ctx, "cli:plain")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sess.Messages = append(sess.Messages, Message{Role: "user", Content: "hello"})
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if raw := readOnlySessionFile(t, dir); strings.Contains(raw, "documents") {
		t.Fatalf("a text-only message wrote a documents key:\n%s", raw)
	}
}

// readOnlySessionFile returns the contents of the single .jsonl file in dir.
// The on-disk name is an implementation detail, so it is found rather than
// hard-coded.
func readOnlySessionFile(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			raw, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
			if rerr != nil {
				t.Fatalf("read %s: %v", e.Name(), rerr)
			}
			return string(raw)
		}
	}
	t.Fatal("no session file was written")
	return ""
}
