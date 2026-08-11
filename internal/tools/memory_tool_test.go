package tools

import (
	"context"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/memory"
)

func TestMemorySearchTool_Name(t *testing.T) {
	tool := NewMemorySearchTool(nil)
	if got := tool.Name(); got != "memory_search" {
		t.Errorf("Name() = %q, want %q", got, "memory_search")
	}
}

func TestMemorySearchTool_Description(t *testing.T) {
	tool := NewMemorySearchTool(nil)
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() should not be empty")
	}
}

func TestMemorySearchTool_Parameters(t *testing.T) {
	tool := NewMemorySearchTool(nil)
	params := tool.Parameters()

	if len(params) != 2 {
		t.Fatalf("expected 2 parameters, got %d", len(params))
	}

	// Check query parameter
	if params[0].Name != "query" {
		t.Errorf("first parameter name = %q, want %q", params[0].Name, "query")
	}
	if params[0].Type != ParamString {
		t.Errorf("query parameter type = %v, want %v", params[0].Type, ParamString)
	}
	if !params[0].Required {
		t.Error("query parameter should be required")
	}

	// Check max_results parameter
	if params[1].Name != "max_results" {
		t.Errorf("second parameter name = %q, want %q", params[1].Name, "max_results")
	}
	if params[1].Type != ParamInteger {
		t.Errorf("max_results type = %v, want %v", params[1].Type, ParamInteger)
	}
	if params[1].Required {
		t.Error("max_results parameter should not be required")
	}
}

func TestMemorySearchTool_Execute_NilMemory(t *testing.T) {
	tool := NewMemorySearchTool(nil)
	result := tool.Execute(nil, map[string]any{
		"query": "test",
	})

	if result.Error == nil {
		t.Fatal("expected error for nil memory manager")
	}
	if result.Error.Error() != "memory manager not available" {
		t.Errorf("unexpected error: %v", result.Error)
	}
}

func TestMemorySearchTool_Execute_EmptyQuery(t *testing.T) {
	mem := newTestMemoryManager(t, []memory.Fact{
		{
			Category:   memory.FactUserInfo,
			Content:    "User likes dark mode",
			Confidence: 0.9,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
	})
	tool := NewMemorySearchTool(mem)

	// Empty query — scoreRelevance still returns base 0.5, so facts are returned
	result := tool.Execute(nil, map[string]any{
		"query": "",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output for empty query with facts")
	}
	if !containsResult(result.Output, "dark mode") {
		t.Errorf("expected output to contain 'dark mode', got: %s", result.Output)
	}
}

func TestMemorySearchTool_Execute_NoQuery(t *testing.T) {
	mem := newTestMemoryManager(t, []memory.Fact{
		{
			Category:   memory.FactUserInfo,
			Content:    "User likes dark mode",
			Confidence: 0.9,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
	})
	tool := NewMemorySearchTool(mem)

	// No "query" key in args — query defaults to "", base score still matches
	result := tool.Execute(nil, map[string]any{})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestMemorySearchTool_Execute_ReturnsResults(t *testing.T) {
	facts := []memory.Fact{
		{
			Category:   memory.FactUserInfo,
			Content:    "User prefers dark mode in all applications",
			Confidence: 0.9,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
		{
			Category:   memory.FactPreference,
			Content:    "User likes Python for scripting",
			Confidence: 0.8,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
		{
			Category:   memory.FactProject,
			Content:    "Working on joshbot AI assistant in Go",
			Confidence: 0.95,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
	}

	mem := newTestMemoryManager(t, facts)
	tool := NewMemorySearchTool(mem)

	result := tool.Execute(nil, map[string]any{
		"query": "dark mode",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	if result.Output == "" {
		t.Fatal("expected non-empty output")
	}
	// The dark mode fact should be in results
	if !containsResult(result.Output, "dark mode") {
		t.Errorf("expected output to contain 'dark mode', got: %s", result.Output)
	}
}

func TestMemorySearchTool_Execute_WithMaxResults(t *testing.T) {
	facts := []memory.Fact{
		{
			Category:   memory.FactUserInfo,
			Content:    "Fact one about Go programming",
			Confidence: 0.8,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
		{
			Category:   memory.FactUserInfo,
			Content:    "Fact two about Go programming",
			Confidence: 0.7,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
		{
			Category:   memory.FactUserInfo,
			Content:    "Fact three about Go programming",
			Confidence: 0.6,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
	}

	mem := newTestMemoryManager(t, facts)
	tool := NewMemorySearchTool(mem)

	result := tool.Execute(nil, map[string]any{
		"query":       "Go programming",
		"max_results": float64(2),
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestMemorySearchTool_Execute_NoMatches(t *testing.T) {
	// A memory manager with no facts written (only default template, which has no
	// facts in the search format). scoreRelevance always returns > 0 with its base
	// score of 0.5, so any real fact matches any query. "No matches" only occurs
	// when there are zero facts to score.
	mem := newTestMemoryManagerEmpty(t)
	tool := NewMemorySearchTool(mem)

	result := tool.Execute(nil, map[string]any{
		"query": "anything",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Output != "No matching facts found in memory." {
		t.Errorf("expected 'No matching facts found in memory.', got %q", result.Output)
	}
}

// newTestMemoryManagerEmpty creates a memory.Manager with the default template
// (no real facts), ensuring MEMORY.md exists on disk.
func newTestMemoryManagerEmpty(t *testing.T) *memory.Manager {
	t.Helper()
	ws := t.TempDir()
	mem, err := memory.New(ws)
	if err != nil {
		t.Fatalf("memory.New() error = %v", err)
	}
	// Initialize creates the default template (empty of parseable facts)
	if err := mem.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return mem
}

func TestMemorySearchTool_Execute_WithContext(t *testing.T) {
	facts := []memory.Fact{
		{
			Category:   memory.FactUserInfo,
			Content:    "Test fact for context test",
			Confidence: 0.9,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
	}

	mem := newTestMemoryManager(t, facts)
	tool := NewMemorySearchTool(mem)

	ctx := context.Background()
	result := tool.Execute(ctx, map[string]any{
		"query": "context test",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output")
	}
}

// newTestMemoryManager creates a memory.Manager with the given facts written.
func newTestMemoryManager(t *testing.T, facts []memory.Fact) *memory.Manager {
	t.Helper()
	ws := t.TempDir()
	mem, err := memory.New(ws)
	if err != nil {
		t.Fatalf("memory.New() error = %v", err)
	}

	if len(facts) > 0 {
		err = mem.WriteFacts(context.Background(), facts)
		if err != nil {
			t.Fatalf("WriteFacts() error = %v", err)
		}
	}

	return mem
}

// containsResult reports whether s contains substr (case-insensitive helper).
func containsResult(s, substr string) bool {
	return len(s) >= len(substr) && containsAtResult(s, substr, 0)
}

func containsAtResult(s, substr string, start int) bool {
	if start+len(substr) > len(s) {
		return false
	}
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
