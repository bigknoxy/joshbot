package learning

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// This file is a behavioural eval harness for the consolidation content gate
// added in response to GH issue #73: the consolidator was wrapping the raw
// LLM completion (refusal, meta-commentary, verbose prose, whatever came
// back) as a single high-confidence fact and persisting it straight into
// MEMORY.md, which is loaded into every future turn's context.
//
// It follows the pattern in internal/agent/evalharness_test.go: a provider
// double that replays a scripted completion and records every request it
// received, run through the *real* Consolidator.RunOnce path (no network, no
// API key, no clock dependence), with assertions on what actually lands in
// MEMORY.md rather than on the raw completion string.

// scriptedProvider replays a fixed completion for every Chat call and
// records the requests it was given, so a test can assert on what the
// consolidator actually sent as well as what it did with the reply.
type scriptedProvider struct {
	mu       sync.Mutex
	content  string
	requests []providers.ChatRequest
}

func (p *scriptedProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return &providers.ChatResponse{Choices: []providers.Choice{{
		Message: providers.Message{Role: providers.RoleAssistant, Content: p.content},
	}}}, nil
}

func (p *scriptedProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func (p *scriptedProvider) Transcribe(ctx context.Context, _ []byte, _ string) (string, error) {
	return "", nil
}

func (p *scriptedProvider) Name() string             { return "scripted" }
func (p *scriptedProvider) Config() providers.Config { return providers.DefaultConfig() }

func (p *scriptedProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// newTestConsolidator wires a Consolidator against a fresh memory.Manager
// (with a couple of history lines seeded so RunOnce has something to send)
// and a scripted provider that always replies with content.
func newTestConsolidator(t *testing.T, content string) (*Consolidator, *memory.Manager, *scriptedProvider) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	mm, err := memory.New(tmp)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mm.Initialize(ctx); err != nil {
		t.Fatalf("mem.Initialize: %v", err)
	}
	if err := mm.AppendHistory(ctx, "User asked about deploying the service."); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := mm.AppendHistory(ctx, "Agent walked through the systemd unit file."); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}

	p := &scriptedProvider{content: content}
	c := NewConsolidator(mm, p, time.Hour)
	return c, mm, p
}

// factContents returns the Content of every stored FactSystem fact.
func factContents(t *testing.T, mm *memory.Manager) []string {
	t.Helper()
	results, err := mm.Search(context.Background(), memory.SearchQuery{Category: memory.FactSystem, Max: 1000})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Fact.Content)
	}
	return out
}

// TestEval_RunOnce_ValidOneLinersStored is the control case: a well-formed
// completion of short factual one-liners should be stored verbatim, one fact
// per line, each within the length bound.
func TestEval_RunOnce_ValidOneLinersStored(t *testing.T) {
	ctx := context.Background()
	content := "User's name is Alice.\nAlice is working on the joshbot deployment.\nAlice prefers systemd over Docker."
	c, mm, p := newTestConsolidator(t, content)

	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if p.requestCount() != 1 {
		t.Fatalf("expected exactly 1 chat request, got %d", p.requestCount())
	}

	facts := factContents(t, mm)
	if len(facts) != 3 {
		t.Fatalf("expected 3 stored facts, got %d: %v", len(facts), facts)
	}
	for _, f := range facts {
		if len(f) > maxFactContentLength {
			t.Fatalf("fact exceeds length bound (%d): %q", maxFactContentLength, f)
		}
		if strings.TrimSpace(f) == "" {
			t.Fatalf("stored an empty fact")
		}
	}

	mem, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory: %v", err)
	}
	if !strings.Contains(mem, "Alice") {
		t.Fatalf("expected MEMORY.md to contain consolidated facts, got: %q", mem)
	}
}

// TestEval_RunOnce_RefusalRejected proves a refusal-shaped completion does
// not become a permanent memory entry: MEMORY.md must be byte-identical to
// its pre-RunOnce baseline (a golden diff, not just "non-empty").
func TestEval_RunOnce_RefusalRejected(t *testing.T) {
	ctx := context.Background()
	content := "I don't have enough context to extract facts from this conversation."
	c, mm, _ := newTestConsolidator(t, content)

	baseline, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory baseline: %v", err)
	}

	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	got, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory: %v", err)
	}
	if got != baseline {
		t.Fatalf("expected MEMORY.md unchanged after refusal completion\nbaseline=%q\ngot=%q", baseline, got)
	}
	if facts := factContents(t, mm); len(facts) != 0 {
		t.Fatalf("expected no stored facts after refusal, got: %v", facts)
	}
}

// TestEval_RunOnce_WhitespaceOnlyRejected covers a completion that is not
// the empty string but carries no content — this is the case that actually
// exercises the new gate at the LLM boundary (a literal "" short-circuits to
// the pre-existing heuristic fallback before saveSummary is ever called).
func TestEval_RunOnce_WhitespaceOnlyRejected(t *testing.T) {
	ctx := context.Background()
	content := "   \n\t  \n   "
	c, mm, _ := newTestConsolidator(t, content)

	baseline, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory baseline: %v", err)
	}

	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	got, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory: %v", err)
	}
	if got != baseline {
		t.Fatalf("expected MEMORY.md unchanged after whitespace-only completion\nbaseline=%q\ngot=%q", baseline, got)
	}
}

// TestEval_RunOnce_LongBlobRejected covers a wall-of-prose completion far
// longer than any reasonable one-line fact (5000+ chars, well past what
// MaxTokens:200 is meant to bound). It must be rejected outright rather than
// stored as a single giant fact.
func TestEval_RunOnce_LongBlobRejected(t *testing.T) {
	ctx := context.Background()
	content := strings.Repeat("This is verbose filler prose that does not look like a one-line fact at all. ", 65)
	if len(content) < 4000 {
		t.Fatalf("test setup: blob too short: %d chars", len(content))
	}
	c, mm, _ := newTestConsolidator(t, content)

	baseline, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory baseline: %v", err)
	}

	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	got, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory: %v", err)
	}
	if got != baseline {
		t.Fatalf("expected MEMORY.md unchanged after oversized completion\nbaseline=%q\ngot len=%d", baseline, len(got))
	}
}

// TestSaveSummary_EmptyStringRejected exercises the literal empty-string
// case directly against saveSummary (the function named in the issue as the
// culprit) rather than through RunOnce, since RunOnce treats a literal ""
// completion as "no response" and defers to the pre-existing heuristic
// fallback rather than ever calling saveSummary. This proves the gate itself
// rejects empty content, independent of that fallback routing decision.
func TestSaveSummary_EmptyStringRejected(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	mm, err := memory.New(tmp)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mm.Initialize(ctx); err != nil {
		t.Fatalf("mem.Initialize: %v", err)
	}

	baseline, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory baseline: %v", err)
	}

	c := NewConsolidator(mm, nil, time.Hour)
	if err := c.saveSummary(ctx, ""); err != nil {
		t.Fatalf("saveSummary: %v", err)
	}

	got, err := mm.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory: %v", err)
	}
	if got != baseline {
		t.Fatalf("expected MEMORY.md unchanged after empty completion\nbaseline=%q\ngot=%q", baseline, got)
	}
	if facts := factContents(t, mm); len(facts) != 0 {
		t.Fatalf("expected no stored facts for empty completion, got: %v", facts)
	}
}
