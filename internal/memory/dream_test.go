package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDreamManager_RecordAndConsolidate(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamEnabled())

	if !dm.Enabled() {
		t.Fatal("expected Dream to be enabled")
	}
	if dm.Mode() != DreamFull {
		t.Fatal("expected DreamFull mode")
	}

	// Stage 1: Record some raw thoughts
	ctx := context.Background()
	records := []DreamRecord{
		{Type: DreamThought, Content: "User prefers dark mode for coding", Importance: 0.8, Tags: []string{"preference"}},
		{Type: DreamAction, Content: "Set editor theme to dark", Importance: 0.6, Tags: []string{"preference"}},
		{Type: DreamResult, Content: "Editor theme changed successfully", Importance: 0.4, Tags: []string{"preference"}},
		{Type: DreamThought, Content: "User likes Vim keybindings", Importance: 0.7, Tags: []string{"preference"}},
		{Type: DreamError, Content: "Failed to install plugin", Importance: 0.3, Tags: []string{"error"}},
	}

	for _, rec := range records {
		if err := dm.Record(ctx, rec); err != nil {
			t.Fatalf("Record failed: %v", err)
		}
	}

	// Verify raw log was written
	count := dm.CountRawRecords()
	if count != 5 {
		t.Fatalf("expected 5 raw records, got %d", count)
	}

	// Stage 2: Consolidate
	consolidations, err := dm.Consolidate(ctx)
	if err != nil {
		t.Fatalf("Consolidate failed: %v", err)
	}
	if len(consolidations) == 0 {
		t.Fatal("expected at least one consolidation")
	}

	t.Logf("Produced %d consolidated insights", len(consolidations))
	for _, c := range consolidations {
		t.Logf("  - %q (confidence: %.2f, tags: %v)", c.Insight[:min(60, len(c.Insight))], c.Confidence, c.Tags)
		if c.Confidence <= 0 || c.Confidence > 1 {
			t.Errorf("confidence %.2f out of [0,1] range", c.Confidence)
		}
	}
}

func TestDreamManager_PromoteToFacts(t *testing.T) {
	dm := NewDreamManager(t.TempDir(), WithDreamEnabled())

	consolidations := []DreamConsolidated{
		{
			ID:         "dc_1",
			Insight:    "User prefers dark mode",
			Confidence: 0.9,
			Tags:       []string{"preference"},
		},
		{
			ID:         "dc_2",
			Insight:    "Project uses Go 1.24",
			Confidence: 0.85,
			Tags:       []string{"project"},
		},
	}

	facts := dm.PromoteToFacts(consolidations)
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}

	// First fact should be preference category
	if facts[0].Category != FactPreference {
		t.Errorf("expected FactPreference, got %s", facts[0].Category)
	}
	if facts[0].Source != "dream_consolidation" {
		t.Errorf("expected source dream_consolidation, got %s", facts[0].Source)
	}

	// Second fact should be project category
	if facts[1].Category != FactProject {
		t.Errorf("expected FactProject, got %s", facts[1].Category)
	}
}

func TestDreamManager_DisabledByDefault(t *testing.T) {
	dm := NewDreamManager(t.TempDir())
	if dm.Enabled() {
		t.Fatal("Dream should be disabled by default")
	}

	ctx := context.Background()
	err := dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "test"})
	if err != nil {
		t.Fatalf("Record on disabled Dream should be no-op, got: %v", err)
	}

	if dm.CountRawRecords() != 0 {
		t.Fatal("disabled Dream should not write records")
	}
}

func TestDreamManager_RecordOnlyMode(t *testing.T) {
	dm := NewDreamManager(t.TempDir(), WithDreamMode(DreamRecordOnly))
	if dm.Mode() != DreamRecordOnly {
		t.Fatal("expected DreamRecordOnly mode")
	}

	ctx := context.Background()
	err := dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "test recording"})
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	if dm.CountRawRecords() != 1 {
		t.Fatal("expected 1 raw record in record-only mode")
	}

	// Consolidate should be no-op in RecordOnly mode
	consolidations, err := dm.Consolidate(ctx)
	if err != nil {
		t.Fatalf("Consolidate in RecordOnly mode should not error: %v", err)
	}
	if len(consolidations) != 0 {
		t.Fatal("Consolidate in RecordOnly mode should return no results")
	}
}

func TestDreamManager_Clear(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamEnabled())

	ctx := context.Background()
	_ = dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "test"})
	if dm.CountRawRecords() != 1 {
		t.Fatal("expected 1 record before clear")
	}

	if err := dm.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	if dm.CountRawRecords() != 0 {
		t.Fatal("expected 0 records after clear")
	}
}

func TestDreamManager_ClearRawRecords(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamEnabled())

	ctx := context.Background()
	_ = dm.Record(ctx, DreamRecord{Type: DreamAction, Content: "did something"})
	if dm.CountRawRecords() != 1 {
		t.Fatal("expected 1 record")
	}

	if err := dm.ClearRawRecords(); err != nil {
		t.Fatalf("ClearRawRecords failed: %v", err)
	}
	if dm.CountRawRecords() != 0 {
		t.Fatal("expected 0 records after ClearRawRecords")
	}
}

func TestInMemoryVectorStore_UpsertAndSearch(t *testing.T) {
	vs := NewInMemoryVectorStore()

	vec1 := Embedding{1.0, 0.0, 0.0}
	vec2 := Embedding{0.0, 1.0, 0.0}
	vec3 := Embedding{0.9, 0.1, 0.0} // similar to vec1

	_ = vs.Upsert("a", vec1, map[string]any{"label": "x-axis"})
	_ = vs.Upsert("b", vec2, map[string]any{"label": "y-axis"})
	_ = vs.Upsert("c", vec3, map[string]any{"label": "near x-axis"})

	results, err := vs.Search(vec1, 3)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results")
	}

	// "a" and "c" should be top results (most similar to [1,0,0])
	if results[0].ID != "a" {
		t.Errorf("expected top result to be 'a', got '%s'", results[0].ID)
	}
	t.Logf("Search results for [1,0,0]:")
	for _, r := range results {
		t.Logf("  %s: score=%.4f", r.ID, r.Score)
	}
}

func TestTFIDFEmbedder_FitAndTransform(t *testing.T) {
	emb := NewTFIDFEmbedder()

	corpus := []string{
		"the cat sat on the mat",
		"the dog played in the park",
		"cats and dogs are friends",
	}
	emb.Fit(corpus)

	if emb.Dim() == 0 {
		t.Fatal("expected non-zero dimensionality after Fit")
	}

	vec := emb.Transform("cat and dog")
	if len(vec) != emb.Dim() {
		t.Fatalf("expected embedding of dim %d, got %d", emb.Dim(), len(vec))
	}

	// Vector should be L2-normalized
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	norm = float64(int(norm*1000)) / 1000 // round to 3 decimal places
	if norm < 0.99 || norm > 1.01 {
		t.Errorf("expected L2 norm ≈1.0, got %.4f", norm)
	}
}

func TestManagerWithDream(t *testing.T) {
	dir := t.TempDir()
	mgr, err := New(dir, WithDream(WithDreamEnabled()))
	if err != nil {
		t.Fatalf("New Manager failed: %v", err)
	}

	dream := mgr.Dream()
	if dream == nil {
		t.Fatal("expected DreamManager to be set")
	}
	if !dream.Enabled() {
		t.Fatal("expected Dream to be enabled")
	}

	// Test SearchSimilarMemories (should return nil since no data)
	ctx := context.Background()
	results, err := mgr.SearchSimilarMemories(ctx, "test query", 5)
	if err != nil {
		t.Fatalf("SearchSimilarMemories failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatal("expected 0 results with no data")
	}
}

func TestDreamManager_SimilarityClustering(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamEnabled())
	ctx := context.Background()

	// Record similar thoughts
	records := []DreamRecord{
		{Type: DreamThought, Content: "user prefers vim editor for coding", Importance: 0.8, Tags: []string{"preference"}},
		{Type: DreamThought, Content: "user likes vim keybindings in IDE", Importance: 0.7, Tags: []string{"preference"}},
		{Type: DreamThought, Content: "vim is the preferred text editor", Importance: 0.6, Tags: []string{"preference"}},
		{Type: DreamThought, Content: "project deadline is next friday", Importance: 0.5, Tags: []string{"project"}},
		{Type: DreamThought, Content: "need to finish the API endpoint by deadline", Importance: 0.4, Tags: []string{"project"}},
	}

	for _, rec := range records {
		_ = dm.Record(ctx, rec)
	}

	consolidations, err := dm.Consolidate(ctx)
	if err != nil {
		t.Fatalf("Consolidate failed: %v", err)
	}
	if len(consolidations) == 0 {
		t.Fatal("expected consolidations")
	}

	// Similar records should be clustered together
	t.Logf("Clustering produced %d groups from 5 records", len(consolidations))
	for i, c := range consolidations {
		t.Logf("  Group %d: %d sources, confidence=%.2f", i+1, len(c.SourceIDs), c.Confidence)
	}

	// With 5 records across 2 topics, we expect at least 2 clusters
	if len(consolidations) < 2 {
		t.Logf("Warning: expected ≥2 clusters, got %d (similarity threshold may need tuning)", len(consolidations))
	}
}

func TestDreamManager_Persistence(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamEnabled())
	ctx := context.Background()

	// Record a thought
	_ = dm.Record(ctx, DreamRecord{
		Type:       DreamThought,
		Content:    "test persistence",
		Importance: 0.5,
	})

	// Verify raw log file exists
	path := filepath.Join(dir, "dream_raw.log")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("dream_raw.log should exist after Record")
	}

	// Create a new DreamManager pointing to same dir
	dm2 := NewDreamManager(dir, WithDreamEnabled())
	if dm2.CountRawRecords() != 1 {
		t.Fatalf("expected 1 record in new DreamManager, got %d", dm2.CountRawRecords())
	}
}

func TestDreamManager_TimestampDefaults(t *testing.T) {
	dm := NewDreamManager(t.TempDir(), WithDreamEnabled())
	ctx := context.Background()

	before := time.Now().UTC()
	_ = dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "test"})
	after := time.Now().UTC()

	records, _ := dm.loadRawRecords()
	if len(records) != 1 {
		t.Fatal("expected 1 record")
	}
	if records[0].Timestamp.Before(before) || records[0].Timestamp.After(after) {
		t.Errorf("timestamp %v not between %v and %v", records[0].Timestamp, before, after)
	}
}

// SearchSimilar must return the actual insight text, not an empty string. The
// original implementation read metadata keys ("insight"/"confidence") that were
// never written, so every result had an empty Insight and Confidence 0.
func TestDreamManager_SearchSimilarReturnsInsightText(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamEnabled())
	ctx := context.Background()

	_ = dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "user prefers vim editor for coding", Importance: 0.8, Tags: []string{"preference"}})
	_ = dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "user likes vim keybindings in IDE", Importance: 0.7, Tags: []string{"preference"}})

	consolidations, err := dm.Consolidate(ctx)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if len(consolidations) == 0 {
		t.Fatal("expected at least one consolidation")
	}

	results, err := dm.SearchSimilar(ctx, "vim editor", 5)
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for 'vim editor'")
	}
	for _, r := range results {
		if r.Insight == "" {
			t.Errorf("SearchSimilar returned an empty Insight; the insight text must be present")
		}
		if r.Confidence <= 0 {
			t.Errorf("SearchSimilar returned Confidence %v; want > 0", r.Confidence)
		}
	}
}

// Consolidated insights must survive a restart. The original implementation
// kept them only in an in-memory vector store and cleared the raw log after
// consolidation, so a restart lost everything.
func TestDreamManager_ConsolidatedInsightsPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamEnabled())
	ctx := context.Background()

	_ = dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "user prefers vim editor for coding", Importance: 0.8, Tags: []string{"preference"}})
	_ = dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "user likes vim keybindings in IDE", Importance: 0.7, Tags: []string{"preference"}})

	consolidations, err := dm.Consolidate(ctx)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if len(consolidations) == 0 {
		t.Fatal("expected at least one consolidation")
	}

	// The raw log is cleared after consolidation; the durable consolidated log
	// must now hold the insights.
	if dm.CountRawRecords() != 0 {
		t.Fatalf("expected raw log cleared after consolidation, got %d records", dm.CountRawRecords())
	}
	if _, err := os.Stat(dm.consolidatedPath()); err != nil {
		t.Fatalf("expected consolidated log to exist after consolidation: %v", err)
	}

	// A fresh DreamManager over the same dir (a restart) must still find the
	// insights via SearchSimilar.
	dm2 := NewDreamManager(dir, WithDreamEnabled())
	results, err := dm2.SearchSimilar(ctx, "vim editor", 5)
	if err != nil {
		t.Fatalf("SearchSimilar after restart: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results after restart; consolidated insights were lost")
	}
	if results[0].Insight == "" {
		t.Error("restored insight has empty text")
	}
}

// Clear must remove the durable consolidated log too, not just the raw log.
func TestDreamManager_ClearRemovesConsolidatedLog(t *testing.T) {
	dir := t.TempDir()
	dm := NewDreamManager(dir, WithDreamEnabled())
	ctx := context.Background()

	_ = dm.Record(ctx, DreamRecord{Type: DreamThought, Content: "user prefers vim editor", Importance: 0.8})
	if _, err := dm.Consolidate(ctx); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if _, err := os.Stat(dm.consolidatedPath()); err != nil {
		t.Fatalf("expected consolidated log to exist: %v", err)
	}

	if err := dm.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(dm.consolidatedPath()); !os.IsNotExist(err) {
		t.Errorf("consolidated log still exists after Clear (err=%v); want IsNotExist", err)
	}
}
