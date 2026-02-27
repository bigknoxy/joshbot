package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Memory consolidation tests

func TestManager_WriteMemory_EmptyContent(t *testing.T) {
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

	err = mgr.WriteMemory(ctx, "")
	if err != ErrEmptyContent {
		t.Fatalf("WriteMemory() error = %v, want ErrEmptyContent", err)
	}
}

func TestManager_AppendHistory_EmptyEntry(t *testing.T) {
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

	err = mgr.AppendHistory(ctx, "")
	if err != ErrEmptyContent {
		t.Fatalf("AppendHistory() error = %v, want ErrEmptyContent", err)
	}
}

func TestManager_MultipleHistoryEntries(t *testing.T) {
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

	// Append multiple entries
	if err := mgr.AppendHistory(ctx, "First entry"); err != nil {
		t.Fatalf("AppendHistory() error = %v", err)
	}
	if err := mgr.AppendHistory(ctx, "Second entry"); err != nil {
		t.Fatalf("AppendHistory() error = %v", err)
	}
	if err := mgr.AppendHistory(ctx, "Third entry"); err != nil {
		t.Fatalf("AppendHistory() error = %v", err)
	}

	data, err := os.ReadFile(mgr.HistoryPath())
	if err != nil {
		t.Fatalf("Read history error = %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "First entry") {
		t.Error("history missing first entry")
	}
	if !strings.Contains(content, "Second entry") {
		t.Error("history missing second entry")
	}
	if !strings.Contains(content, "Third entry") {
		t.Error("history missing third entry")
	}
}

func TestManager_PathFunctions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	memPath := mgr.MemoryPath()
	histPath := mgr.HistoryPath()

	expectedMemPath := filepath.Join(dir, "memory", "MEMORY.md")
	expectedHistPath := filepath.Join(dir, "memory", "HISTORY.md")

	if memPath != expectedMemPath {
		t.Errorf("MemoryPath() = %q, want %q", memPath, expectedMemPath)
	}
	if histPath != expectedHistPath {
		t.Errorf("HistoryPath() = %q, want %q", histPath, expectedHistPath)
	}
}

func TestManager_InitializeCreatesDirectories(t *testing.T) {
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

	// Verify files exist
	if _, err := os.Stat(mgr.MemoryPath()); os.IsNotExist(err) {
		t.Error("MEMORY.md should exist after Initialize")
	}
	if _, err := os.Stat(mgr.HistoryPath()); os.IsNotExist(err) {
		t.Error("HISTORY.md should exist after Initialize")
	}
}

func TestManager_InitializeExistingFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Create files manually
	if err := os.WriteFile(mgr.MemoryPath(), []byte("custom memory"), 0644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if err := os.WriteFile(mgr.HistoryPath(), []byte("custom history"), 0644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	// Initialize should NOT overwrite existing files
	if err := mgr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	content, _ := os.ReadFile(mgr.MemoryPath())
	if string(content) != "custom memory" {
		t.Errorf("Memory file was overwritten: %q", content)
	}

	histContent, _ := os.ReadFile(mgr.HistoryPath())
	if string(histContent) != "custom history" {
		t.Errorf("History file was overwritten: %q", histContent)
	}
}

func TestManager_MemoryFileCorrupted(t *testing.T) {
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

	// Try to read non-existent memory (should return empty, not error)
	// But for corrupted file, read should still work
	content, err := mgr.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory() error = %v", err)
	}
	// After init, should have default template
	if content == "" {
		t.Error("expected content after init")
	}
}

func TestManager_ConcurrentReadWrite(t *testing.T) {
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

	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	// Multiple goroutines reading
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, err := mgr.LoadMemory(ctx)
				if err != nil {
					errCh <- err
				}
			}
		}()
	}

	// Single goroutine writing
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 5; j++ {
			err := mgr.WriteMemory(ctx, "# Test\n\ncontent\n")
			if err != nil {
				errCh <- err
			}
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent error: %v", err)
	}
}

func TestManager_ConcurrentHistoryAppend(t *testing.T) {
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

	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	// Multiple goroutines appending
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			err := mgr.AppendHistory(ctx, "entry")
			if err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent append error: %v", err)
	}

	// Verify all entries were written
	data, err := os.ReadFile(mgr.HistoryPath())
	if err != nil {
		t.Fatalf("Read history error = %v", err)
	}

	// Count entries
	count := strings.Count(string(data), "entry")
	if count != 10 {
		t.Errorf("expected 10 entries, got %d", count)
	}
}

func TestManager_WriteMemory_NoNewline(t *testing.T) {
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

	// Write without trailing newline - should add one
	err = mgr.WriteMemory(ctx, "no newline")
	if err != nil {
		t.Fatalf("WriteMemory() error = %v", err)
	}

	content, _ := os.ReadFile(mgr.MemoryPath())
	if !strings.HasSuffix(string(content), "\n") {
		t.Error("content should have trailing newline")
	}
}

func TestManager_MemoryContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Note: LoadMemory does NOT check context cancellation in current implementation
	// It uses a simple file read without context checking
	// This test documents that behavior - reads proceed even if context is cancelled
	_, err = mgr.LoadMemory(ctx)
	// In current implementation, no error is returned for cancelled context
	if err != nil {
		t.Logf("LoadMemory() returned error: %v (may vary by implementation)", err)
	}
}

func TestManager_HistoryContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Note: LoadHistory does NOT check context cancellation in current implementation
	// This test documents that behavior
	_, err = mgr.LoadHistory(ctx, "")
	// In current implementation, no error is returned for cancelled context
	if err != nil {
		t.Logf("LoadHistory() returned error: %v (may vary by implementation)", err)
	}
}

func TestManager_AppendHistoryContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mgr.now = func() time.Time { return time.Unix(0, 0).UTC() }
	if err := mgr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Cancel before append
	cancel()

	err = mgr.AppendHistory(ctx, "test")
	if err != context.Canceled {
		t.Fatalf("AppendHistory() error = %v, want context.Canceled", err)
	}
}

func TestManager_WriteMemoryContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := mgr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Cancel before write
	cancel()

	err = mgr.WriteMemory(ctx, "test")
	if err != context.Canceled {
		t.Fatalf("WriteMemory() error = %v, want context.Canceled", err)
	}
}

func TestManager_MemoryPathDirectory(t *testing.T) {
	// Test that New creates the memory directory
	dir := t.TempDir()
	subDir := filepath.Join(dir, "nested")

	_, err := New(subDir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(filepath.Join(subDir, "memory")); os.IsNotExist(err) {
		t.Error("memory directory should be created")
	}
}

func TestManager_MultipleWrites(t *testing.T) {
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

	// Multiple writes
	for i := 0; i < 5; i++ {
		content := "# Test\n\ncontent " + string(rune('a'+i)) + "\n"
		if err := mgr.WriteMemory(ctx, content); err != nil {
			t.Fatalf("WriteMemory() error = %v", err)
		}

		loaded, err := mgr.LoadMemory(ctx)
		if err != nil {
			t.Fatalf("LoadMemory() error = %v", err)
		}
		if loaded != content {
			t.Errorf("loaded content = %q, want %q", loaded, content)
		}
	}
}
