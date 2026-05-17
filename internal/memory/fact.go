package memory

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

type FactCategory string

const (
	FactUserInfo   FactCategory = "user_info"
	FactPreference FactCategory = "preference"
	FactProject    FactCategory = "project"
	FactDecision   FactCategory = "decision"
	FactSkill      FactCategory = "skill"
	FactSystem     FactCategory = "system"
)

type Fact struct {
	ID          string       `json:"id"`
	Category    FactCategory `json:"category"`
	Content     string       `json:"content"`
	Tags        []string     `json:"tags,omitempty"`
	Source      string       `json:"source"`
	Confidence  float64      `json:"confidence"`
	AccessCount int          `json:"access_count"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// FactID generates a deterministic ID from category and content (normalized).
func FactID(category FactCategory, content string) string {
	norm := strings.ToLower(strings.TrimSpace(content))
	h := sha256.Sum256([]byte(string(category) + ":" + norm))
	return fmt.Sprintf("%x", h[:8])
}

// MergeFacts merges two facts about the same thing. Higher confidence wins;
// on equal confidence, the fact with the later UpdatedAt wins.
func MergeFacts(a, b Fact) Fact {
	result := a

	bWinsConfidence := b.Confidence > a.Confidence
	bWinsRecency := b.Confidence == a.Confidence && b.UpdatedAt.After(a.UpdatedAt)
	if bWinsConfidence || bWinsRecency {
		result.Content = b.Content
		result.Confidence = b.Confidence
		result.UpdatedAt = b.UpdatedAt
	}

	if b.AccessCount > a.AccessCount {
		result.AccessCount = b.AccessCount
	}

	// Merge tags, preserving order, deduplicating.
	seen := make(map[string]bool, len(a.Tags)+len(b.Tags))
	merged := make([]string, 0, len(a.Tags)+len(b.Tags))
	for _, t := range a.Tags {
		if !seen[t] {
			seen[t] = true
			merged = append(merged, t)
		}
	}
	for _, t := range b.Tags {
		if !seen[t] {
			seen[t] = true
			merged = append(merged, t)
		}
	}
	result.Tags = merged

	return result
}

// ClampConfidence clamps c to [0, 1].
func ClampConfidence(c float64) float64 {
	switch {
	case c < 0:
		return 0
	case c > 1:
		return 1
	default:
		return c
	}
}
