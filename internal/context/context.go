package contextpkg

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// ModelInfo holds basic model properties used for budgeting.
type ModelInfo struct {
	Name          string
	ContextWindow int // approximate token window
}

// DefaultContextWindow is the window assumed for a model the registry does
// not recognise. It used to be the 4096-token "small" class, which no current
// hosted model has: with the default max_tokens of 8192 the budget arithmetic
// went negative, was clamped to 256 tokens, and proactive compaction then
// fired at ~700 characters of history and summarized the whole conversation
// away on every tool-using turn (#346). Guessing large costs an oversized
// request that the provider rejects with a clear error; guessing small costs
// the conversation silently.
const DefaultContextWindow = 131072

// Registry provides heuristics to map model names to context windows.
type Registry struct {
	defaults  []ModelInfo
	overrides map[string]int
}

// NewRegistry returns a registry pre-seeded with common model classes.
func NewRegistry() *Registry {
	return &Registry{defaults: []ModelInfo{
		{Name: "small", ContextWindow: 4096},
		{Name: "medium", ContextWindow: 8192},
		{Name: "large", ContextWindow: 32768},
	}, overrides: map[string]int{
		"openai/gpt-4o":                    128000,
		"openai/gpt-4.1":                   128000,
		"claude-sonnet-4-20250514":         200000,
		"anthropic/claude-3.5-sonnet":      200000,
		"openai/gpt-4":                     8192,
		"z-ai/glm-4.5-air:free":            8192,
		"openai/llama3.2":                  8192,
		"meta-llama/llama-3.2-3b-instruct": 8192,
		"openrouter/free":                  131072,
		"nvidia/stepfun-ai/step-3.5-flash": 131072,
	}}
}

// Lookup returns the best-fit ModelInfo for a given model name.
func (r *Registry) Lookup(model string) ModelInfo {
	m := strings.ToLower(model)
	if window, ok := r.overrides[m]; ok {
		return ModelInfo{Name: model, ContextWindow: window}
	}

	// Heuristic detection for large models by name pattern
	// Check for explicit size indicators in model name
	if strings.Contains(m, "large") || strings.Contains(m, "128k") || strings.Contains(m, "200k") {
		return ModelInfo{Name: model, ContextWindow: 131072}
	}
	if strings.Contains(m, "32k") {
		return ModelInfo{Name: model, ContextWindow: 32768}
	}
	if strings.Contains(m, "16k") {
		return ModelInfo{Name: model, ContextWindow: 16384}
	}

	// heuristics
	switch {
	case strings.Contains(m, "gemma") || strings.Contains(m, "llama"):
		return r.defaults[1]
	case strings.Contains(m, "gpt") || strings.Contains(m, "claude"):
		return r.defaults[2]
	case strings.Contains(m, "small"):
		return r.defaults[0]
	default:
		return ModelInfo{Name: model, ContextWindow: DefaultContextWindow}
	}
}

// SetOverride sets/updates an exact model override for context window.
func (r *Registry) SetOverride(model string, contextWindow int) {
	if contextWindow <= 0 {
		return
	}
	if r.overrides == nil {
		r.overrides = map[string]int{}
	}
	r.overrides[strings.ToLower(strings.TrimSpace(model))] = contextWindow
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
//
// When maxCompletion (plus the margin) eats the whole window — a
// misconfiguration, not a real model — the budget falls back to a quarter of
// the window rather than a 256-token floor. A 256-token budget is not a
// degraded mode, it is amnesia: compaction summarizes the conversation into
// a few hundred characters on every tool call (#346).
func (b *BudgetManager) ComputeBudget(model string, maxCompletion int) int {
	info := b.registry.Lookup(model)
	avail := info.ContextWindow - maxCompletion - b.margin
	if floor := info.ContextWindow / 4; avail < floor {
		avail = floor
	}
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

// lastNonEmptyContent returns the tail of the last message with non-empty
// Content, truncated to maxChars. Returns false if no message has content.
func lastNonEmptyContent(messages []providers.Message, maxChars int) (string, bool) {
	// A negative maxChars would make the tail slice below run off the front of
	// the string and panic. Clamp it: "no room at all" is a failed
	// compression, not a crash.
	if maxChars < 0 {
		maxChars = 0
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Content != "" {
			content := messages[i].Content
			if len(content) > maxChars {
				content = content[len(content)-maxChars:]
			}
			// A non-positive maxChars truncates the content away entirely.
			// Reporting that as a successful fallback made CompressMessages
			// return an empty context with a nil error — the exact silent
			// failure this fallback exists to prevent. Report "no content"
			// instead, so the caller gets an error it can act on.
			if content == "" {
				return "", false
			}
			return content, true
		}
	}
	return "", false
}

// minPlausibleSummaryChars is the floor below which a provider-generated
// summary is treated as degenerate — an empty completion, a refusal stub,
// a truncated stream — rather than a genuine summary of 50+ messages of
// conversation, and is therefore rejected in favor of the deterministic
// fallback. This is a length heuristic only; it is deliberately not an
// LLM-as-judge check.
const minPlausibleSummaryChars = 15

// isPlausibleSummary reports whether s, once surrounding whitespace is
// trimmed, is non-empty and at least minChars long.
func isPlausibleSummary(s string, minChars int) bool {
	return len(strings.TrimSpace(s)) >= minChars
}

// CompressMessages returns a compacted string representation of messages limited by token budget.
// It naively keeps the most recent messages until the token budget is met; if exceeded and a Provider
// is available, it will ask the provider to summarize them. ctx governs the provider call(s) and is
// the caller's responsibility to cancel/time out; CompressMessages does not create its own background
// context for provider requests.
func (c *Compressor) CompressMessages(ctx context.Context, model string, messages []providers.Message, budget int) (string, error) {
	if len(messages) == 0 {
		return "", fmt.Errorf("no messages to compress")
	}

	// If all messages have empty content, return error early
	allEmpty := true
	for _, m := range messages {
		if m.Content != "" {
			allEmpty = false
			break
		}
	}
	if allEmpty {
		return "", fmt.Errorf("all %d messages have empty content", len(messages))
	}

	// Prefer a provider summary of the whole conversation whenever a provider
	// is available. This used to run only above 50 messages, and the second
	// provider path further down summarized `joined` — which is already cut
	// down to the budget by the newest-backwards join, so at a small budget
	// the model was asked to "summarize" a single message. Both left the
	// deterministic join, which is a truncation and not a summary, as the
	// path almost every real compaction took (#346).
	if c.Provider != nil {
		if summary, ok := c.summarizeWithProvider(ctx, model, messages); ok {
			return summary, nil
		}
		// A missing/empty/whitespace-only/implausibly-short summary (refusal,
		// truncated stream, provider error) is a failed compression: fall
		// through to the deterministic newest-backwards join below instead of
		// returning an empty compressed context with a nil error.
	}
	// join messages from newest backwards until budget
	var parts []string
	tokens := 0
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
	if joined == "" {
		if content, ok := lastNonEmptyContent(messages, budget*4); ok {
			return content, nil
		}
		return "", fmt.Errorf("no compressible content in %d messages", len(messages))
	}
	if TokenEstimator(joined) <= budget {
		return joined, nil
	}

	// fallback: truncate
	out := joined
	maxChars := budget * 4
	// A caller doing its own budget arithmetic can hand us a negative budget.
	// Left unclamped, the tail slice below is out[len(out)+n:] — a panic, not
	// a compression failure. Clamp to zero so it degrades into the "nothing
	// fits" path, which reports an error.
	if maxChars < 0 {
		maxChars = 0
	}
	if len(out) > maxChars {
		out = out[len(out)-maxChars:]
	}
	if out == "" {
		if content, ok := lastNonEmptyContent(messages, maxChars); ok {
			return content, nil
		}
		return "", fmt.Errorf("no compressible content after truncation in %d messages", len(messages))
	}
	return out, nil
}

// summaryMaxTokens bounds the provider summary. 200 tokens was enough to lose
// the thread of a six-turn chat; a summary that stands in for the whole
// earlier conversation needs room for facts, decisions and open requests.
const summaryMaxTokens = 768

// summaryInputMaxChars caps how much conversation is sent to be summarized,
// newest first, so a very long history does not itself overflow the model.
const summaryInputMaxChars = 200_000

// summarizeWithProvider asks the provider for a summary of the whole
// conversation. It reports false when the provider errors or answers with
// something too short to be a summary, so the caller can fall back.
func (c *Compressor) summarizeWithProvider(ctx context.Context, model string, messages []providers.Message) (string, bool) {
	var b strings.Builder
	for _, mm := range messages {
		if mm.Content == "" {
			continue
		}
		b.WriteString(string(mm.Role))
		b.WriteString(": ")
		b.WriteString(mm.Content)
		b.WriteString("\n\n")
	}
	joined := b.String()
	if len(joined) > summaryInputMaxChars {
		cut := len(joined) - summaryInputMaxChars
		for cut < len(joined) && !utf8.RuneStart(joined[cut]) {
			cut++
		}
		joined = "[earlier conversation omitted]\n\n" + joined[cut:]
	}
	sys := "You are a summarization assistant. Summarize the conversation so an assistant that reads only your summary can continue it: keep the user's facts and preferences, decisions made, results already obtained, and any request that is still open or in progress. Write in the third person about the user and the assistant."
	req := providers.ChatRequest{
		Model: model,
		Messages: []providers.Message{
			{Role: providers.RoleSystem, Content: sys},
			{Role: providers.RoleUser, Content: "Summarize the following conversation concisely:\n\n" + joined},
		},
		MaxTokens: summaryMaxTokens,
	}
	resp, err := c.Provider.Chat(ctx, req)
	if err != nil || resp == nil || len(resp.Choices) == 0 {
		return "", false
	}
	summary := resp.Choices[0].Message.Content
	if !isPlausibleSummary(summary, minPlausibleSummaryChars) {
		return "", false
	}
	return summary, true
}
