package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/memory"
)

type MemorySearchTool struct {
	mem *memory.Manager
}

func NewMemorySearchTool(mem *memory.Manager) *MemorySearchTool {
	return &MemorySearchTool{mem: mem}
}

func (t *MemorySearchTool) Name() string { return "memory_search" }

func (t *MemorySearchTool) Description() string {
	return "memory_search: search long-term memory for user facts and past decisions."
}

func (t *MemorySearchTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "query",
			Type:        "string",
			Description: "Search text",
			Required:    true,
		},
		{
			Name:        "max_results",
			Type:        "integer",
			Description: "Max results (default 5)",
		},
	}
}

func (t *MemorySearchTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	maxResults := 5
	if m, ok := args["max_results"].(float64); ok {
		maxResults = int(m)
	}

	if t.mem == nil {
		return ToolResult{Error: fmt.Errorf("memory manager not available")}
	}

	results, err := t.mem.Search(nil, memory.SearchQuery{
		Text: query,
		Max:  maxResults,
	})
	if err != nil {
		return ToolResult{Error: fmt.Errorf("search failed: %w", err)}
	}

	// Dream insights are consolidated across turns and are found by vector
	// similarity rather than keyword overlap, so they answer questions the
	// keyword search misses. They are additive: when Dream is off,
	// SearchSimilarMemories returns nothing and the output is unchanged.
	insights, _ := t.mem.SearchSimilarMemories(context.Background(), query, maxResults)

	if len(results) == 0 && len(insights) == 0 {
		return ToolResult{Output: "No matching facts found in memory."}
	}

	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "- [%.0f%% confidence] %s\n", r.Score*100, r.Fact.Content)
	}
	now := time.Now()
	for _, in := range insights {
		fmt.Fprintf(&b, "- [%.0f%% confidence, consolidated] %s\n", in.DecayedConfidence(now)*100, in.Insight)
	}
	return ToolResult{Output: b.String()}
}
