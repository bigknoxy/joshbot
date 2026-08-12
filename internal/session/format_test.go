package session_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	pkg "github.com/bigknoxy/joshbot/internal/session"
)

// humanBytes tests — via FormatInfoTable output.

func TestFormatInfoTableEmptyList(t *testing.T) {
	var buf bytes.Buffer
	pkg.FormatInfoTable(&buf, nil, time.Now())
	if !strings.Contains(buf.String(), "No sessions yet") {
		t.Errorf("expected empty message: %s", buf.String())
	}
}

func TestFormatInfoTableWithData(t *testing.T) {
	now := time.Now()
	infos := []pkg.Info{
		{ID: "cli:user", Messages: 42, Bytes: 1024*1024 + 500},
		{ID: "bot:test", Messages: 7, Bytes: 3900},
	}
	var buf bytes.Buffer
	pkg.FormatInfoTable(&buf, infos, now)

	out := buf.String()
	for _, expected := range []string{"cli:user", "bot:test", "MB", "KB", "session(s)"} {
		if !strings.Contains(out, expected) {
			t.Errorf("expected %q in output: %s", expected, out)
		}
	}
}

// FormatMessages tests.

func TestFormatMessagesNoCompaction(t *testing.T) {
	msgs := []pkg.Message{
		{Role: "user", Content: "hello", Timestamp: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)},
	}
	var buf bytes.Buffer
	pkg.FormatMessages(&buf, msgs, 0)

	out := buf.String()
	for _, expected := range []string{"user", "hello", "2026-01-15"} {
		if !strings.Contains(out, expected) {
			t.Errorf("expected %q in output: %s", expected, out)
		}
	}
}

func TestFormatMessagesCompactionMarker(t *testing.T) {
	msgs := []pkg.Message{
		{Role: "system", Compaction: true, Content: "earlier summary", Timestamp: time.Now()},
	}
	var buf bytes.Buffer
	pkg.FormatMessages(&buf, msgs, 0)

	if !strings.Contains(buf.String(), "[compaction record]") {
		t.Errorf("compaction marker missing from output: %s", buf.String())
	}
}

func TestFormatMessagesToolCalls(t *testing.T) {
	msgs := []pkg.Message{
		{Role: "assistant", Content: "result", Timestamp: time.Now(),
			ToolCalls: []pkg.ToolCall{{Name: "shell_exec", Arguments: json.RawMessage(`{"command":"ls"}`)}},
		},
	}
	var buf bytes.Buffer
	pkg.FormatMessages(&buf, msgs, 0)

	out := buf.String()
	if !strings.Contains(out, "tool shell_exec") {
		t.Errorf("tool call not rendered: %s", out)
	}
}

func TestFormatMessagesLastN(t *testing.T) {
	msgs := make([]pkg.Message, 5)
	for i := range msgs {
		msgs[i].Role = "user"
		msgs[i].Content = fmt.Sprintf("msg-%d", i)
	}
	var buf bytes.Buffer
	pkg.FormatMessages(&buf, msgs, 2)

	out := buf.String()
	if strings.Contains(out, "msg-0") {
		t.Error("first message should not appear when last=2 is used")
	}
	if !strings.Contains(out, "msg-4") {
		t.Error("last message must appear")
	}
}

func TestFormatMessagesEmptySession(t *testing.T) {
	var buf bytes.Buffer
	pkg.FormatMessages(&buf, nil, 0)
	if !strings.Contains(buf.String(), "no messages") {
		t.Errorf("expected empty session notice: %s", buf.String())
	}
}
