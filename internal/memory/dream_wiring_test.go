package memory

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Dream subsystem shipped fully implemented and wired to nothing (#193).
// These tests cover the seams that make it reachable: the config string that
// turns it on, the recording hook on the history append, the age discount that
// keeps a year-old insight from outranking a fresh fact, and the listing the
// CLI reports from.

func TestParseDreamModeMapsEveryConfigValueAndRejectsTypos(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want DreamMode
	}{
		{"", DreamOff},
		{"off", DreamOff},
		{"record", DreamRecordOnly},
		{"full", DreamFull},
	} {
		got, err := ParseDreamMode(tc.in)
		if err != nil {
			t.Fatalf("ParseDreamMode(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseDreamMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	// A typo must be an error, not a silent fall back to off: an operator who
	// wrote "record-only" would otherwise get no recording and no explanation.
	if _, err := ParseDreamMode("record-only"); err == nil {
		t.Fatal("ParseDreamMode(\"record-only\") returned no error")
	}
}

func TestDecayedConfidenceHalvesAtTheHalfLifeAndSparesUndatedInsights(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	fresh := DreamConsolidated{Confidence: 0.8, CreatedAt: base}
	if got := fresh.DecayedConfidence(base); math.Abs(got-0.8) > 1e-9 {
		t.Errorf("a brand-new insight was discounted: got %v, want 0.8", got)
	}

	aged := fresh.DecayedConfidence(base.Add(DreamConfidenceHalfLife))
	if math.Abs(aged-0.4) > 1e-6 {
		t.Errorf("confidence at one half-life = %v, want 0.4", aged)
	}

	older := fresh.DecayedConfidence(base.Add(4 * DreamConfidenceHalfLife))
	if older >= aged {
		t.Errorf("confidence did not keep falling: %v at 4 half-lives vs %v at 1", older, aged)
	}
	if older <= 0 {
		t.Errorf("an old insight decayed to %v; it is weak evidence, not counter-evidence", older)
	}

	// A record written before CreatedAt was populated must read as undated,
	// not as infinitely old — that would zero out every legacy insight.
	undated := DreamConsolidated{Confidence: 0.8}
	if got := undated.DecayedConfidence(base); math.Abs(got-0.8) > 1e-9 {
		t.Errorf("a zero CreatedAt was treated as ancient: got %v, want 0.8", got)
	}
}

func TestPromoteToFactsCarriesTheDecayedConfidenceNotTheRawOne(t *testing.T) {
	dm := NewDreamManager(t.TempDir(), WithDreamMode(DreamFull))
	old := DreamConsolidated{
		Insight:    "user deploys on fridays",
		Confidence: 0.9,
		CreatedAt:  time.Now().Add(-3 * DreamConfidenceHalfLife),
	}
	facts := dm.PromoteToFacts([]DreamConsolidated{old})
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(facts))
	}
	if facts[0].Confidence >= 0.9 {
		t.Errorf("a three-half-life-old insight was promoted at %v; the raw confidence reached the fact",
			facts[0].Confidence)
	}
	if facts[0].Confidence <= 0 {
		t.Errorf("promotion zeroed the confidence: %v", facts[0].Confidence)
	}
}

// Stage 1 rides on AppendHistory. If that hook is dropped, nothing ever reaches
// the raw log and consolidation silently has nothing to do — the whole feature
// becomes a no-op with no error anywhere.
func TestAppendHistoryRecordsToTheRawLogOnlyWhenDreamIsOn(t *testing.T) {
	ctx := context.Background()

	on, err := New(t.TempDir(), WithDream(WithDreamMode(DreamFull)))
	if err != nil {
		t.Fatal(err)
	}
	if err := on.AppendHistory(ctx, "user asked about deploys"); err != nil {
		t.Fatal(err)
	}
	if n := on.Dream().CountRawRecords(); n != 1 {
		t.Fatalf("Dream on: raw records = %d, want 1", n)
	}

	off, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := off.AppendHistory(ctx, "user asked about deploys"); err != nil {
		t.Fatal(err)
	}
	if off.Dream() != nil {
		t.Fatal("Dream is on with no WithDream option")
	}
	// And the history file itself must still be written in both cases.
	data, err := os.ReadFile(off.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "user asked about deploys") {
		t.Errorf("history append lost the entry:\n%s", data)
	}
}

func TestConsolidateProducesInsightsSearchableAfterAProcessRestart(t *testing.T) {
	ws := t.TempDir()
	ctx := context.Background()

	mgr, err := New(ws, WithDream(WithDreamMode(DreamFull)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{
		"deploy pipeline runs on github actions",
		"github actions runs the deploy pipeline nightly",
		"user prefers dark mode in the terminal",
	} {
		if err := mgr.AppendHistory(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	insights, err := mgr.Dream().Consolidate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) == 0 {
		t.Fatal("consolidation produced no insights")
	}
	if n := mgr.Dream().CountRawRecords(); n != 0 {
		t.Errorf("raw log still holds %d record(s) after consolidation", n)
	}

	// A second Manager over the same workspace is what a restarted joshbot
	// sees: the insights have to come off disk, not out of the vector store
	// the first process built.
	restarted, err := New(ws, WithDream(WithDreamMode(DreamFull)))
	if err != nil {
		t.Fatal(err)
	}
	found, err := restarted.SearchSimilarMemories(ctx, "deploy pipeline", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("a restarted manager found no consolidated insights")
	}
	if !strings.Contains(strings.ToLower(found[0].Insight), "deploy") {
		t.Errorf("top hit for %q was %q", "deploy pipeline", found[0].Insight)
	}

	// With Dream off, the same call must return nothing rather than error —
	// that is what makes the memory_search rows additive.
	plain, err := New(ws)
	if err != nil {
		t.Fatal(err)
	}
	got, err := plain.SearchSimilarMemories(ctx, "deploy pipeline", 3)
	if err != nil || len(got) != 0 {
		t.Errorf("Dream off returned %d insight(s), err=%v", len(got), err)
	}
}

func TestListConsolidatedReturnsEveryInsightNewestFirst(t *testing.T) {
	ws := t.TempDir()
	dm := NewDreamManager(filepath.Join(ws), WithDreamMode(DreamFull))

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	want := []DreamConsolidated{
		{ID: "a", Insight: "oldest", Confidence: 0.5, CreatedAt: base},
		{ID: "b", Insight: "middle", Confidence: 0.5, CreatedAt: base.Add(time.Hour)},
		{ID: "c", Insight: "newest", Confidence: 0.5, CreatedAt: base.Add(2 * time.Hour)},
	}
	var buf strings.Builder
	for _, c := range want {
		line, _ := json.Marshal(c)
		buf.Write(line)
		buf.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(ws, "dream_consolidated.jsonl"), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := dm.ListConsolidated()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("ListConsolidated returned %d insight(s), want 3 — a limit crept in", len(got))
	}
	if got[0].Insight != "newest" || got[2].Insight != "oldest" {
		t.Errorf("order is %q..%q, want newest..oldest", got[0].Insight, got[2].Insight)
	}
}

// Consolidate persists before it clears, and a failed persist must surface.
// Reporting success there prints insights that are not on disk, leaves the
// next `memory status` showing none, and gives the operator nothing to
// correlate. The raw log staying intact is the other half: it is what makes
// the next run a retry rather than a loss.
func TestConsolidateReportsAFailedPersistAndKeepsTheRawLog(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamMode(DreamFull))

	for _, c := range []string{
		"the deploy pipeline runs on github actions",
		"deploys go out through github actions pipelines",
	} {
		if err := dm.Record(context.Background(), DreamRecord{Type: DreamThought, Content: c}); err != nil {
			t.Fatal(err)
		}
	}

	// Make the consolidated log unwritable the way a permissions change or a
	// full disk would.
	consolidated := filepath.Join(dir, "dream_consolidated.jsonl")
	if err := os.WriteFile(consolidated, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(consolidated, 0o600) })

	if _, err := dm.Consolidate(context.Background()); err == nil {
		t.Fatal("Consolidate reported success over a failed persist")
	}
	if n := dm.CountRawRecords(); n == 0 {
		t.Fatal("raw log was cleared even though the insights never reached disk")
	}
}
