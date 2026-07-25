package memory

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// osReadFile is overridable in tests.
var osReadFile = os.ReadFile

type SearchQuery struct {
	Text     string
	Category FactCategory // "" for all
	Tags     []string
	Max      int // default 5
}

type SearchResult struct {
	Fact  Fact
	Score float64
}

// Search performs keyword search across facts with relevance scoring.
func (m *Manager) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	facts, err := m.loadFactsLocked()
	if err != nil {
		return nil, fmt.Errorf("load facts for search: %w", err)
	}

	if q.Max <= 0 {
		q.Max = 5
	}

	var results []SearchResult
	queryLower := strings.ToLower(strings.TrimSpace(q.Text))
	queryWords := strings.Fields(queryLower)

	for _, fact := range facts {
		if q.Category != "" && fact.Category != q.Category {
			continue
		}

		if len(q.Tags) > 0 && !hasAnyTag(fact.Tags, q.Tags) {
			continue
		}

		if score := scoreRelevance(fact, queryWords); score > 0 {
			results = append(results, SearchResult{Fact: fact, Score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > q.Max {
		results = results[:q.Max]
	}

	return results, nil
}

func scoreRelevance(fact Fact, queryWords []string) float64 {
	score := 0.5 // base score for any fact
	factLower := strings.ToLower(fact.Content)

	// Keyword overlap
	for _, w := range queryWords {
		if strings.Contains(factLower, w) {
			score += 0.3
		}
	}

	// Recency: newer facts score higher (within 30 days)
	daysSince := time.Since(fact.UpdatedAt).Hours() / 24
	recencyBoost := math.Max(0, 1.0-daysSince/30.0) * 0.2
	score += recencyBoost

	// Confidence boost
	score += fact.Confidence * 0.2

	// Access count boost (diminishing returns)
	accessBoost := math.Log2(float64(fact.AccessCount+1)) * 0.05
	score += accessBoost

	return score
}

// loadFactsLocked reads memory file and parses categorized facts.
// Must be called with at least RLock held.
func (m *Manager) loadFactsLocked() ([]Fact, error) {
	data, err := osReadFile(m.MemoryPath())
	if err != nil {
		return nil, err
	}
	return parseFacts(string(data))
}

// parseFacts parses categorized facts from markdown content.
// Format:
// ## category_name
// - [timestamp] content (confidence: N, tags: ...)
func parseFacts(content string) ([]Fact, error) {
	var facts []Fact
	var currentCategory FactCategory

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Category header
		if strings.HasPrefix(trimmed, "## ") {
			cat := strings.TrimPrefix(trimmed, "## ")
			currentCategory = FactCategory(strings.TrimSpace(cat))
			continue
		}

		// Fact line (with or without timestamp)
		if strings.HasPrefix(trimmed, "- ") && currentCategory != "" {
			fact := parseFactLine(trimmed, currentCategory)
			if fact != nil {
				facts = append(facts, *fact)
			}
		}
	}

	return facts, nil
}

// parseFactLine parses a single fact line like:
// - [2026-05-17] User prefers dark mode (confidence: 0.9, tags: work, frontend)
func parseFactLine(line string, category FactCategory) *Fact {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "- "))

	var createdAt time.Time
	if strings.HasPrefix(rest, "[") {
		if end := strings.Index(rest, "]"); end > 0 {
			createdAt, _ = time.Parse("2006-01-02", rest[1:end])
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	confidence := 1.0
	var tags []string
	content := rest

	if idx := strings.LastIndex(rest, "(confidence:"); idx > 0 {
		content = strings.TrimSpace(rest[:idx])
		metaStr := rest[idx+1:]
		if end := strings.Index(metaStr, ")"); end > 0 {
			metaContent := metaStr[:end]

			// Parse confidence value (up to next comma or end)
			if confIdx := strings.Index(metaContent, "confidence:"); confIdx >= 0 {
				confPart := metaContent[confIdx+len("confidence:"):]
				if commaIdx := strings.Index(confPart, ","); commaIdx > 0 {
					confPart = confPart[:commaIdx]
				}
				fmt.Sscanf(strings.TrimSpace(confPart), "%f", &confidence)
			}

			// Parse tags — everything after "tags:" until end of metadata
			if tagsIdx := strings.Index(metaContent, "tags:"); tagsIdx >= 0 {
				tagsPart := metaContent[tagsIdx+len("tags:"):]
				for _, tag := range strings.Split(tagsPart, ",") {
					if t := strings.TrimSpace(tag); t != "" {
						tags = append(tags, t)
					}
				}
			}
		}
	}

	if content == "" {
		return nil
	}

	return &Fact{
		ID:         FactID(category, content),
		Category:   category,
		Content:    content,
		Tags:       tags,
		Confidence: ClampConfidence(confidence),
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
}

// hasAnyTag reports whether factTags contains any of the query tags (case-insensitive).
func hasAnyTag(factTags []string, queryTags []string) bool {
	for _, qt := range queryTags {
		for _, ft := range factTags {
			if strings.EqualFold(qt, ft) {
				return true
			}
		}
	}
	return false
}

// FormatFactMarkdown formats a fact as a markdown line.
func FormatFactMarkdown(f Fact) string {
	dateStr := f.CreatedAt.Format("2006-01-02")
	meta := fmt.Sprintf("confidence: %.1f", f.Confidence)
	if len(f.Tags) > 0 {
		meta += fmt.Sprintf(", tags: %s", strings.Join(f.Tags, ", "))
	}
	return fmt.Sprintf("- [%s] %s (%s)", dateStr, f.Content, meta)
}
