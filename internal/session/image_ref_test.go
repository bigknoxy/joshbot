package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImageRefSurvivesRoundTripWithoutTheBytes pins a deliberate decision: a
// session records that an image was sent — its type, size and digest — and does
// not store the image.
//
// The reasons are the same ones that make session files what they are. Session
// JSONL is exempt from redaction and protected only by its 0600 mode, so a
// megabyte of base64 image per turn would be a large amount of unredactable
// content at rest. It would also be reloaded into every subsequent request in
// the memory window, re-billing the image on turns that never mentioned it.
// The descriptor is enough to say "you sent me a 41KB PNG" on a later turn.
//
// If someone later decides the bytes should persist, this test is the place
// that argument has to be won — it fails, loudly, rather than the change
// slipping in as an apparent improvement.
func TestImageRefSurvivesRoundTripWithoutTheBytes(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()

	const id = "cli:imgtest"
	sess, err := mgr.GetOrCreate(ctx, id)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	sess.Messages = append(sess.Messages, Message{
		Role:    "user",
		Content: "what is in this picture?",
		Images: []ImageRef{{
			Label:  "shot.png",
			MIME:   "image/png",
			Bytes:  41234,
			SHA256: "cafebabe",
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
		if len(loaded.Messages[i].Images) > 0 {
			found = &loaded.Messages[i]
			break
		}
	}
	if found == nil {
		t.Fatal("the image attachment did not survive the round trip")
	}
	got := found.Images[0]
	want := ImageRef{Label: "shot.png", MIME: "image/png", Bytes: 41234, SHA256: "cafebabe"}
	if got != want {
		t.Fatalf("ImageRef = %+v, want %+v", got, want)
	}
	if found.Content != "what is in this picture?" {
		t.Fatalf("the caption must survive alongside the attachment, got %q", found.Content)
	}

	// A text-only message must still serialise with no images key at all —
	// json:"images,omitempty" is what keeps every existing session file
	// byte-identical to what it was before attachments existed.
	raw, err := os.ReadFile(filepath.Join(dir, "cli_imgtest.jsonl"))
	if err != nil {
		// The on-disk name is an implementation detail; find it instead of
		// hard-failing on the mangling scheme.
		entries, derr := os.ReadDir(dir)
		if derr != nil {
			t.Fatalf("ReadDir: %v", derr)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".jsonl") {
				raw, err = os.ReadFile(filepath.Join(dir, e.Name()))
				break
			}
		}
		if err != nil {
			t.Fatalf("could not read the session file: %v", err)
		}
	}
	if strings.Contains(string(raw), "iVBOR") || strings.Contains(string(raw), "base64") {
		t.Fatal("image bytes were written into the session file")
	}
	if !strings.Contains(string(raw), `"sha256":"cafebabe"`) {
		t.Fatalf("the descriptor was not persisted; file was:\n%s", raw)
	}
}
