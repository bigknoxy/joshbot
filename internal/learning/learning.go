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
}

// NewConsolidator constructs a consolidator for the workspace (expects memory.Manager initialized).
func NewConsolidator(mem *memory.Manager, provider providers.Provider, interval time.Duration) *Consolidator {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &Consolidator{mem: mem, provider: provider, interval: interval, stopCh: make(chan struct{})}
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

	// take last 6 non-empty lines as recent summary input
	lines := []string{}
	for _, ln := range strings.Split(strings.TrimSpace(hist), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	n := 6
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
	// read existing memory and append
	memText, err := c.mem.LoadMemory(ctx)
	if err != nil {
		return err
	}
	newText := strings.TrimRight(memText, "\n") + "\n" + content
	if err := c.mem.WriteMemory(ctx, newText); err != nil {
		return err
	}

	return nil
}
