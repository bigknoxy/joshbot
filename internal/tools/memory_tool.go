package tools

import (
	"fmt"
	"strings"

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

	if len(results) == 0 {
		return ToolResult{Output: "No matching facts found in memory."}
	}

	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "- [%.0f%% confidence] %s\n", r.Score*100, r.Fact.Content)
	}
	return ToolResult{Output: b.String()}
}
