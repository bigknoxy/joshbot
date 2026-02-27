package learning

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/providers"
)

type mockProvider struct{}

func (m *mockProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{Choices: []providers.Choice{{Message: providers.Message{Content: "fact1\nfact2"}}}}, nil
}
func (m *mockProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, nil
}
func (m *mockProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", nil
}
func (m *mockProvider) Name() string             { return "mock" }
func (m *mockProvider) Config() providers.Config { return providers.DefaultConfig() }

func TestConsolidator_RunOnce(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	mem, err := memory.New(tmp)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Initialize(ctx); err != nil {
		t.Fatalf("mem.Init: %v", err)
	}

	// append some history lines
	_ = mem.AppendHistory(ctx, "Line one")
	_ = mem.AppendHistory(ctx, "Line two")

	c := NewConsolidator(mem, &mockProvider{}, 1*time.Hour)
	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce error: %v", err)
	}

	got, err := mem.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory: %v", err)
	}
	if got == "" {
		t.Fatalf("expected memory to have consolidated facts")
	}
}

func TestMergeConsolidatedFacts_ReplacesExistingSection(t *testing.T) {
	original := "# Long-Term Memory\n\n## Preferences\n- Likes concise responses\n\n## Consolidated Facts\nold fact\n"
	replacement := "\n## Consolidated Facts\nnew fact\n"

	// New behavior: merge keeps both old and new facts (up to maxFacts)
	got := mergeConsolidatedFacts(original, replacement, 20)
	if strings.Count(got, "## Consolidated Facts") != 1 {
		t.Fatalf("expected one consolidated section, got: %q", got)
	}
	if !strings.Contains(got, "new fact") {
		t.Fatalf("expected new fact in merged content, got: %q", got)
	}
	if !strings.Contains(got, "old fact") {
		t.Fatalf("expected old fact to be preserved, got: %q", got)
	}
}

func TestMergeConsolidatedFacts_ReplacesWhenSectionAtTop(t *testing.T) {
	original := "## Consolidated Facts\nold fact\n"
	replacement := "\n## Consolidated Facts\nnew fact\n"

	// New behavior: merge keeps both old and new facts
	got := mergeConsolidatedFacts(original, replacement, 20)
	if strings.Count(got, "## Consolidated Facts") != 1 {
		t.Fatalf("expected one consolidated section, got: %q", got)
	}
	if !strings.Contains(got, "old fact") {
		t.Fatalf("expected old fact to be preserved, got: %q", got)
	}
	if !strings.Contains(got, "new fact") {
		t.Fatalf("expected new fact in merged content, got: %q", got)
	}
}

func TestMergeConsolidatedFacts_EmptyMemory(t *testing.T) {
	replacement := "\n## Consolidated Facts\nnew fact\n"

	got := mergeConsolidatedFacts("", replacement, 20)
	if !strings.Contains(got, "new fact") {
		t.Fatalf("unexpected merged output for empty memory: %q", got)
	}
}

func TestMergeConsolidatedFacts_Deduplication(t *testing.T) {
	// Original has "user likes coffee"
	original := "# Long-Term Memory\n\n## Consolidated Facts\nuser likes coffee\n"
	// New facts include the same fact - should be deduplicated
	replacement := "\n## Consolidated Facts\nuser likes coffee\nuser prefers tea\n"

	got := mergeConsolidatedFacts(original, replacement, 20)

	// Should only have one occurrence of "user likes coffee"
	count := strings.Count(got, "user likes coffee")
	if count != 1 {
		t.Fatalf("expected 1 occurrence of 'user likes coffee' after dedup, got %d: %q", count, got)
	}
	// Should have the new fact
	if !strings.Contains(got, "user prefers tea") {
		t.Fatalf("expected 'user prefers tea' in merged content, got: %q", got)
	}
}

func TestMergeConsolidatedFacts_LimitsMaxFacts(t *testing.T) {
	// Original has 15 facts (more than max of 5)
	original := "# Long-Term Memory\n\n## Consolidated Facts\nfact1\nfact2\nfact3\nfact4\nfact5\nfact6\nfact7\nfact8\nfact9\nfact10\nfact11\nfact12\nfact13\nfact14\nfact15\n"
	// Add 3 new facts
	replacement := "\n## Consolidated Facts\nnewfact1\nnewfact2\nnewfact3\n"

	// Max 5 facts - should keep only 5 most recent (the 3 new + 2 old)
	got := mergeConsolidatedFacts(original, replacement, 5)

	// Count lines in consolidated section
	parts := strings.Split(got, "## Consolidated Facts")
	if len(parts) != 2 {
		t.Fatalf("expected single consolidated section, got: %q", got)
	}
	section := parts[1]
	lines := strings.Split(strings.TrimSpace(section), "\n")
	// Filter out empty lines
	var factLines []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			factLines = append(factLines, l)
		}
	}
	if len(factLines) > 5 {
		t.Fatalf("expected at most 5 facts after limit, got %d: %q", len(factLines), factLines)
	}
}

func TestMergeConsolidatedFacts_DefaultConfig(t *testing.T) {
	cfg := DefaultConsolidatorConfig()
	if cfg.HistoryLines != 12 {
		t.Fatalf("expected default HistoryLines=12, got %d", cfg.HistoryLines)
	}
	if cfg.MaxFacts != 20 {
		t.Fatalf("expected default MaxFacts=20, got %d", cfg.MaxFacts)
	}
}
