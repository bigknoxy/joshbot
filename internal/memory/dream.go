package memory

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Vector Store
// ---------------------------------------------------------------------------

// Embedding is a vector representation of text.
type Embedding []float64

// VectorStore is a minimal persistent vector store for similarity search.
// It uses TF-IDF vectors with cosine similarity — no external dependencies
// required. Implementations must be safe for concurrent use.
type VectorStore interface {
	Upsert(id string, vec Embedding, metadata map[string]any) error
	Search(query Embedding, k int) ([]VectorSearchResult, error)
	Delete(id string) error
	Close() error
}

// VectorSearchResult is a single hit from a vector similarity search.
type VectorSearchResult struct {
	ID       string
	Score    float64
	Metadata map[string]any
}

// InMemoryVectorStore is a simple in-memory vector store using cosine similarity.
type InMemoryVectorStore struct {
	mu       sync.RWMutex
	vectors  map[string]Embedding
	metadata map[string]map[string]any
}

// NewInMemoryVectorStore creates a new in-memory vector store.
func NewInMemoryVectorStore() *InMemoryVectorStore {
	return &InMemoryVectorStore{
		vectors:  make(map[string]Embedding),
		metadata: make(map[string]map[string]any),
	}
}

func (s *InMemoryVectorStore) Upsert(id string, vec Embedding, meta map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vectors[id] = vec
	if meta != nil {
		s.metadata[id] = meta
	} else {
		s.metadata[id] = map[string]any{}
	}
	return nil
}

func (s *InMemoryVectorStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vectors, id)
	delete(s.metadata, id)
	return nil
}

func (s *InMemoryVectorStore) Search(query Embedding, k int) ([]VectorSearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.vectors) == 0 || k <= 0 {
		return nil, nil
	}

	type scored struct {
		id    string
		score float64
		meta  map[string]any
	}

	results := make([]scored, 0, len(s.vectors))
	normQ := cosineNorm(query)

	for id, vec := range s.vectors {
		if len(vec) != len(query) {
			continue
		}
		score := cosineSimilarity(query, vec, normQ)
		results = append(results, scored{id, score, s.metadata[id]})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if k > len(results) {
		k = len(results)
	}

	out := make([]VectorSearchResult, k)
	for i := 0; i < k; i++ {
		out[i] = VectorSearchResult{
			ID:       results[i].id,
			Score:    results[i].score,
			Metadata: results[i].meta,
		}
	}
	return out, nil
}

func (s *InMemoryVectorStore) Close() error { return nil }

func cosineNorm(v Embedding) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
}

func cosineSimilarity(a, b Embedding, normA float64) float64 {
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	normB := cosineNorm(b)
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (normA * normB)
}

// ---------------------------------------------------------------------------
// TF-IDF Embedder
// ---------------------------------------------------------------------------

var wordRe = regexp.MustCompile(`[a-z0-9]+`)

// TFIDFEmbedder converts text into fixed-size TF-IDF embedding vectors.
type TFIDFEmbedder struct {
	mu       sync.RWMutex
	df       map[string]int
	idf      map[string]float64
	vocab    map[string]int
	docCount int
	dim      int
}

// NewTFIDFEmbedder creates a new TF-IDF embedder with no vocabulary.
func NewTFIDFEmbedder() *TFIDFEmbedder {
	return &TFIDFEmbedder{
		df:    make(map[string]int),
		vocab: make(map[string]int),
	}
}

// Fit builds the vocabulary and IDF weights from the given corpus.
func (e *TFIDFEmbedder) Fit(corpus []string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.docCount = len(corpus)
	e.df = make(map[string]int)

	for _, doc := range corpus {
		seen := make(map[string]bool)
		for _, tok := range tokenize(doc) {
			seen[tok] = true
		}
		for tok := range seen {
			e.df[tok]++
		}
	}

	terms := make([]string, 0, len(e.df))
	for term := range e.df {
		terms = append(terms, term)
	}
	sort.Strings(terms)

	e.vocab = make(map[string]int, len(terms))
	e.idf = make(map[string]float64, len(terms))
	for i, term := range terms {
		e.vocab[term] = i
		e.idf[term] = math.Log((1+float64(e.docCount))/(1+float64(e.df[term]))) + 1
	}
	e.dim = len(terms)
}

// Dim returns the embedding dimensionality.
func (e *TFIDFEmbedder) Dim() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dim
}

// Transform converts a single document into an embedding vector.
func (e *TFIDFEmbedder) Transform(text string) Embedding {
	e.mu.RLock()
	defer e.mu.RUnlock()

	vec := make(Embedding, e.dim)
	if e.dim == 0 {
		return vec
	}

	tf := make(map[string]int)
	for _, tok := range tokenize(text) {
		tf[tok]++
	}

	var sumSq float64
	for term, freq := range tf {
		if idx, ok := e.vocab[term]; ok {
			weight := float64(freq) * e.idf[term]
			vec[idx] = weight
			sumSq += weight * weight
		}
	}

	norm := math.Sqrt(sumSq)
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

// FitTransform builds the vocabulary from the corpus and returns embeddings.
func (e *TFIDFEmbedder) FitTransform(corpus []string) []Embedding {
	e.Fit(corpus)
	embeddings := make([]Embedding, len(corpus))
	for i, doc := range corpus {
		embeddings[i] = e.Transform(doc)
	}
	return embeddings
}

func tokenize(text string) []string {
	return wordRe.FindAllString(strings.ToLower(text), -1)
}

// ---------------------------------------------------------------------------
// Dream Memory System — Two-Stage Consolidation
// ---------------------------------------------------------------------------

// DreamMode controls how the Dream consolidation system operates.
type DreamMode int

const (
	// DreamOff disables Dream consolidation (backward compatible default).
	DreamOff DreamMode = iota
	// DreamRecordOnly records raw thoughts/actions/results but doesn't run reflection.
	DreamRecordOnly
	// DreamFull enables both recording and reflection-based consolidation.
	DreamFull
)

// DreamOption configures the Dream memory system.
type DreamOption func(*dreamConfig)

// WithDreamEnabled enables the Dream two-stage consolidation system.
func WithDreamEnabled() DreamOption {
	return func(d *dreamConfig) {
		d.enabled = true
		d.mode = DreamFull
	}
}

// WithDreamMode sets the Dream operating mode.
func WithDreamMode(mode DreamMode) DreamOption {
	return func(d *dreamConfig) {
		d.enabled = true
		d.mode = mode
	}
}

// WithVectorStore injects a custom vector store for Dream consolidation.
func WithVectorStore(vs VectorStore) DreamOption {
	return func(d *dreamConfig) {
		if vs != nil {
			d.vectorStore = vs
		}
	}
}

type dreamConfig struct {
	enabled     bool
	mode        DreamMode
	vectorStore VectorStore
	embedder    *TFIDFEmbedder
	mu          sync.RWMutex
}

// DreamRecordType identifies what kind of event is recorded.
type DreamRecordType string

const (
	DreamThought DreamRecordType = "thought"
	DreamAction  DreamRecordType = "action"
	DreamResult  DreamRecordType = "result"
	DreamError   DreamRecordType = "error"
)

// DreamRecord represents a raw thought, action, or result (Stage 1).
type DreamRecord struct {
	ID         string          `json:"id"`
	Type       DreamRecordType `json:"type"`
	Content    string          `json:"content"`
	TraceID    string          `json:"trace_id,omitempty"`
	Timestamp  time.Time       `json:"ts"`
	TokenCount int             `json:"tokens,omitempty"`
	Importance float64         `json:"importance,omitempty"`
	Tags       []string        `json:"tags,omitempty"`
}

// DreamConsolidated is the output of Stage 2 (reflection).
type DreamConsolidated struct {
	ID          string    `json:"id"`
	Insight     string    `json:"insight"`
	SourceIDs   []string  `json:"source_ids"`
	Confidence  float64   `json:"confidence"`
	Tags        []string  `json:"tags,omitempty"`
	EmbeddingID string    `json:"embedding_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// DreamManager implements the two-stage Dream memory consolidation system.
//
// Stage 1 (Recording): Raw thoughts, actions, and results from each subagent
// or agent run are appended to dream_raw.log.
//
// Stage 2 (Reflection): Periodically (or on demand), the raw log is processed
// into consolidated insights using TF-IDF vector similarity. Related raw
// records are clustered and summarized into higher-level DreamConsolidated
// insights, which can then be promoted to structured Facts in MEMORY.md.
//
// The system is opt-in via WithDreamEnabled() and is fully backward compatible
// — when disabled, the Manager behaves exactly as before.
type DreamManager struct {
	config    *dreamConfig
	memoryDir string
	now       func() time.Time
	mu        sync.RWMutex
}

// NewDreamManager creates a Dream memory manager rooted in the given memory dir.
func NewDreamManager(memoryDir string, opts ...DreamOption) *DreamManager {
	dm := &DreamManager{
		config: &dreamConfig{
			enabled:     false,
			mode:        DreamRecordOnly,
			vectorStore: NewInMemoryVectorStore(),
			embedder:    NewTFIDFEmbedder(),
		},
		memoryDir: memoryDir,
		now:       time.Now,
	}

	for _, opt := range opts {
		opt(dm.config)
	}

	return dm
}

// Enabled returns whether the Dream system is active.
func (dm *DreamManager) Enabled() bool {
	dm.config.mu.RLock()
	defer dm.config.mu.RUnlock()
	return dm.config.enabled
}

// Mode returns the current Dream operating mode.
func (dm *DreamManager) Mode() DreamMode {
	dm.config.mu.RLock()
	defer dm.config.mu.RUnlock()
	return dm.config.mode
}

// Record logs a raw event (Stage 1) to the dream_raw.log file.
func (dm *DreamManager) Record(ctx context.Context, rec DreamRecord) error {
	dm.config.mu.RLock()
	enabled := dm.config.enabled
	mode := dm.config.mode
	dm.config.mu.RUnlock()

	if !enabled {
		return nil
	}
	if mode == DreamOff {
		return nil
	}

	// Fill in defaults
	if rec.ID == "" {
		h := sha256.Sum256([]byte(string(rec.Type) + ":" + rec.Content + ":" + rec.TraceID))
		rec.ID = fmt.Sprintf("%x", h[:8])
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = dm.now().UTC()
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode dream record: %w", err)
	}

	path := filepath.Join(dm.memoryDir, "dream_raw.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open dream log: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(string(data) + "\n"); err != nil {
		return fmt.Errorf("write dream log: %w", err)
	}
	return nil
}

// Consolidate runs Stage 2: processes raw records, embeds them into the vector
// store, clusters similar records, and produces consolidated insights.
func (dm *DreamManager) Consolidate(ctx context.Context) ([]DreamConsolidated, error) {
	dm.config.mu.RLock()
	enabled := dm.config.enabled
	mode := dm.config.mode
	dm.config.mu.RUnlock()

	if !enabled || mode != DreamFull {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	records, err := dm.loadRawRecords()
	if err != nil {
		return nil, fmt.Errorf("load raw records: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}

	// Build corpus for TF-IDF embedding
	corpus := make([]string, len(records))
	for i, rec := range records {
		corpus[i] = rec.Content
	}

	dm.config.mu.Lock()
	dm.config.embedder.Fit(corpus)
	emb := dm.config.embedder
	dm.config.mu.Unlock()

	dm.config.mu.RLock()
	vs := dm.config.vectorStore
	dm.config.mu.RUnlock()

	// Embed all records and upsert to vector store.
	// Cache embeddings so the clustering pass doesn't re-compute them.
	embeddings := make([]Embedding, len(records))
	recordByID := make(map[string]DreamRecord, len(records))
	for i, rec := range records {
		vec := emb.Transform(rec.Content)
		embeddings[i] = vec
		recordByID[rec.ID] = rec
		meta := map[string]any{
			"type":       string(rec.Type),
			"trace_id":   rec.TraceID,
			"importance": rec.Importance,
			"tags":       rec.Tags,
		}
		if err := vs.Upsert(rec.ID, vec, meta); err != nil {
			// Non-fatal: the vector store is best-effort. A failed upsert
			// means the record won't appear in similarity search, but
			// consolidation continues with whatever succeeded.
		}
	}

	// Simple clustering: for each record, find similar records (cosine > 0.7)
	seen := make(map[string]bool)
	consolidations := make([]DreamConsolidated, 0)

	for i, rec := range records {
		if seen[rec.ID] {
			continue
		}

		// Reuse cached embedding instead of recomputing.
		queryVec := embeddings[i]
		results, _ := vs.Search(queryVec, 10)

		cluster := []DreamRecord{rec}
		seen[rec.ID] = true
		for _, sr := range results {
			if sr.Score < 0.7 || seen[sr.ID] {
				continue
			}
			if r2, ok := recordByID[sr.ID]; ok {
				cluster = append(cluster, r2)
				seen[r2.ID] = true
			}
		}

		consolidated := dm.summarizeCluster(cluster)
		consolidations = append(consolidations, consolidated)
	}

	// Truncate the raw log after successful consolidation to prevent unbounded
	// growth. The consolidated insights are persisted to the durable log, which
	// replaces the raw data.
	if len(consolidations) > 0 {
		// Persist the consolidated insights first so a crash between the two
		// steps cannot lose the work: the raw log is only cleared once the
		// consolidated form is safely on disk.
		if err := dm.saveConsolidated(consolidations); err != nil {
			// The raw log is deliberately left intact so the next run can
			// retry — but the error is returned, not swallowed. Reporting
			// success here would print insights that are not on disk and
			// leave `memory status` showing none, with nothing to correlate.
			return consolidations, fmt.Errorf("persist consolidated insights: %w", err)
		}
		if err := dm.ClearRawRecords(); err != nil {
			// Non-fatal: consolidation succeeded, we just couldn't trim the log.
			// Next consolidation will re-process these records.
		}
	}

	return consolidations, nil
}

// summarizeCluster produces a DreamConsolidated insight from a cluster of records.
func (dm *DreamManager) summarizeCluster(cluster []DreamRecord) DreamConsolidated {
	var parts []string
	var sourceIDs []string
	var tags []string
	var maxImportance float64

	for _, rec := range cluster {
		parts = append(parts, rec.Content)
		sourceIDs = append(sourceIDs, rec.ID)
		tags = append(tags, rec.Tags...)
		if rec.Importance > maxImportance {
			maxImportance = rec.Importance
		}
	}

	// Confidence based on cluster size and importance
	confidence := 0.5 + (maxImportance * 0.3) + (math.Min(float64(len(cluster)), 5) * 0.04)
	if confidence > 1.0 {
		confidence = 1.0
	}

	insight := strings.Join(parts, " ")
	if len(cluster) == 1 {
		insight = cluster[0].Content
	}

	return DreamConsolidated{
		ID:         fmt.Sprintf("dc_%d", dm.now().UnixNano()),
		Insight:    insight,
		SourceIDs:  sourceIDs,
		Confidence: confidence,
		Tags:       deduplicateStrings(tags),
		CreatedAt:  dm.now().UTC(),
	}
}

// loadRawRecords reads all records from dream_raw.log.
func (dm *DreamManager) loadRawRecords() ([]DreamRecord, error) {
	path := filepath.Join(dm.memoryDir, "dream_raw.log")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var records []DreamRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec DreamRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // skip malformed
		}
		records = append(records, rec)
	}
	return records, nil
}

// consolidatedPath returns the path of the durable consolidated-insights log.
func (dm *DreamManager) consolidatedPath() string {
	return filepath.Join(dm.memoryDir, "dream_consolidated.jsonl")
}

// saveConsolidated appends consolidated insights to the durable log. Unlike
// the in-memory vector store, this survives a restart, so a consolidation is
// not lost when the process exits.
func (dm *DreamManager) saveConsolidated(consolidations []DreamConsolidated) error {
	if len(consolidations) == 0 {
		return nil
	}
	f, err := os.OpenFile(dm.consolidatedPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open consolidated log: %w", err)
	}
	defer f.Close()
	for _, c := range consolidations {
		data, err := json.Marshal(c)
		if err != nil {
			return fmt.Errorf("encode consolidated insight: %w", err)
		}
		if _, err := f.WriteString(string(data) + "\n"); err != nil {
			return fmt.Errorf("write consolidated log: %w", err)
		}
	}
	return nil
}

// loadConsolidated reads all consolidated insights from the durable log.
func (dm *DreamManager) loadConsolidated() ([]DreamConsolidated, error) {
	data, err := os.ReadFile(dm.consolidatedPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []DreamConsolidated
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c DreamConsolidated
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue // skip malformed
		}
		out = append(out, c)
	}
	return out, nil
}

// PromoteToFacts converts DreamConsolidated insights into structured Facts.
func (dm *DreamManager) PromoteToFacts(consolidations []DreamConsolidated) []Fact {
	facts := make([]Fact, 0, len(consolidations))
	now := time.Now().UTC()

	for _, c := range consolidations {
		cat := FactSystem
		for _, t := range c.Tags {
			switch t {
			case "user_info":
				cat = FactUserInfo
			case "preference":
				cat = FactPreference
			case "project":
				cat = FactProject
			case "decision":
				cat = FactDecision
			}
		}

		f := Fact{
			ID:         FactID(cat, c.Insight),
			Category:   cat,
			Content:    c.Insight,
			Tags:       c.Tags,
			Source:     "dream_consolidation",
			Confidence: c.DecayedConfidence(now),
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		facts = append(facts, f)
	}
	return facts
}

// CountRawRecords returns the number of raw records in the log.
func (dm *DreamManager) CountRawRecords() int {
	records, _ := dm.loadRawRecords()
	return len(records)
}

// ClearRawRecords empties the raw log after consolidation.
func (dm *DreamManager) ClearRawRecords() error {
	path := filepath.Join(dm.memoryDir, "dream_raw.log")
	return os.Remove(path)
}

// SearchSimilar finds records similar to the given query text.
func (dm *DreamManager) SearchSimilar(ctx context.Context, query string, k int) ([]DreamConsolidated, error) {
	dm.config.mu.RLock()
	enabled := dm.config.enabled
	dm.config.mu.RUnlock()

	if !enabled {
		return nil, nil
	}

	// A search-local embedder, never the shared dm.config.embedder: Fit
	// replaces the whole vocabulary, so a search running alongside Consolidate
	// would have its Transform calls straddle two different vocabularies.
	// Fit/Transform are each locked, so that races cleanly — it just scores
	// against the wrong basis, silently, whenever the dimensions happen to
	// match. Search reads; it must not mutate shared state.
	emb := NewTFIDFEmbedder()

	// Search over the durable consolidated insights, not the in-memory vector
	// store. The vector store is rebuilt per Consolidate call and holds raw
	// records with metadata that never carried the insight text; the durable
	// log is what survives a restart and is what /resume-style lookups need.
	consolidated, err := dm.loadConsolidated()
	if err != nil {
		return nil, err
	}
	if len(consolidated) == 0 {
		return nil, nil
	}

	// Build a TF-IDF model over the consolidated insights so the query and the
	// stored insights share a vocabulary.
	corpus := make([]string, len(consolidated))
	for i, c := range consolidated {
		corpus[i] = c.Insight
	}
	emb.Fit(corpus)
	queryVec := emb.Transform(query)

	type scored struct {
		c     DreamConsolidated
		score float64
	}
	results := make([]scored, 0, len(consolidated))
	normQ := cosineNorm(queryVec)
	for _, c := range consolidated {
		vec := emb.Transform(c.Insight)
		if len(vec) != len(queryVec) {
			continue
		}
		// Rank by similarity discounted for age, so a stale insight has to be
		// a markedly better match than a fresh one to outrank it.
		sim := cosineSimilarity(queryVec, vec, normQ)
		results = append(results, scored{c, sim * (0.5 + 0.5*c.DecayedConfidence(dm.now()))})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })

	if k > len(results) {
		k = len(results)
	}
	out := make([]DreamConsolidated, 0, k)
	for i := 0; i < k; i++ {
		out = append(out, results[i].c)
	}
	return out, nil
}

// ListConsolidated returns every stored insight, newest-first. SearchSimilar
// cannot stand in for this: it ranks by similarity to a query and returns at
// most k, so "show me everything" through it means inventing a query and a
// limit, and an empty query scores every insight identically.
func (dm *DreamManager) ListConsolidated() ([]DreamConsolidated, error) {
	out, err := dm.loadConsolidated()
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Clear clears all Dream data: raw log, consolidated insights, and vector store.
func (dm *DreamManager) Clear() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.config.mu.Lock()
	dm.config.vectorStore = NewInMemoryVectorStore()
	dm.config.embedder = NewTFIDFEmbedder()
	dm.config.mu.Unlock()

	for _, path := range []string{
		filepath.Join(dm.memoryDir, "dream_raw.log"),
		filepath.Join(dm.memoryDir, "dream_consolidated.jsonl"),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// GetEmbedder returns the configured embedder.
func (dm *DreamManager) GetEmbedder() *TFIDFEmbedder {
	dm.config.mu.RLock()
	defer dm.config.mu.RUnlock()
	return dm.config.embedder
}

// GetVectorStore returns the configured vector store.
func (dm *DreamManager) GetVectorStore() VectorStore {
	dm.config.mu.RLock()
	defer dm.config.mu.RUnlock()
	return dm.config.vectorStore
}

// ---------------------------------------------------------------------------
// Manager integration (Dream field on existing Manager)
// ---------------------------------------------------------------------------

// ParseDreamMode maps a config string onto a DreamMode. An empty string means
// off, so a config written before the key existed keeps the old behaviour.
// Anything else is an error rather than a silent fallback: an operator who
// typed "record-only" would otherwise get no recording and no explanation.
func ParseDreamMode(s string) (DreamMode, error) {
	switch s {
	case "", "off":
		return DreamOff, nil
	case "record":
		return DreamRecordOnly, nil
	case "full":
		return DreamFull, nil
	default:
		return DreamOff, fmt.Errorf("unknown dream mode %q (want off, record or full)", s)
	}
}

// DreamConfidenceHalfLife is how long a consolidated insight takes to lose half
// its confidence. An insight is a claim about the user drawn from one window of
// conversation; a year-old one that nothing has reinforced should not outrank a
// fresh contradicting fact, and the only evidence of continued truth is that
// consolidation keeps producing it.
const DreamConfidenceHalfLife = 30 * 24 * time.Hour

// DecayedConfidence is the insight's confidence discounted for age. It never
// reaches zero — an old insight is weak evidence, not counter-evidence — and a
// zero CreatedAt (a record written before this field was populated) is treated
// as undated rather than infinitely old, which would silently zero out every
// legacy insight.
func (c DreamConsolidated) DecayedConfidence(now time.Time) float64 {
	if c.CreatedAt.IsZero() {
		return ClampConfidence(c.Confidence)
	}
	age := now.Sub(c.CreatedAt)
	if age <= 0 {
		return ClampConfidence(c.Confidence)
	}
	factor := math.Pow(0.5, age.Seconds()/DreamConfidenceHalfLife.Seconds())
	return ClampConfidence(c.Confidence * factor)
}

// WithDream sets Dream consolidation options on the memory Manager.
func WithDream(opts ...DreamOption) Option {
	return func(m *Manager) {
		m.dream = NewDreamManager(m.memoryDir, opts...)
	}
}

// SearchSimilarMemories performs a vector similarity search over consolidated
// Dream insights to find memories relevant to the query.
func (m *Manager) SearchSimilarMemories(ctx context.Context, query string, k int) ([]DreamConsolidated, error) {
	m.mu.RLock()
	dream := m.dream
	m.mu.RUnlock()
	if dream == nil {
		return nil, nil
	}
	return dream.SearchSimilar(ctx, query, k)
}

// Dream returns the DreamManager if Dream is enabled, nil otherwise.
func (m *Manager) Dream() *DreamManager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dream
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func deduplicateStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
