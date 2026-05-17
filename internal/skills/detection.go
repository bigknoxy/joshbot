package skills

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ToolCallRecord captures a single tool usage within a trace.
type ToolCallRecord struct {
	Tool   string
	Args   map[string]any
	Result string // truncated to 200 chars
}

// Trace captures an entire agent ReAct turn: the user message, each tool call,
// and the final output returned to the user.
type Trace struct {
	UserMessage string
	ToolCalls   []ToolCallRecord
	FinalOutput string
}

// CandidateSkill represents a detected skill candidate awaiting extraction.
type CandidateSkill struct {
	Name        string
	Description string
	Trace       Trace
	Confidence  float64
}

// PatternStats tracks how often a tool-call pattern has been observed.
type PatternStats struct {
	Pattern   string
	ToolCount int
	Sessions  []string
	LastSeen  time.Time
	Frequency int
}

// SkillDetector observes agent traces, tracks repeated tool-call patterns
// across sessions, and signals when a pattern is mature enough to become a skill.
type SkillDetector struct {
	mu           sync.RWMutex
	patternCount map[string]*PatternStats
}

// NewSkillDetector creates an empty SkillDetector.
func NewSkillDetector() *SkillDetector {
	return &SkillDetector{
		patternCount: make(map[string]*PatternStats),
	}
}

// RecordTrace stores the pattern observed in this trace so it can inform future
// detection decisions. Traces with fewer than one tool call are ignored.
func (d *SkillDetector) RecordTrace(sessionID string, trace Trace) {
	d.mu.Lock()
	defer d.mu.Unlock()

	pattern := patternKey(trace.ToolCalls)
	if pattern == "" {
		return
	}

	stats, ok := d.patternCount[pattern]
	if !ok {
		stats = &PatternStats{
			Pattern:   pattern,
			ToolCount: len(trace.ToolCalls),
			Sessions:  make([]string, 0),
		}
		d.patternCount[pattern] = stats
	}

	stats.Frequency++
	stats.LastSeen = time.Now()

	for _, s := range stats.Sessions {
		if s == sessionID {
			return
		}
	}
	stats.Sessions = append(stats.Sessions, sessionID)
}

// Detect scores the given trace against detection heuristics. Returns a
// CandidateSkill if the weighted sum meets or exceeds the threshold of 2.0.
func (d *SkillDetector) Detect(trace Trace) *CandidateSkill {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var confidence float64

	if len(trace.ToolCalls) >= 3 {
		confidence += 1.0
	}

	if pattern := patternKey(trace.ToolCalls); pattern != "" {
		if stats, ok := d.patternCount[pattern]; ok && len(stats.Sessions) > 1 {
			confidence += 2.0
		}
	}

	msgLower := strings.ToLower(trace.UserMessage)
	if containsAny(msgLower, "create a skill", "create skill", "make a skill",
		"create a tool", "save this as", "remember this as a skill") {
		confidence += 3.0
	}
	if containsAny(msgLower, "can you always", "always do this",
		"do this every time", "make this automatic") {
		confidence += 2.0
	}

	if len(trace.ToolCalls) > 5 {
		confidence += 0.5
	}

	outputLower := strings.ToLower(trace.FinalOutput)
	if containsAny(outputLower, "template", "script", "reusable", "parameter",
		"usage:", "how to use", "steps:", "step 1", "procedure") {
		confidence += 1.5
	}

	if confidence < 2.0 {
		return nil
	}

	return &CandidateSkill{
		Name:        suggestName(trace),
		Description: fmt.Sprintf("Skill discovered from %d-tool sequence with confidence %.1f", len(trace.ToolCalls), confidence),
		Trace:       trace,
		Confidence:  confidence,
	}
}

// Candidates returns a snapshot of all tracked patterns as CandidateSkills.
func (d *SkillDetector) Candidates() []CandidateSkill {
	d.mu.RLock()
	defer d.mu.RUnlock()

	candidates := make([]CandidateSkill, 0, len(d.patternCount))
	for _, stats := range d.patternCount {
		candidates = append(candidates, CandidateSkill{
			Name:        stats.Pattern,
			Description: fmt.Sprintf("Pattern %s seen %d times in %d sessions", stats.Pattern, stats.Frequency, len(stats.Sessions)),
			Trace:       Trace{},
			Confidence:  float64(min(stats.Frequency, 10)) / 10.0,
		})
	}
	return candidates
}

// patternKey builds an arrow-separated string from tool names in the record slice.
// Returns "" when there are no tool calls to key on.
func patternKey(calls []ToolCallRecord) string {
	if len(calls) == 0 {
		return ""
	}
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Tool
	}
	return strings.Join(names, "→")
}

// suggestName produces a plausible skill name from the trace.
func suggestName(trace Trace) string {
	if len(trace.ToolCalls) == 0 {
		return "untitled-skill"
	}
	return fmt.Sprintf("%s-skill", trace.ToolCalls[0].Tool)
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, phrases ...string) bool {
	for _, p := range phrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
