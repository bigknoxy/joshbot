package learning

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// Consolidator periodically summarizes recent HISTORY and writes key facts into MEMORY.md
type Consolidator struct {
	mem      *memory.Manager
	provider providers.Provider // optional
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// Configurable options
	historyLines int // number of history lines to process
	maxFacts     int // max consolidated facts to keep
}

// ConsolidatorConfig holds configuration options for the Consolidator
type ConsolidatorConfig struct {
	HistoryLines int // default 12
	MaxFacts     int // default 20
}

// DefaultConsolidatorConfig returns sensible defaults
func DefaultConsolidatorConfig() ConsolidatorConfig {
	return ConsolidatorConfig{
		HistoryLines: 12,
		MaxFacts:     20,
	}
}

// NewConsolidator constructs a consolidator for the workspace (expects memory.Manager initialized).
func NewConsolidator(mem *memory.Manager, provider providers.Provider, interval time.Duration) *Consolidator {
	return NewConsolidatorWithConfig(mem, provider, interval, DefaultConsolidatorConfig())
}

// NewConsolidatorWithConfig constructs a consolidator with custom configuration.
func NewConsolidatorWithConfig(mem *memory.Manager, provider providers.Provider, interval time.Duration, cfg ConsolidatorConfig) *Consolidator {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	if cfg.HistoryLines <= 0 {
		cfg.HistoryLines = 12
	}
	if cfg.MaxFacts <= 0 {
		cfg.MaxFacts = 20
	}
	return &Consolidator{
		mem:          mem,
		provider:     provider,
		interval:     interval,
		stopCh:       make(chan struct{}),
		historyLines: cfg.HistoryLines,
		maxFacts:     cfg.MaxFacts,
	}
}

// Start runs background consolidation loop.
func (c *Consolidator) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			_ = c.RunOnce(context.Background()) // best-effort
			select {
			case <-ticker.C:
			case <-c.stopCh:
				return
			}
		}
	}()
}

// Stop stops background worker.
func (c *Consolidator) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

// RunOnce performs a single consolidation pass: reads HISTORY.md, summarizes last N lines, appends to MEMORY.md.
func (c *Consolidator) RunOnce(ctx context.Context) error {
	if c.mem == nil {
		return fmt.Errorf("no memory manager")
	}

	hist, err := c.mem.LoadHistory(ctx, "")
	if err != nil {
		return err
	}
	if strings.TrimSpace(hist) == "" {
		return nil
	}

	// Take last N non-empty lines as recent summary input.
	var lines []string
	for _, ln := range strings.Split(strings.TrimSpace(hist), "\n") {
		if trimmed := strings.TrimSpace(ln); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	n := c.historyLines
	if len(lines) < n {
		n = len(lines)
	}
	recent := strings.Join(lines[len(lines)-n:], "\n")

	var summary string
	if c.provider != nil {
		sys := "You are a memory consolidation assistant. Extract a short list of factual one-line statements from the conversation, one per line, with no preamble or commentary. If there is nothing worth remembering, respond with nothing."
		req := providers.ChatRequest{
			Model: c.provider.Config().Model,
			Messages: []providers.Message{
				{Role: providers.RoleSystem, Content: sys},
				{Role: providers.RoleUser, Content: recent},
			},
			MaxTokens:   200,
			Temperature: 0.0,
		}
		resp, err := c.provider.Chat(ctx, req)
		if err == nil && len(resp.Choices) > 0 {
			summary = resp.Choices[0].Message.Content
		}
	}

	switch {
	case summary == "":
		return c.heuristicFallback(ctx, recent)
	default:
		return c.saveSummary(ctx, summary)
	}
}

// saveSummary applies a deterministic content gate to the raw completion
// before persisting anything: it is split into candidate one-line facts,
// each of which must be non-empty, within maxFactContentLength, and not look
// like a refusal or meta-commentary (see extractValidFacts). A completion
// that yields no valid facts is rejected outright — logged and discarded —
// leaving MEMORY.md unchanged, rather than being stored as a single
// unfiltered blob.
//
// The consolidation prompt asks for plain one-line statements, not JSON, so
// this no longer attempts a JSON.Unmarshal fast path: that branch was dead
// in production (the prompt never requests JSON) and its presence implied a
// contract that was never exercised. See GH issue #73.
func (c *Consolidator) saveSummary(ctx context.Context, summary string) error {
	validFacts, reason := extractValidFacts(summary, maxFactContentLength, c.maxFacts)
	if len(validFacts) == 0 {
		log.Warn("consolidation rejected: completion failed content gate",
			"reason", reason,
			"preview", previewString(summary, 80))
		return nil
	}

	now := time.Now()
	facts := make([]memory.Fact, 0, len(validFacts))
	for _, content := range validFacts {
		facts = append(facts, memory.Fact{
			ID:         memory.FactID(memory.FactSystem, content),
			Category:   memory.FactSystem,
			Content:    content,
			Confidence: 0.6,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}
	return c.mem.ReconcileFacts(ctx, facts)
}

// previewString returns s trimmed to at most n characters, for safe logging
// of otherwise-unbounded model output.
func previewString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// heuristicFallback picks lines that look like facts and writes them as consolidated section.
func (c *Consolidator) heuristicFallback(ctx context.Context, recent string) error {
	var facts []string
	for _, ln := range strings.Split(recent, "\n") {
		if strings.Contains(ln, ":") || strings.HasPrefix(ln, "- ") {
			facts = append(facts, ln)
		}
	}
	if len(facts) == 0 {
		facts = strings.Split(recent, "\n")
	}

	existing, _ := c.mem.LoadMemory(ctx)
	newText := mergeConsolidatedFacts(existing, strings.Join(facts, "\n"), c.maxFacts)
	return c.mem.WriteMemory(ctx, newText)
}

func mergeConsolidatedFacts(memoryText, consolidatedSection string, maxFacts int) string {
	consolidationIdx := strings.Index(memoryText, "## Consolidated Facts")
	hasExistingSection := consolidationIdx >= 0

	seen := make(map[string]bool)
	var existingFacts []string

	if hasExistingSection {
		sectionContent := memoryText[consolidationIdx+len("## Consolidated Facts"):]
		for _, line := range strings.Split(sectionContent, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				existingFacts = append(existingFacts, trimmed)
				seen[trimmed] = true
			}
		}
	}

	var newFacts []string
	for _, line := range strings.Split(consolidatedSection, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "##") && !seen[trimmed] {
			newFacts = append(newFacts, trimmed)
			seen[trimmed] = true
		}
	}

	// Combine: new facts first (more recent), then existing, limited to maxFacts.
	allFacts := newFacts
	for _, f := range existingFacts {
		if len(allFacts) >= maxFacts {
			break
		}
		allFacts = append(allFacts, f)
	}
	if len(allFacts) > maxFacts {
		allFacts = allFacts[:maxFacts]
	}

	factsStr := strings.Join(allFacts, "\n")
	switch {
	case hasExistingSection:
		return memoryText[:consolidationIdx] + "## Consolidated Facts\n" + factsStr + "\n"
	case strings.TrimSpace(memoryText) != "":
		return strings.TrimRight(memoryText, "\n") + "\n## Consolidated Facts\n" + factsStr + "\n"
	default:
		return "## Consolidated Facts\n" + factsStr + "\n"
	}
}
