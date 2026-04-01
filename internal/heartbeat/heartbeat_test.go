package heartbeat

import (
	"os"
	"path/filepath"
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
