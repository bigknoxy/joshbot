package memory

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// seedMemory writes content straight to MEMORY.md, the way a user's own
// edits and the shipped template land there.
func seedMemory(t *testing.T, mgr *Manager, content string) {
	t.Helper()
	if err := os.WriteFile(mgr.MemoryPath(), []byte(content), 0o600); err != nil {
		t.Fatalf("seed MEMORY.md: %v", err)
	}
}

func readMemory(t *testing.T, mgr *Manager) string {
	t.Helper()
	data, err := os.ReadFile(mgr.MemoryPath())
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	return string(data)
}

func fact(category FactCategory, content string, confidence float64) Fact {
	return Fact{
		ID:         FactID(category, content),
		Category:   category,
		Content:    content,
		Confidence: confidence,
		CreatedAt:  time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
	}
}

// TestReconcileFactsPreservesUserProse is the regression this whole change
// exists for: the consolidator used to rebuild MEMORY.md from the fact set
// alone, so the first successful consolidation erased every line the user had
// written.
func TestReconcileFactsPreservesUserProse(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const prose = "Always check the deploy checklist before shipping on Fridays."
	const comment = "<!-- critical reminders the agent must never forget -->"
	seedMemory(t, mgr, "# Long-Term Memory\n\n## Important Notes\n"+comment+"\n"+prose+"\n")

	if err := mgr.ReconcileFacts(context.Background(), []Fact{
		fact(FactUserInfo, "User prefers dark mode", 0.9),
	}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	got := readMemory(t, mgr)
	if !strings.Contains(got, prose) {
		t.Errorf("the user's prose was destroyed by consolidation; MEMORY.md is now:\n%s", got)
	}
	if !strings.Contains(got, comment) {
		t.Errorf("the HTML comment was destroyed by consolidation; MEMORY.md is now:\n%s", got)
	}
	if !strings.Contains(got, "User prefers dark mode") {
		t.Errorf("the new fact was not written; MEMORY.md is now:\n%s", got)
	}
}

// TestReconcileFactsPreservesTheShippedTemplate pins the specific line the
// default template tells the user to write their critical reminders under.
func TestReconcileFactsPreservesTheShippedTemplate(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	seedMemory(t, mgr, defaultMemoryTemplate)

	if err := mgr.ReconcileFacts(context.Background(), []Fact{
		fact(FactUserInfo, "User prefers dark mode", 0.9),
	}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	got := readMemory(t, mgr)
	for _, want := range []string{
		"## User Information",
		"<!-- facts about the user will accumulate here -->",
		"## Preferences",
		"<!-- preferences, likes, dislikes -->",
		"## Projects & Context",
		"<!-- project details and decisions -->",
		"## Important Notes",
		"<!-- critical reminders the agent must never forget -->",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the shipped template lost %q; MEMORY.md is now:\n%s", want, got)
		}
	}
}

// TestReconcileFactsPreservesUnknownHeadingsAndBullets covers content that
// is neither a fact nor a heading joshbot knows: a hand-written bullet, a
// heading it has never seen, and a paragraph.
func TestReconcileFactsPreservesUnknownHeadingsAndBullets(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const body = `# Long-Term Memory

## My Own Section
- Remember to call the dentist
Some prose that is not a fact at all.

## Another Section
1. a numbered item
2. another numbered item
`
	seedMemory(t, mgr, body)

	if err := mgr.ReconcileFacts(context.Background(), []Fact{
		fact(FactPreference, "User likes coffee", 0.8),
	}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	got := readMemory(t, mgr)
	for _, want := range []string{
		"## My Own Section",
		"- Remember to call the dentist",
		"Some prose that is not a fact at all.",
		"## Another Section",
		"1. a numbered item",
		"2. another numbered item",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("consolidation lost %q; MEMORY.md is now:\n%s", want, got)
		}
	}
}

// TestReconcileFactsUpdatesFactInPlace checks that a fact already in the
// document is updated where it stands rather than appended again.
func TestReconcileFactsUpdatesFactInPlace(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	seedMemory(t, mgr, "# Long-Term Memory\n\n## user_info\n- [2026-05-17] User prefers dark mode (confidence: 0.5)\n")

	if err := mgr.ReconcileFacts(context.Background(), []Fact{
		fact(FactUserInfo, "User prefers dark mode", 0.9),
	}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	got := readMemory(t, mgr)
	if !strings.Contains(got, "User prefers dark mode (confidence: 0.9)") {
		t.Errorf("the fact was not updated in place; MEMORY.md is now:\n%s", got)
	}
	if strings.Count(got, "User prefers dark mode") != 1 {
		t.Errorf("the fact was duplicated; MEMORY.md is now:\n%s", got)
	}
}

// TestReconcileFactsEvictsOnlyFacts checks that the 100-fact cap still
// applies — and that evicting a fact never takes the user's writing with it.
func TestReconcileFactsEvictsOnlyFacts(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const prose = "- a hand-written bullet that is not a fact"
	seedMemory(t, mgr, "# Long-Term Memory\n\n## user_info\n"+prose+"\n")

	facts := make([]Fact, 0, 120)
	for i := 0; i < 120; i++ {
		content := "fact " + strings.Repeat("x", i+1)
		facts = append(facts, Fact{
			ID:         FactID(FactUserInfo, content),
			Category:   FactUserInfo,
			Content:    content,
			Confidence: 0.5,
			CreatedAt:  time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
			UpdatedAt:  time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
		})
	}
	if err := mgr.ReconcileFacts(context.Background(), facts); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	got := readMemory(t, mgr)
	if !strings.Contains(got, prose) {
		t.Errorf("eviction took the user's own bullet with it; MEMORY.md is now:\n%s", got)
	}

	count := 0
	for _, line := range splitLines(got) {
		if strings.HasPrefix(line, "- [") {
			count++
		}
	}
	if count != 100 {
		t.Errorf("expected the fact cap to leave 100 facts, got %d", count)
	}
}

// TestReconcileFactsDoesNotDuplicateAFactWrittenByHand covers the shape
// parseFactLine accepts but FormatFactMarkdown never writes: the fact is
// already in the document, so merging must not add a second copy of it.
func TestReconcileFactsDoesNotDuplicateAFactWrittenByHand(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	seedMemory(t, mgr, "# Long-Term Memory\n\n## user_info\n- [2026-05-17] User prefers dark mode\n")

	if err := mgr.ReconcileFacts(context.Background(), []Fact{
		fact(FactUserInfo, "User prefers dark mode", 0.9),
	}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	got := readMemory(t, mgr)
	if n := strings.Count(got, "User prefers dark mode"); n != 1 {
		t.Errorf("expected the fact to appear once, got %d; MEMORY.md is now:\n%s", n, got)
	}
}

// TestReconcileFactsCreatesTheCategorySection checks that a fact whose
// category has no section yet gets one, without disturbing the rest.
func TestReconcileFactsCreatesTheCategorySection(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	seedMemory(t, mgr, "# Long-Term Memory\n\n## Important Notes\nkeep me\n")

	if err := mgr.ReconcileFacts(context.Background(), []Fact{
		fact(FactDecision, "Ship on Thursday", 0.7),
	}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	got := readMemory(t, mgr)
	if !strings.Contains(got, "## decision") {
		t.Errorf("the category section was not created; MEMORY.md is now:\n%s", got)
	}
	if !strings.Contains(got, "Ship on Thursday") {
		t.Errorf("the fact was not written; MEMORY.md is now:\n%s", got)
	}
	if !strings.Contains(got, "keep me") {
		t.Errorf("existing content was lost; MEMORY.md is now:\n%s", got)
	}
}

// TestReconcileFactsOnAMissingFile keeps the old behaviour for a workspace
// that has no MEMORY.md yet.
func TestReconcileFactsOnAMissingFile(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := mgr.ReconcileFacts(context.Background(), []Fact{
		fact(FactUserInfo, "User prefers dark mode", 0.9),
	}); err != nil {
		t.Fatalf("ReconcileFacts() error = %v", err)
	}

	got := readMemory(t, mgr)
	if !strings.HasPrefix(got, "# Long-Term Memory") {
		t.Errorf("expected a fresh document, got:\n%s", got)
	}
	if !strings.Contains(got, "User prefers dark mode") {
		t.Errorf("the fact was not written; MEMORY.md is now:\n%s", got)
	}
}

// TestMergeFactsDocumentIsIdempotent checks that reconciling the same facts
// twice leaves the document alone the second time.
func TestMergeFactsDocumentIsIdempotent(t *testing.T) {
	t.Parallel()
	mgr, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	seedMemory(t, mgr, defaultMemoryTemplate)
	facts := []Fact{
		fact(FactUserInfo, "User prefers dark mode", 0.9),
		fact(FactPreference, "User likes coffee", 0.8),
	}

	if err := mgr.ReconcileFacts(context.Background(), facts); err != nil {
		t.Fatalf("first ReconcileFacts() error = %v", err)
	}
	first := readMemory(t, mgr)

	if err := mgr.ReconcileFacts(context.Background(), facts); err != nil {
		t.Fatalf("second ReconcileFacts() error = %v", err)
	}
	if second := readMemory(t, mgr); second != first {
		t.Errorf("reconciling the same facts twice changed the document:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}
