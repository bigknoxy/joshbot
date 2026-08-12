package learning

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/memory"
)

// --- stripListMarker ---

func TestStripListMarker_Bulleted(t *testing.T) {
	tests := []struct {
		input  string
		want   string
		reason string
	}{
		{"- the actual fact", "the actual fact", "dash bullet"},
		{"* another fact", "another fact", "asterisk bullet"},
		{"• bullet point", "bullet point", "unicode bullet"},
	}
	for _, tt := range tests {
		have := stripListMarker(tt.input)
		if have != tt.want {
			t.Errorf("stripListMarker(%q) = %q; want %q (%s)", tt.input, have, tt.want, tt.reason)
		}
	}
}

func TestStripListMarker_Numbered(t *testing.T) {
	tests := []struct {
		input  string
		want   string
		reason string
	}{
		{"1. numbered item", "numbered item", "dot-terminated"},
		{"42) alternate numbering", "alternate numbering", "paren-terminated"},
		// Only digits followed by . or ) should be stripped.
		{"abc123 def", "abc123 def", "no leading digit run — stays"},
		{"7. numbered end", "numbered end", "bare digit + dot"},
	}
	for _, tt := range tests {
		have := stripListMarker(tt.input)
		if have != tt.want {
			t.Errorf("stripListMarker(%q) = %q; want %q (%s)", tt.input, have, tt.want, tt.reason)
		}
	}
}

// --- looksLikeRefusalOrMeta ---

func TestLooksLikeRefusalOrMeta_RejectionPhrases(t *testing.T) {
	for _, phrase := range []string{
		"I don't have enough context to help.",
		"As an AI, I cannot do that.",
		"Unable to extract meaningful facts.",
		"I'm sorry but no factual content exists here.",
		"Here are the facts from our chat",
	} {
		if !looksLikeRefusalOrMeta(phrase) {
			t.Errorf("expected refusal detection for: %q", phrase)
		}
	}
}

func TestLooksLikeRefusalOrMeta_LegitimateFactsPass(t *testing.T) {
	for _, fact := range []string{
		"user prefers coffee over tea",
		"deployment uses systemd, not docker",
		"I cannot wait for the launch — user's project milestone", // "cannot" without refusal shape
	} {
		if looksLikeRefusalOrMeta(fact) {
			t.Errorf("false positive on legitimate fact: %q", fact)
		}
	}
}

// --- extractValidFacts — the content gate itself ---

func TestExtractValidFacts_RejectsTooLongLines(t *testing.T) {
	longLine := strings.Repeat("word ", 600) // well past maxFactContentLength (300)
	facts, reason := extractValidFacts(longLine+" and this too", 10, 20)

	if len(facts) != 0 {
		t.Errorf("expected no facts from an over-length blob, got %d", len(facts))
	}
	if !strings.Contains(reason, "length") {
		t.Errorf("reason does not mention the length rule: %q", reason)
	}
}

func TestExtractValidFacts_MixedInputKeepsGoodLines(t *testing.T) {
	input := `- valid fact one
- I don't have enough information to help you with the second item
- valid fact two
this line is way too long: ` + strings.Repeat("x", 500) + `
- another good fact`

	facts, reason := extractValidFacts(input, maxFactContentLength, 20)

	if len(facts) != 3 {
		t.Fatalf("expected 3 surviving facts, got %d: %v", len(facts), facts)
	}
	if reason != "" {
		t.Errorf("expected no rejection reason when some lines survive, got: %q", reason)
	}
	if facts[0] != "valid fact one" {
		t.Errorf("fact[0] = %q (stripListMarker must fire)", facts[0])
	}
}

func TestExtractValidFacts_RespectsMaxFactsCap(t *testing.T) {
	lines := make([]string, 15)
	for i := range lines {
		lines[i] = strings.Repeat("x", 100) // short enough, valid
	}
	input := strings.Join(lines, "\n")
	facts, reason := extractValidFacts(input, maxFactContentLength, 5)

	if len(facts) != 5 {
		t.Errorf("expected exactly 5 (the cap), got %d — maxFacts was not enforced", len(facts))
	}
	if reason != "" {
		t.Errorf("should not report rejection when facts survived the cap: %q", reason)
	}
}

// --- heuristicFallback (0% before — a regression here silently degrades memory quality) ---

func TestConsolidator_HeuristicFallback(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	mem, err := memory.New(tmp)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Initialize(ctx); err != nil {
		t.Fatalf("mem.Initialize: %v", err)
	}

	c := NewConsolidator(mem, nil, time.Hour) // provider == nil → heuristicFallback path is taken

	lines := []string{
		"user asked about systemd deployment",
		"agent explained the service file structure",
		"- user prefers coffee",
		"a random line with no colon or dash",
	}
	for _, l := range lines {
		if err := mem.AppendHistory(ctx, l); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
	}

	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce (heuristic path): %v", err)
	}

	got, err := mem.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory: %v", err)
	}
	// heuristicFallback picks lines with ":" or prefix "- ". It writes them as
	// the consolidated section. The key is that it actually writes something,
	// not that MEMORY.md is empty (which means a regression in the fallback).
	if !strings.Contains(got, "## Consolidated") {
		t.Errorf("heuristicFallback did not write a consolidated section:\n%s", got)
	}
}

func TestConsolidator_HeuristicFallback_NoColonsOrDashes(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	mem, err := memory.New(tmp)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Initialize(ctx); err != nil {
		t.Fatalf("mem.Initialize: %v", err)
	}

	c := NewConsolidator(mem, nil, time.Hour) // no provider → fallback taken
	// History lines that have neither ":" nor "- " — heuristicFallback falls
	// back to all lines.
	for _, l := range []string{"just plain text", "another plain line"} {
		if err := mem.AppendHistory(ctx, l); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
	}

	baseline, err := mem.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory baseline: %v", err)
	}

	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	got, err := mem.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory after: %v", err)
	}
	// When no colon/dash lines exist, the fallback still writes ALL the lines
	// so something useful is remembered, not lost.
	if strings.TrimSpace(got) == baseline || got == "" {
		t.Errorf("expected consolidated content even when no ':' or '- ' found:\nbaseline=%q\ngot=%q", baseline, got)
	}
}

func TestConsolidator_RunOnce_WithNilMemoryManager(t *testing.T) {
	c := NewConsolidator(nil, &mockProvider{}, time.Hour)
	if err := c.RunOnce(context.Background()); err == nil {
		t.Fatal("expected an error when memory manager is nil")
	}
}

func TestConsolidator_RunOnce_WithEmptyHistory(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	mem, err := memory.New(tmp)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Initialize(ctx); err != nil {
		t.Fatalf("mem.Initialize: %v", err)
	}

	c := NewConsolidator(mem, &mockProvider{}, time.Hour)
	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("empty history must not fail: %v", err)
	}
}

func TestPreviewString(t *testing.T) {
	short := "hello"
	if got := previewString(short, 10); got != short {
		t.Errorf("short string truncated unexpectedly: %q", got)
	}
	long := strings.Repeat("x", 200)
	trunc := previewString(long, 80)
	if len(trunc) > 83 { // 80 + "..."
		t.Errorf("preview still too long after trim: %d bytes", len(trunc))
	}
	if !strings.HasSuffix(trunc, "...") {
		t.Error("expected truncation suffix on trimmed preview")
	}
}

// --- NewConsolidatorWithConfig — default normalization ---

func TestNewConsolidatorWithConfig_NormalizesNegativeValues(t *testing.T) {
	mem, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	c := NewConsolidatorWithConfig(mem, nil, -1*time.Second, ConsolidatorConfig{
		HistoryLines: -5,
		MaxFacts:     0,
	})
	if c.interval != 10*time.Minute {
		t.Errorf("negative interval not normalized to default: %v", c.interval)
	}
	if c.historyLines != 12 {
		t.Errorf("negative HistoryLines not normalized: %d", c.historyLines)
	}
	if c.maxFacts != 20 {
		t.Errorf("zero MaxFacts not normalized: %d", c.maxFacts)
	}
}
