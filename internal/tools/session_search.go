package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/redact"
	"github.com/bigknoxy/joshbot/internal/session"
)

// SessionSearchTool lets the agent search its own past conversations across
// every channel — "what did we decide about X last week" answered from the
// transcripts instead of guessed. Registered only when a session manager is
// wired, like the cron tool: the agent is never offered a search over a store
// that is not there.
type SessionSearchTool struct {
	sessions *session.Manager
}

func NewSessionSearchTool(sessions *session.Manager) *SessionSearchTool {
	return &SessionSearchTool{sessions: sessions}
}

func (t *SessionSearchTool) Name() string { return "session_search" }

func (t *SessionSearchTool) Description() string {
	return "session_search: search past conversation transcripts across all channels for a phrase; returns the newest matching messages with timestamps. Use when asked about something discussed earlier that is not in the current context."
}

func (t *SessionSearchTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "query",
			Type:        "string",
			Description: "Text to search for (case-insensitive substring)",
			Required:    true,
		},
		{
			Name:        "max_results",
			Type:        "integer",
			Description: "Max matches to return (default 10)",
		},
	}
}

func (t *SessionSearchTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return ToolResult{Error: fmt.Errorf("query is required")}
	}
	limit := 10
	if m, ok := args["max_results"].(float64); ok && int(m) > 0 {
		limit = int(m)
	}

	execCtx, ok := ctx.(context.Context)
	if !ok {
		execCtx = context.Background()
	}
	matches, err := t.sessions.Search(execCtx, query, limit)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("search failed: %w", err)}
	}
	if len(matches) == 0 {
		return ToolResult{Output: fmt.Sprintf("No past messages match %q.", query)}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d match(es) for %q, newest first:\n", len(matches), query)
	for _, m := range matches {
		fmt.Fprintf(&b, "- [%s] %s (%s): %s\n",
			m.Timestamp.Format(time.DateTime), m.SessionID, m.Role, m.Snippet)
	}
	// Session files are stored unredacted (the 0600 mode is the boundary),
	// and this output re-enters a live conversation — an old transcript that
	// captured a credential must not resurface it into the current chat.
	return ToolResult{Output: redact.String(b.String())}
}
