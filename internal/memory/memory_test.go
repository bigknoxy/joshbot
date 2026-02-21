package memory

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestManagerReadWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := mgr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	content, err := mgr.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory() error = %v", err)
	}
	if content == "" {
		t.Fatalf("expected template content, got empty")
	}

	updated := "# Long-Term Memory\n\nkey: value\n"
	if err := mgr.WriteMemory(ctx, updated); err != nil {
		t.Fatalf("WriteMemory() error = %v", err)
	}

	got, err := mgr.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory() after write error = %v", err)
	}
	if got != updated {
		t.Fatalf("LoadMemory() = %q, want %q", got, updated)
	}
}

func TestAppendHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mgr.now = func() time.Time { return time.Unix(0, 0).UTC() }
	if err := mgr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if err := mgr.AppendHistory(ctx, "Did a thing"); err != nil {
		t.Fatalf("AppendHistory() error = %v", err)
	}

	data, err := os.ReadFile(mgr.HistoryPath())
	if err != nil {
		t.Fatalf("Read history error = %v", err)
	}
	if !strings.Contains(string(data), "[1970-01-01 00:00] Did a thing") {
		t.Fatalf("history missing entry: %s", data)
	}
}

func TestLoadHistoryMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	got, err := mgr.LoadHistory(ctx, "")
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty history, got %q", got)
	}
}
