package heartbeat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

func TestNewService(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	svc := NewService(b, tmpDir)
	if svc == nil {
		t.Fatal("NewService() returned nil")
	}
	if svc.path != filepath.Join(tmpDir, "HEARTBEAT.md") {
		t.Errorf("NewService() path = %v, want %v", svc.path, filepath.Join(tmpDir, "HEARTBEAT.md"))
	}
}

func TestSetInterval(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	svc := NewService(b, tmpDir)
	svc.SetInterval(5 * time.Second)
	if svc.interval != 5*time.Second {
		t.Errorf("SetInterval() = %v, want %v", svc.interval, 5*time.Second)
	}
}

func TestSetIntervalInvalid(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	svc := NewService(b, tmpDir)
	svc.SetInterval(0)
	if svc.interval != 30*time.Minute {
		t.Errorf("SetInterval(0) should not change interval, got %v", svc.interval)
	}
}

func TestScanAndPublishNoFile(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	svc := NewService(b, tmpDir)
	// Should not panic when file doesn't exist
	svc.scanAndPublish()
}

func TestScanAndPublishWithTasks(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	svc := NewService(b, tmpDir)

	// Create HEARTBEAT.md with tasks
	content := `# Heartbeat Tasks
- [ ] Check system health
- [ ] Review memory usage
- [x] Completed task
* [ ] Another pending task`
	err := os.WriteFile(filepath.Join(tmpDir, "HEARTBEAT.md"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	svc.scanAndPublish()
	// Should not panic - tasks are published to bus
}

func TestStartStop(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	svc := NewService(b, tmpDir)
	svc.SetInterval(100 * time.Millisecond)

	svc.Start()
	time.Sleep(150 * time.Millisecond)
	svc.Stop()
	// Should not hang or panic
}

func TestCheckboxRegex(t *testing.T) {
	tests := []struct {
		line    string
		matches bool
	}{
		{"- [ ] Check system", true},
		{"* [ ] Another task", true},
		{"  - [ ] Indented task", true},
		{"- [x] Completed task", false},
		{"# Heading", false},
		{"Regular text", false},
	}

	for _, tt := range tests {
		_, _, got := parseTask(tt.line)
		if got != tt.matches {
			t.Errorf("regex on %q: got %v, want %v", tt.line, got, tt.matches)
		}
	}
}

// TestParseTaskPublishImpliesCheckoff pins the invariant that broke the
// heartbeat's one-shot guarantee: publishing and checking off used to be two
// disagreeing regexes, so "-[ ] task" and "* [ ]task" were published on every
// tick forever and never checked off. A single parse now decides both, so any
// line that yields a task must also yield a checked rewrite.
func TestParseTaskPublishImpliesCheckoff(t *testing.T) {
	tests := []struct {
		line    string
		ok      bool
		task    string
		checked string
	}{
		{"- [ ] task", true, "task", "- [x] task"},
		{"* [ ] task", true, "task", "* [x] task"},
		{"+ [ ] task", true, "task", "+ [x] task"},
		{"  - [ ] indented", true, "indented", "  - [x] indented"},
		{"\t* [ ] tabbed", true, "tabbed", "\t* [x] tabbed"},
		{"-[ ] task", true, "task", "-[x] task"},
		{"* [ ]task", true, "task", "* [x]task"},
		{"-  [ ]  spaced", true, "spaced", "-  [x]  spaced"},
		{"- [ ] trailing  ", true, "trailing", "- [x] trailing"},
		{"- [ ] crlf\r", true, "crlf", "- [x] crlf\r"},
		{"- [x] done", false, "", ""},
		{"* [X] done", false, "", ""},
		{"- [ ]", false, "", ""},
		{"- [ ]   ", false, "", ""},
		{"# Heading", false, "", ""},
		{"Regular text", false, "", ""},
		{"[ ] no bullet", false, "", ""},
	}

	for _, tt := range tests {
		task, checked, ok := parseTask(tt.line)
		if ok != tt.ok {
			t.Errorf("parseTask(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if task != tt.task {
			t.Errorf("parseTask(%q) task = %q, want %q", tt.line, task, tt.task)
		}
		if checked != tt.checked {
			t.Errorf("parseTask(%q) checked = %q, want %q", tt.line, checked, tt.checked)
		}
		// The rewritten line must no longer parse, or it re-fires next tick.
		if _, _, again := parseTask(checked); again {
			t.Errorf("parseTask(%q) rewrite %q still parses as unchecked", tt.line, checked)
		}
	}
}

// TestScanAndPublishChecksOffNonCanonicalSyntax is the end-to-end form of the
// same defect: a non-canonical line published every tick forever.
func TestScanAndPublishChecksOffNonCanonicalSyntax(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "HEARTBEAT.md")
	content := "-[ ] tight\n* [ ]nospace\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc := NewService(b, tmpDir)

	svc.scanAndPublish()
	if got := len(drainInbound(b)); got != 2 {
		t.Fatalf("first tick published %d tasks, want 2", got)
	}

	svc.scanAndPublish()
	if got := drainInbound(b); len(got) != 0 {
		t.Errorf("second tick republished %d tasks, want 0: %+v", len(got), got)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(after), "[ ]") {
		t.Errorf("file still has unchecked boxes after publish: %q", after)
	}
}

// TestScanAndPublishLeavesTaskUncheckedWhenSendFails covers the silent task
// loss: a full bus queue dropped the message but the box was flipped anyway.
func TestScanAndPublishLeavesTaskUncheckedWhenSendFails(t *testing.T) {
	b := bus.NewMessageBus()
	// Fill the inbound queue so every Send returns false.
	for i := 0; i < bus.MaxQueueSize; i++ {
		if !b.Send(bus.InboundMessage{Content: "filler"}) {
			t.Fatalf("failed to fill queue at %d", i)
		}
	}
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "HEARTBEAT.md")
	content := "- [ ] undelivered\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc := NewService(b, tmpDir)
	svc.scanAndPublish()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != content {
		t.Errorf("file rewritten despite dropped Send: got %q, want %q", after, content)
	}
}

// TestScanAndPublishPreservesFileMode pins the atomic rewrite: a 0600
// HEARTBEAT.md must not come back as 0644.
func TestScanAndPublishPreservesFileMode(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte("- [ ] task\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc := NewService(b, tmpDir)
	svc.scanAndPublish()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	// The rewrite must leave no temp files behind.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("leftover files after atomic write: %v", entries)
	}
}

// drainInbound reads all currently-queued inbound messages without blocking.
func drainInbound(b *bus.MessageBus) []bus.InboundMessage {
	var out []bus.InboundMessage
	for {
		select {
		case m := <-b.InboundChannel():
			out = append(out, m)
		default:
			return out
		}
	}
}

// TestScanAndPublishDeliverPath covers the full publish path (#141): tasks are
// published with a resolved chat_id, the Contract is appended, and every task is
// checked off so it does not re-fire on the next tick.
func TestScanAndPublishDeliverPath(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "HEARTBEAT.md")
	content := "# Tasks\n- [ ] Check health\n* [ ] Review usage\n- [x] Already done\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc := NewService(b, tmpDir)
	svc.SetChannel("telegram")
	svc.SetChatIDResolver(func(channel string) (string, bool) {
		if channel != "telegram" {
			t.Errorf("resolver channel = %q, want telegram", channel)
		}
		return "12345", true
	})

	svc.scanAndPublish()

	msgs := drainInbound(b)
	if len(msgs) != 2 {
		t.Fatalf("published %d messages, want 2", len(msgs))
	}
	for _, m := range msgs {
		if m.SenderID != "heartbeat" {
			t.Errorf("SenderID = %q, want heartbeat", m.SenderID)
		}
		if m.Channel != "telegram" {
			t.Errorf("Channel = %q, want telegram", m.Channel)
		}
		if got := m.Metadata["chat_id"]; got != "12345" {
			t.Errorf("chat_id = %v, want 12345", got)
		}
		if m.Metadata["source"] != "heartbeat" {
			t.Errorf("source = %v, want heartbeat", m.Metadata["source"])
		}
		if !strings.Contains(m.Content, Contract) {
			t.Errorf("Content missing Contract: %q", m.Content)
		}
	}

	// File boxes flipped to [x]: a second scan publishes nothing.
	svc.scanAndPublish()
	if again := drainInbound(b); len(again) != 0 {
		t.Fatalf("re-fired %d tasks after check-off, want 0", len(again))
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "[ ]") {
		t.Errorf("unchecked boxes remain after publish:\n%s", data)
	}
}

// TestScanAndPublishNoChatIDSkips verifies that when no chat ID is known the
// tasks are neither published nor checked off, so they retry later (#141).
func TestScanAndPublishNoChatIDSkips(t *testing.T) {
	b := bus.NewMessageBus()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "HEARTBEAT.md")
	content := "- [ ] Check health\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc := NewService(b, tmpDir)
	svc.SetChatIDResolver(func(channel string) (string, bool) { return "", false })

	svc.scanAndPublish()

	if msgs := drainInbound(b); len(msgs) != 0 {
		t.Fatalf("published %d messages with no chat ID, want 0", len(msgs))
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "[ ]") {
		t.Errorf("task was checked off despite no recipient:\n%s", data)
	}
}
