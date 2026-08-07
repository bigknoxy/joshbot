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
		matches := checkboxRE.FindAllStringSubmatch(tt.line, -1)
		got := len(matches) > 0
		if got != tt.matches {
			t.Errorf("regex on %q: got %v, want %v", tt.line, got, tt.matches)
		}
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
