package subagent

import (
	"context"
	"fmt"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// Runner runs a short-lived, isolated agent (subagent) for focused tasks.
// It uses the Provider interface to execute a single chat-turn and returns the assistant text.
type Runner struct {
	provider    providers.Provider
	model       string
	maxTokens   int
	temperature float64
	timeout     time.Duration
}

// NewRunner constructs a Runner.
func NewRunner(provider providers.Provider, model string, maxTokens int, temperature float64, timeout time.Duration) *Runner {
	return &Runner{provider: provider, model: model, maxTokens: maxTokens, temperature: temperature, timeout: timeout}
}

// Run executes the subagent with the given prompt. It creates a constrained system prompt
// instructing the model to act in isolation and return a concise answer. The subagent does
// one model call only (no tool execution).
func (r *Runner) Run(ctx context.Context, prompt string) (string, error) {
	if r.provider == nil {
		return "", fmt.Errorf("no provider")
	}

	sys := `You are an isolated subagent. Execute the following task only. Do not call external tools or
make network requests. Keep the response concise (max 500 tokens) and return final answer only.`

	req := providers.ChatRequest{
		Model:       r.model,
		MaxTokens:   r.maxTokens,
		Temperature: r.temperature,
		Messages: []providers.Message{
			{Role: providers.RoleSystem, Content: sys},
			{Role: providers.RoleUser, Content: prompt},
		},
		Stream: false,
	}

	callCtx := ctx
	if r.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	resp, err := r.provider.Chat(callCtx, req)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return resp.Choices[0].Message.Content, nil
}
