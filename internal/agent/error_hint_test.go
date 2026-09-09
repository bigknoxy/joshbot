package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

func TestLLMErrorHint(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string // substring; "" means no hint
	}{
		{"nil", nil, ""},
		{"bad key plain", fmt.Errorf("LLM call failed: API error (401): bad key"), "rejected the API key"},
		{"forbidden", fmt.Errorf("API error (403): denied"), "rejected the API key"},
		{"bad key structured", &providers.FallbackError{StatusCode: 401, Provider: "openrouter", Message: "no"}, "openrouter rejected the API key"},
		{"model 404", fmt.Errorf("API error (404): no such model"), "check the model name"},
		{"ollama 404", &providers.FallbackError{StatusCode: 404, Provider: "ollama", Message: "not found"}, "ollama pull"},
		{"rate limit", &providers.FallbackError{StatusCode: 429, Provider: "nvidia", Message: "slow down"}, "--fallback"},
		{"aggregate", fmt.Errorf("all providers failed (tried: a → b; a: HTTP 429; b: HTTP 503): boom"), "preflight"},
		{"context length openai style", &providers.FallbackError{StatusCode: 400, Provider: "openai", Message: "This model's maximum context length is 128000 tokens. However, your messages resulted in 150000 tokens."}, "context window"},
		{"context length code style", fmt.Errorf("API error (400): {\"error\":{\"message\":\"reduce the length\",\"code\":\"context_length_exceeded\"}}"), "context window"},
		{"context length generic", &providers.FallbackError{StatusCode: 413, Provider: "custom", Message: "request exceeds the model's context window"}, "context window"},
		{"plain 400, not context length", &providers.FallbackError{StatusCode: 400, Provider: "openai", Message: "invalid request: missing field"}, ""},
		{"plain error", errors.New("something odd"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llmErrorHint(tc.err)
			if tc.want == "" {
				if got != "" {
					t.Errorf("llmErrorHint(%v) = %q, want no hint", tc.err, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("llmErrorHint(%v) = %q, want substring %q", tc.err, got, tc.want)
			}
		})
	}
}
