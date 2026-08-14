package session

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SearchMatch is one message that matched a session search.
type SearchMatch struct {
	SessionID string
	Role      Role
	Snippet   string // the matching message flattened and trimmed around the hit
	Timestamp time.Time
}

// maxSnippetRunes bounds a snippet so a match inside a pasted document does
// not return the document.
const maxSnippetRunes = 200

// Search scans every live session transcript for messages containing query
// (case-insensitive) and returns the newest matches first, capped at limit.
//
// It is deliberately grep-grade: session files are line-delimited JSON read
// with the same leniency as Load (an unparseable line is skipped, never
// fatal), sidecars are excluded via isSessionFile, and nothing is written —
// like ListInfo, an inventory operation must not quarantine or rewrite.
// Compaction summaries are searched too: after a compaction they are the only
// remaining record of the earlier conversation.
func (m *Manager) Search(ctx context.Context, query string, limit int) ([]SearchMatch, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	needle := strings.ToLower(query)

	m.mu.RLock()
	defer m.mu.RUnlock()

	entries, err := os.ReadDir(m.sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var matches []SearchMatch
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if entry.IsDir() {
			continue
		}
		id, ok := isSessionFile(entry.Name())
		if !ok {
			continue
		}
		file, err := os.Open(filepath.Join(m.sessionsDir, entry.Name()))
		if err != nil {
			continue // transient read errors skip the session, not the search
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var msg Message
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			if msg.Content == "" {
				continue
			}
			if !strings.Contains(strings.ToLower(msg.Content), needle) {
				continue
			}
			matches = append(matches, SearchMatch{
				SessionID: id,
				Role:      msg.Role,
				Snippet:   snippetAround(msg.Content, needle),
				Timestamp: msg.Timestamp,
			})
		}
		file.Close()
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Timestamp.After(matches[j].Timestamp)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// snippetAround flattens content to one line and windows it around the first
// occurrence of needle (already lower-cased).
func snippetAround(content, needle string) string {
	flat := strings.Join(strings.Fields(content), " ")
	idx := strings.Index(strings.ToLower(flat), needle)
	if idx < 0 {
		idx = 0
	}
	runes := []rune(flat)
	// Byte index -> rune index for the window start.
	start := len([]rune(flat[:idx]))
	from := start - maxSnippetRunes/4
	if from < 0 {
		from = 0
	}
	to := from + maxSnippetRunes
	if to > len(runes) {
		to = len(runes)
	}
	s := string(runes[from:to])
	if from > 0 {
		s = "…" + s
	}
	if to < len(runes) {
		s += "…"
	}
	return s
}
