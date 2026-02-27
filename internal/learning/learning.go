package learning

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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
			if err := c.RunOnce(context.Background()); err != nil {
				// best-effort: ignore errors
				_ = err
			}
			select {
			case <-ticker.C:
				continue
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

	// take last N non-empty lines as recent summary input (configurable, default 12)
	lines := []string{}
	for _, ln := range strings.Split(strings.TrimSpace(hist), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	n := c.historyLines
	if len(lines) < n {
		n = len(lines)
	}
	recent := strings.Join(lines[len(lines)-n:], "\n")

	var summary string
	if c.provider != nil {
		sys := "You are a memory consolidation assistant. Extract a short list of factual one-line statements from the conversation."
		req := providers.ChatRequest{
			Model:       c.provider.Config().Model,
			Messages:    []providers.Message{{Role: providers.RoleSystem, Content: sys}, {Role: providers.RoleUser, Content: recent}},
			MaxTokens:   200,
			Temperature: 0.0,
		}
		resp, err := c.provider.Chat(ctx, req)
		if err == nil && len(resp.Choices) > 0 {
			summary = resp.Choices[0].Message.Content
		}
	}

	if summary == "" {
		// fallback: pick lines that look like facts (contain ':' or '- ')
		facts := []string{}
		for _, ln := range strings.Split(recent, "\n") {
			if strings.Contains(ln, ":") || strings.HasPrefix(ln, "- ") || len(ln) < 200 {
				facts = append(facts, ln)
			}
		}
		if len(facts) == 0 {
			facts = strings.Split(recent, "\n")
		}
		summary = strings.Join(facts, "\n")
	}

	// Append a short header and the summary to MEMORY.md
	content := "\n## Consolidated Facts\n" + summary + "\n"
	// read existing memory and append (with deduplication and limit)
	memText, err := c.mem.LoadMemory(ctx)
	if err != nil {
		return err
	}
	newText := mergeConsolidatedFacts(memText, content, c.maxFacts)
	if err := c.mem.WriteMemory(ctx, newText); err != nil {
		return err
	}

	return nil
}

func mergeConsolidatedFacts(memoryText, consolidatedSection string, maxFacts int) string {
	// Step 1: Parse existing facts from memory for deduplication
	existingFacts := []string{}
	hasExistingSection := false

	// Find the consolidated section in memory
	consolidationIdx := strings.Index(memoryText, "## Consolidated Facts")
	if consolidationIdx >= 0 {
		hasExistingSection = true
		// Extract existing facts after the header
		sectionStart := consolidationIdx + len("## Consolidated Facts")
		sectionContent := memoryText[sectionStart:]
		for _, line := range strings.Split(sectionContent, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				existingFacts = append(existingFacts, line)
			}
		}
	}

	// Step 2: Parse new facts from the consolidated section
	newFacts := []string{}
	seen := map[string]bool{}

	// Add existing facts to seen set for deduplication
	for _, f := range existingFacts {
		seen[f] = true
	}

	// Extract new facts, skipping duplicates
	for _, line := range strings.Split(consolidatedSection, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "##") {
			if !seen[line] {
				newFacts = append(newFacts, line)
				seen[line] = true
			}
		}
	}

	// Step 3: Combine facts - keep most recent up to maxFacts
	// New facts first (they're more recent), then existing non-duplicates
	allFacts := newFacts
	for _, f := range existingFacts {
		if len(allFacts) >= maxFacts {
			break
		}
		allFacts = append(allFacts, f)
	}

	// If we still have more than maxFacts, truncate to maxFacts (keeping newest)
	if len(allFacts) > maxFacts {
		allFacts = allFacts[:maxFacts]
	}

	// Step 4: Build the new memory text
	var result string
	if hasExistingSection {
		// Replace existing section
		beforeSection := memoryText[:consolidationIdx]
		result = beforeSection + "## Consolidated Facts\n" + strings.Join(allFacts, "\n") + "\n"
	} else {
		// Add new section
		if strings.TrimSpace(memoryText) != "" {
			result = strings.TrimRight(memoryText, "\n") + "\n## Consolidated Facts\n" + strings.Join(allFacts, "\n") + "\n"
		} else {
			result = "## Consolidated Facts\n" + strings.Join(allFacts, "\n") + "\n"
		}
	}

	return result
}
