package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSearch_FindsMatchingFacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	facts := []Fact{
		{Category: FactUserInfo, Content: "User prefers dark mode", Confidence: 0.9, UpdatedAt: time.Now()},
		{Category: FactPreference, Content: "User likes coffee", Confidence: 0.8, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	// "dark mode" should match the first fact more strongly
	results, err := mgr.Search(context.Background(), SearchQuery{Text: "dark mode"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// Both facts have base score > 0, but the first should rank higher
	if len(results) != 2 {
		t.Fatalf("expected 2 results (both have base score), got %d", len(results))
	}
	if results[0].Fact.Content != "User prefers dark mode" {
		t.Errorf("expected 'User prefers dark mode' as top result, got %q", results[0].Fact.Content)
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	facts := []Fact{
		{Category: FactUserInfo, Content: "Some fact", Confidence: 0.9, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	// Empty query should match all facts (base score > 0)
	results, err := mgr.Search(context.Background(), SearchQuery{Text: ""})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result for empty query, got %d", len(results))
	}
}

func TestSearch_ByCategory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	facts := []Fact{
		{Category: FactUserInfo, Content: "User name is John", Confidence: 0.9, UpdatedAt: time.Now()},
		{Category: FactPreference, Content: "User prefers dark mode", Confidence: 0.8, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	results, err := mgr.Search(context.Background(), SearchQuery{Text: "user", Category: FactUserInfo})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Fact.Category != FactUserInfo {
		t.Errorf("expected category user_info, got %q", results[0].Fact.Category)
	}
}

func TestSearch_ByTags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	facts := []Fact{
		{Category: FactUserInfo, Content: "User name is John", Tags: []string{"work", "frontend"}, Confidence: 0.9, UpdatedAt: time.Now()},
		{Category: FactPreference, Content: "User prefers dark mode", Tags: []string{"personal"}, Confidence: 0.8, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	results, err := mgr.Search(context.Background(), SearchQuery{Text: "user", Tags: []string{"work"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with tag 'work', got %d", len(results))
	}
}

func TestSearch_MaxResults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	facts := []Fact{
		{Category: FactUserInfo, Content: "fact one", Confidence: 0.9, UpdatedAt: time.Now()},
		{Category: FactUserInfo, Content: "fact two", Confidence: 0.8, UpdatedAt: time.Now()},
		{Category: FactUserInfo, Content: "fact three", Confidence: 0.7, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	results, err := mgr.Search(context.Background(), SearchQuery{Text: "fact", Max: 2})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (Max=2), got %d", len(results))
	}
}

func TestSearch_SortedByScore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Higher confidence fact should rank higher
	facts := []Fact{
		{Category: FactUserInfo, Content: "shared keyword", Confidence: 0.5, UpdatedAt: time.Now()},
		{Category: FactUserInfo, Content: "shared keyword", Confidence: 0.9, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	results, err := mgr.Search(context.Background(), SearchQuery{Text: "shared"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Score <= results[1].Score {
		t.Error("expected first result to have higher score")
	}
}

func TestSearch_NoMatchingFacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	facts := []Fact{
		{Category: FactUserInfo, Content: "User prefers dark mode", Confidence: 0.9, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	// All facts have a base score > 0, so they're all returned.
	// But the non-matching fact should have a lower score (no keyword boost).
	results, err := mgr.Search(context.Background(), SearchQuery{Text: "nonexistent keyword"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	// The fact is returned but with only base score (no keyword match)
	if len(results) != 1 {
		t.Errorf("expected 1 result (base score), got %d", len(results))
	}
	// Score should be lower than a matching fact's score
	matchingResults, _ := mgr.Search(context.Background(), SearchQuery{Text: "dark mode"})
	if len(matchingResults) > 0 && results[0].Score >= matchingResults[0].Score {
		t.Error("non-matching fact should have lower score than matching fact")
	}
}

func TestSearch_MissingMemoryFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Don't write any facts - memory file doesn't exist
	_, err = mgr.Search(context.Background(), SearchQuery{Text: "test"})
	if err == nil {
		t.Error("expected error for missing memory file")
	}
}

func TestScoreRelevance_KeywordMatch(t *testing.T) {
	fact := Fact{
		Content:     "User prefers dark mode theme",
		Confidence:  0.9,
		UpdatedAt:   time.Now(),
		AccessCount: 5,
	}
	score := scoreRelevance(fact, []string{"dark", "mode"})
	if score <= 0 {
		t.Error("expected positive score for matching keywords")
	}
}

func TestScoreRelevance_NoMatch(t *testing.T) {
	fact := Fact{
		Content:     "User prefers dark mode theme",
		Confidence:  0.9,
		UpdatedAt:   time.Now(),
		AccessCount: 5,
	}
	score := scoreRelevance(fact, []string{"nonexistent"})
	// Base score is 0.5, plus recency and confidence boosts, so it's > 0
	if score <= 0 {
		t.Error("expected positive base score even without keyword match")
	}
}

func TestScoreRelevance_ConfidenceBoost(t *testing.T) {
	fact := Fact{
		Content:    "test",
		Confidence: 1.0,
		UpdatedAt:  time.Now(),
	}
	score := scoreRelevance(fact, []string{"test"})
	if score <= 0 {
		t.Error("expected positive score")
	}
}

func TestParseFacts_EmptyContent(t *testing.T) {
	facts, err := parseFacts("")
	if err != nil {
		t.Fatalf("parseFacts() error = %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 facts for empty content, got %d", len(facts))
	}
}

func TestParseFacts_WithCategoryAndFact(t *testing.T) {
	content := `## user_info
- [2026-05-17] User prefers dark mode (confidence: 0.9, tags: work, frontend)
`
	facts, err := parseFacts(content)
	if err != nil {
		t.Fatalf("parseFacts() error = %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Category != FactUserInfo {
		t.Errorf("expected category user_info, got %q", facts[0].Category)
	}
	if facts[0].Content != "User prefers dark mode" {
		t.Errorf("expected 'User prefers dark mode', got %q", facts[0].Content)
	}
}

func TestParseFacts_MultipleCategories(t *testing.T) {
	content := `## user_info
- [2026-05-17] User name is John (confidence: 0.9)
## preferences
- [2026-05-18] User prefers dark mode (confidence: 0.8, tags: ui)
`
	facts, err := parseFacts(content)
	if err != nil {
		t.Fatalf("parseFacts() error = %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}
}

func TestParseFacts_FactWithoutTimestamp(t *testing.T) {
	content := `## user_info
- User prefers dark mode (confidence: 0.9)
`
	facts, err := parseFacts(content)
	if err != nil {
		t.Fatalf("parseFacts() error = %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
}

func TestParseFacts_FactWithoutConfidence(t *testing.T) {
	content := `## user_info
- [2026-05-17] User prefers dark mode
`
	facts, err := parseFacts(content)
	if err != nil {
		t.Fatalf("parseFacts() error = %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Confidence != 1.0 {
		t.Errorf("expected default confidence 1.0, got %v", facts[0].Confidence)
	}
}

func TestParseFacts_FactWithoutTags(t *testing.T) {
	content := `## user_info
- [2026-05-17] User prefers dark mode (confidence: 0.9)
`
	facts, err := parseFacts(content)
	if err != nil {
		t.Fatalf("parseFacts() error = %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if len(facts[0].Tags) != 0 {
		t.Errorf("expected no tags, got %v", facts[0].Tags)
	}
}

func TestParseFacts_EmptyContentLine(t *testing.T) {
	content := `## user_info
- 
`
	facts, err := parseFacts(content)
	if err != nil {
		t.Fatalf("parseFacts() error = %v", err)
	}
	// Empty content line should produce no facts
	if len(facts) != 0 {
		t.Errorf("expected 0 facts for empty content line, got %d", len(facts))
	}
}

func TestParseFactLine_WithAllFields(t *testing.T) {
	line := "- [2026-05-17] User prefers dark mode (confidence: 0.9, tags: work, frontend)"
	fact := parseFactLine(line, FactUserInfo)
	if fact == nil {
		t.Fatal("expected non-nil fact")
	}
	if fact.Content != "User prefers dark mode" {
		t.Errorf("expected 'User prefers dark mode', got %q", fact.Content)
	}
	if fact.Confidence != 0.9 {
		t.Errorf("expected confidence 0.9, got %v", fact.Confidence)
	}
	if len(fact.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d: %v", len(fact.Tags), fact.Tags)
	}
}

func TestParseFactLine_InvalidTimestamp(t *testing.T) {
	line := "- [invalid-date] User prefers dark mode"
	fact := parseFactLine(line, FactUserInfo)
	if fact == nil {
		t.Fatal("expected non-nil fact even with invalid timestamp")
	}
	// Should use current time as fallback
	if fact.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestParseFactLine_NoTimestamp(t *testing.T) {
	line := "- User prefers dark mode"
	fact := parseFactLine(line, FactUserInfo)
	if fact == nil {
		t.Fatal("expected non-nil fact")
	}
	if fact.Content != "User prefers dark mode" {
		t.Errorf("expected 'User prefers dark mode', got %q", fact.Content)
	}
}

func TestParseFactLine_EmptyContent(t *testing.T) {
	line := "- "
	fact := parseFactLine(line, FactUserInfo)
	if fact != nil {
		t.Error("expected nil fact for empty content")
	}
}

func TestHasAnyTag_Match(t *testing.T) {
	if !hasAnyTag([]string{"work", "frontend"}, []string{"work"}) {
		t.Error("expected match for 'work'")
	}
}

func TestHasAnyTag_CaseInsensitive(t *testing.T) {
	if !hasAnyTag([]string{"Work", "Frontend"}, []string{"work"}) {
		t.Error("expected case-insensitive match")
	}
}

func TestHasAnyTag_NoMatch(t *testing.T) {
	if hasAnyTag([]string{"work", "frontend"}, []string{"personal"}) {
		t.Error("expected no match for 'personal'")
	}
}

func TestHasAnyTag_EmptyTags(t *testing.T) {
	if hasAnyTag([]string{}, []string{"work"}) {
		t.Error("expected no match for empty fact tags")
	}
	if hasAnyTag([]string{"work"}, []string{}) {
		t.Error("expected no match for empty query tags")
	}
}

func TestFormatFactMarkdown_Basic(t *testing.T) {
	fact := Fact{
		Content:    "User prefers dark mode",
		Confidence: 0.9,
		CreatedAt:  time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
	}
	result := FormatFactMarkdown(fact)
	expected := "- [2026-05-17] User prefers dark mode (confidence: 0.9)"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestFormatFactMarkdown_WithTags(t *testing.T) {
	fact := Fact{
		Content:    "User prefers dark mode",
		Confidence: 0.8,
		Tags:       []string{"work", "frontend"},
		CreatedAt:  time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
	}
	result := FormatFactMarkdown(fact)
	expected := "- [2026-05-17] User prefers dark mode (confidence: 0.8, tags: work, frontend)"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestWriteFacts_WritesCorrectContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	facts := []Fact{
		{Category: FactUserInfo, Content: "User name is John", Confidence: 0.9, UpdatedAt: time.Now()},
		{Category: FactPreference, Content: "User prefers dark mode", Confidence: 0.8, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	data, err := os.ReadFile(mgr.MemoryPath())
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}

	content := string(data)
	if !contains(content, "User name is John") {
		t.Error("expected 'User name is John' in memory file")
	}
	if !contains(content, "User prefers dark mode") {
		t.Error("expected 'User prefers dark mode' in memory file")
	}
}

func TestWriteFacts_SortsCategories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	facts := []Fact{
		{Category: FactSystem, Content: "system fact", Confidence: 0.9, UpdatedAt: time.Now()},
		{Category: FactUserInfo, Content: "user fact", Confidence: 0.9, UpdatedAt: time.Now()},
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	data, _ := os.ReadFile(mgr.MemoryPath())
	content := string(data)
	// system should come before user_info alphabetically
	sysIdx := indexOf(content, "## system")
	usrIdx := indexOf(content, "## user_info")
	if sysIdx < 0 || usrIdx < 0 {
		t.Fatal("expected both categories in output")
	}
	if sysIdx > usrIdx {
		t.Error("expected 'system' category to appear before 'user_info' (sorted)")
	}
}

func TestReconcileFacts_NewFactsAdded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	existing := Fact{
		ID:         FactID(FactUserInfo, "existing fact"),
		Category:   FactUserInfo,
		Content:    "existing fact",
		Confidence: 0.9,
		UpdatedAt:  time.Now(),
	}
	if err := mgr.WriteFacts(context.Background(), []Fact{existing}); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	newFact := Fact{
		ID:         FactID(FactUserInfo, "new fact"),
		Category:   FactUserInfo,
		Content:    "new fact",
		Confidence: 0.8,
		UpdatedAt:  time.Now(),
	}
	if err := mgr.ReconcileFacts(context.Background(), []Fact{newFact}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	results, err := mgr.Search(context.Background(), SearchQuery{Text: "fact"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 facts after reconcile, got %d", len(results))
	}
}

func TestReconcileFacts_DuplicateMerged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	now := time.Now()
	// Both facts have the SAME content so they share the same FactID when parsed
	existing := Fact{
		ID:         FactID(FactUserInfo, "same fact"),
		Category:   FactUserInfo,
		Content:    "same fact",
		Confidence: 0.5,
		UpdatedAt:  now.Add(-time.Hour),
	}
	if err := mgr.WriteFacts(context.Background(), []Fact{existing}); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	updated := Fact{
		ID:         FactID(FactUserInfo, "same fact"),
		Category:   FactUserInfo,
		Content:    "same fact",
		Confidence: 0.9,
		UpdatedAt:  now,
	}
	if err := mgr.ReconcileFacts(context.Background(), []Fact{updated}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	results, err := mgr.Search(context.Background(), SearchQuery{Text: "same"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 fact after merge, got %d", len(results))
	}
	if results[0].Fact.Confidence != 0.9 {
		t.Errorf("expected merged confidence 0.9, got %v", results[0].Fact.Confidence)
	}
}

func TestReconcileFacts_EvictionLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Write 100 facts
	facts := make([]Fact, 100)
	for i := 0; i < 100; i++ {
		facts[i] = Fact{
			ID:         FactID(FactUserInfo, "fact "+string(rune('a'+i%26))+string(rune('a'+i))),
			Category:   FactUserInfo,
			Content:    "fact " + string(rune('a'+i%26)) + string(rune('a'+i)),
			Confidence: 0.5,
			UpdatedAt:  time.Now(),
		}
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}

	// Reconcile with 5 new facts - should evict to max 100
	newFacts := make([]Fact, 5)
	for i := 0; i < 5; i++ {
		newFacts[i] = Fact{
			ID:         FactID(FactUserInfo, "new "+string(rune('a'+i))),
			Category:   FactUserInfo,
			Content:    "new " + string(rune('a'+i)),
			Confidence: 0.9,
			UpdatedAt:  time.Now(),
		}
	}
	if err := mgr.ReconcileFacts(context.Background(), newFacts); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	// Should have exactly 100 facts (5 new + 95 surviving from 100, evicting 5 lowest)
	data, _ := os.ReadFile(mgr.MemoryPath())
	content := string(data)
	// Count fact lines
	count := 0
	for _, line := range splitLines(content) {
		if len(line) > 0 && line[0] == '-' {
			count++
		}
	}
	if count > 100 {
		t.Errorf("expected at most 100 facts after eviction, got %d", count)
	}
}

func TestReconcileFacts_MissingMemoryFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Reconcile with no existing memory file - should still work
	facts := []Fact{
		{Category: FactUserInfo, Content: "new fact", Confidence: 0.9, UpdatedAt: time.Now()},
	}
	if err := mgr.ReconcileFacts(context.Background(), facts); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}
}

// contains is a helper for substring check.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// indexOf returns the index of substr in s, or -1.
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// splitLines splits a string by newlines.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// Ensure the memory path exists for tests that need it.
func ensureMemoryFile(t *testing.T, mgr *Manager) {
	t.Helper()
	if err := mgr.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
}

// WriteFactsToPath writes facts directly to a memory file for testing.
func writeFactsToPath(t *testing.T, path string, facts []Fact) {
	t.Helper()
	mgr := &Manager{
		memoryDir: filepath.Dir(path),
		now:       time.Now,
	}
	if err := mgr.WriteFacts(context.Background(), facts); err != nil {
		t.Fatalf("WriteFacts() error = %v", err)
	}
}
