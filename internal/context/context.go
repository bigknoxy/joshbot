package contextpkg

import (
	"context"
	"fmt"
	"strings"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// ModelInfo holds basic model properties used for budgeting.
type ModelInfo struct {
	Name          string
	ContextWindow int // approximate token window
}

// Registry provides heuristics to map model names to context windows.
type Registry struct {
	defaults []ModelInfo
}

// NewRegistry returns a registry pre-seeded with common model classes.
func NewRegistry() *Registry {
	return &Registry{defaults: []ModelInfo{
		{Name: "small", ContextWindow: 4096},
		{Name: "medium", ContextWindow: 8192},
		{Name: "large", ContextWindow: 32768},
	}}
}

// Lookup returns the best-fit ModelInfo for a given model name.
func (r *Registry) Lookup(model string) ModelInfo {
	m := strings.ToLower(model)
	// heuristics
	switch {
	case strings.Contains(m, "gemma") || strings.Contains(m, "llama"):
		return r.defaults[1]
	case strings.Contains(m, "gpt") || strings.Contains(m, "claude"):
		return r.defaults[2]
	case strings.Contains(m, "small"):
		return r.defaults[0]
	default:
		return r.defaults[1]
	}
}

// TokenEstimator approximates tokens from text length. Default: 1 token ~= 4 chars
func TokenEstimator(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// BudgetManager computes how many tokens we can allocate for context given model and desired completion size.
type BudgetManager struct {
	registry *Registry
	// safety margin to reserve tokens for system messages
	margin int
}

// NewBudgetManager constructs a BudgetManager.
func NewBudgetManager(reg *Registry, margin int) *BudgetManager {
	if margin <= 0 {
		margin = 100
	}
	return &BudgetManager{registry: reg, margin: margin}
}

// ComputeBudget returns available context tokens for the prompt given model and maxCompletion.
func (b *BudgetManager) ComputeBudget(model string, maxCompletion int) int {
	info := b.registry.Lookup(model)
	avail := info.ContextWindow - maxCompletion - b.margin
	if avail < 256 {
		avail = 256
	}
	return avail
}

// Compressor can compact a list of messages to fit within a token budget.
// If Provider is non-nil, it can request LLM-based summarization.
type Compressor struct {
	Provider providers.Provider // optional
}

// CompressMessages returns a compacted string representation of messages limited by budget tokens.
// It naively keeps the most recent messages until the token budget is met; if exceeded and a Provider
// is available, it will ask the provider to summarize them.
func (c *Compressor) CompressMessages(model string, messages []providers.Message, budget int) (string, error) {
	// Heuristic: if there are many messages, prefer provider summarization when available.
	// DEBUG LOG
	// fmt.Printf("CompressMessages called: messages=%d budget=%d\n", len(messages), budget)
	if c.Provider != nil && len(messages) > 50 {
		sys := "You are a summarization assistant. Produce a concise summary that preserves important facts and decisions."
		joinedAll := ""
		for _, mm := range messages {
			role := string(mm.Role)
			joinedAll += role + ": " + mm.Content + "\n\n"
		}
		req := providers.ChatRequest{
			Model: model,
			Messages: []providers.Message{
				{Role: providers.RoleSystem, Content: sys},
				{Role: providers.RoleUser, Content: "Summarize the following conversation in under 200 tokens:\n\n" + joinedAll},
			},
			MaxTokens: 200,
		}
		resp, err := c.Provider.Chat(context.Background(), req)
		if err == nil && len(resp.Choices) > 0 {
			return resp.Choices[0].Message.Content, nil
		}
	}
	// join messages from newest backwards until budget
	var parts []string
	tokens := 0
	// iterate from end
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		est := TokenEstimator(m.Content)
		if tokens+est > budget && len(parts) > 0 {
			break
		}
		parts = append([]string{fmt.Sprintf("%s: %s", m.Role, m.Content)}, parts...)
		tokens += est
	}

	joined := strings.Join(parts, "\n\n")
	if TokenEstimator(joined) <= budget {
		return joined, nil
	}

	// If provider available, request a concise summary
	if c.Provider != nil {
		sys := "You are a summarization assistant. Produce a concise summary that preserves important facts and decisions."
		req := providers.ChatRequest{
			Model: model,
			Messages: []providers.Message{
				{Role: providers.RoleSystem, Content: sys},
				{Role: providers.RoleUser, Content: "Summarize the following conversation in under 200 tokens:\n\n" + joined},
			},
			MaxTokens: 200,
		}
		resp, err := c.Provider.Chat(context.Background(), req)
		if err == nil && len(resp.Choices) > 0 {
			return resp.Choices[0].Message.Content, nil
		}
	}

	// fallback: truncate
	out := joined
	// keep approximately budget tokens worth of chars
	maxChars := budget * 4
	if len(out) > maxChars {
		out = out[len(out)-maxChars:]
	}
	return out, nil
}
