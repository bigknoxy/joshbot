package main

import (
	"context"
	"testing"

	"github.com/bigknoxy/joshbot/internal/api"
	"github.com/bigknoxy/joshbot/internal/session"
)

// TestWebUIURLIsClickable pins the wildcard rewrite. "0.0.0.0:8080" is not an
// address a browser resolves, so printing the bind address verbatim hands the
// operator a URL that does not work on the one bind where they most need it.
func TestWebUIURLIsClickable(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8080": "http://127.0.0.1:8080/",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/",
		"[::]:8080":      "http://127.0.0.1:8080/",
		"localhost:9000": "http://localhost:9000/",
		"[::1]:8080":     "http://[::1]:8080/",
		"nonsense":       "http://nonsense/",
	}
	for in, want := range cases {
		if got := webUIURL(in); got != want {
			t.Errorf("webUIURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTranscriptReaderShowsOnlyHumanReadableTurns. The web UI renders what a
// person said and was told. A tool message, a system message, a compaction
// envelope and an empty tool-call carrier are the agent's internals: showing
// them would put "<ctx_compress>" and raw tool JSON in the operator's chat log
// and claim the assistant said it.
func TestTranscriptReaderShowsOnlyHumanReadableTurns(t *testing.T) {
	dir := t.TempDir()
	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	key := api.ChannelName + ":web-abcd1234"
	sess, err2 := mgr.GetOrCreate(context.Background(), key)
	if err2 != nil {
		t.Fatalf("GetOrCreate: %v", err2)
	}
	sess.AddMessage(session.NewCompactionRecord("earlier conversation"))
	sess.AddMessage(session.Message{Role: session.RoleUser, Content: "hello"})
	sess.AddMessage(session.Message{Role: session.RoleAssistant, Content: ""})
	sess.AddMessage(session.Message{Role: session.RoleTool, Content: `{"stdout":"secrets"}`})
	sess.AddMessage(session.Message{Role: session.RoleSystem, Content: "system prompt"})
	sess.AddMessage(session.Message{Role: session.RoleAssistant, Content: "hi there"})
	if err := mgr.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	read := transcriptReader(mgr)
	if read == nil {
		t.Fatal("transcriptReader returned nil for a real manager")
	}
	msgs, err := read("web-abcd1234")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("transcript = %+v, want exactly the user turn and the assistant turn", msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("first message = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
		t.Errorf("second message = %+v", msgs[1])
	}
}

// TestTranscriptReaderNilManager: a nil manager means no history, and the API
// server reads a nil reader as an empty transcript rather than an error.
func TestTranscriptReaderNilManager(t *testing.T) {
	if transcriptReader(nil) != nil {
		t.Error("transcriptReader(nil) returned a reader")
	}
}
