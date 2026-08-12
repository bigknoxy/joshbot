package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/memory"
)

// memory_search is the only way a consolidated Dream insight reaches the model.
// If the insight rows are dropped the tool still answers, still finds keyword
// facts, and nothing fails — the whole consolidation stage just stops mattering.

func TestMemorySearchSurfacesConsolidatedInsightsAlongsideKeywordFacts(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()

	mem, err := memory.New(ws, memory.WithDream(memory.WithDreamMode(memory.DreamFull)))
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.WriteFacts(ctx, []memory.Fact{{
		Category: memory.FactProject, Content: "Working on the deploy pipeline", Confidence: 0.9,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{
		"the deploy pipeline is triggered from github actions",
		"github actions triggers the deploy pipeline every night",
	} {
		if err := mem.AppendHistory(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mem.Dream().Consolidate(ctx); err != nil {
		t.Fatal(err)
	}

	out := NewMemorySearchTool(mem).Execute(ctx, map[string]any{"query": "deploy pipeline"})
	if out.Error != nil {
		t.Fatalf("memory_search: %v", out.Error)
	}
	if !strings.Contains(out.Output, "consolidated") {
		t.Errorf("no consolidated insight row in the output:\n%s", out.Output)
	}
	if !strings.Contains(out.Output, "Working on the deploy pipeline") {
		t.Errorf("the keyword fact was dropped when insights were added:\n%s", out.Output)
	}
	if !strings.Contains(out.Output, "github actions") {
		t.Errorf("the insight text itself is missing:\n%s", out.Output)
	}
}

// The insight rows are additive: with Dream off the output must be exactly what
// it was before this feature existed, including the no-match wording.
func TestMemorySearchOutputIsUnchangedWhenDreamIsOff(t *testing.T) {
	ctx := context.Background()
	mem := newTestMemoryManager(t, []memory.Fact{{
		Category: memory.FactPreference, Content: "User likes Go", Confidence: 0.8,
	}})

	tool := NewMemorySearchTool(mem)

	hit := tool.Execute(ctx, map[string]any{"query": "Go"})
	if strings.Contains(hit.Output, "consolidated") {
		t.Errorf("a consolidated row appeared with Dream off:\n%s", hit.Output)
	}
	if !strings.Contains(hit.Output, "User likes Go") {
		t.Errorf("keyword search regressed:\n%s", hit.Output)
	}

	miss := NewMemorySearchTool(newTestMemoryManagerEmpty(t)).Execute(ctx, map[string]any{"query": "anything"})
	if miss.Output != "No matching facts found in memory." {
		t.Errorf("no-match wording changed: %q", miss.Output)
	}
}
