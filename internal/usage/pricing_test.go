package usage

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

func TestCostOfKnownModel(t *testing.T) {
	u := providers.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	cost, known := CostOf("anthropic/claude-opus-4.1", u)
	if !known {
		t.Fatalf("claude-opus should be priced")
	}
	// 1M input at $15/1M + 1M output at $75/1M.
	if cost < 89.99 || cost > 90.01 {
		t.Errorf("cost = %f, want 90.00", cost)
	}
}

func TestCostOfMatchesCaseInsensitivelyAndWholeID(t *testing.T) {
	u := providers.Usage{PromptTokens: 1_000_000, CompletionTokens: 1}
	if _, known := CostOf("openrouter/Anthropic/CLAUDE-SONNET-4.5", u); !known {
		t.Errorf("case-insensitive substring match failed")
	}
	if _, known := CostOf("z-ai/glm-5.2", u); known {
		t.Errorf("unlisted model must not be priced")
	}
}

func TestCostOfUnknownModelIsUnknownNotZero(t *testing.T) {
	cost, known := CostOf("some/future-model-9000", providers.Usage{PromptTokens: 1e6, CompletionTokens: 1e6})
	if known {
		t.Fatalf("unknown model must not report a cost")
	}
	if cost != 0 {
		t.Errorf("unknown model returned %f; the caller is responsible for never reporting it", cost)
	}
}

func TestCostOfPrefersLongestMatch(t *testing.T) {
	// "claude-opus" must win over any shorter key that also matches — the
	// table has no shorter claude key today, so pin the longest-match rule
	// itself with two overlapping literal substrings added temporarily.
	orig := prices["claude"]
	prices["claude"] = Rate{InputUSD: 999, OutputUSD: 999}
	defer func() { prices["claude"] = orig }()

	u := providers.Usage{PromptTokens: 1, CompletionTokens: 1}
	cost, known := CostOf("claude-opus-4.1", u)
	if !known {
		t.Fatal("should be priced")
	}
	// 1 token at opus rates, not the $999 claude stub rate.
	if cost > 0.001 {
		t.Errorf("cost = %f, longest key (claude-opus) should win", cost)
	}
}

func TestPriced(t *testing.T) {
	if !Priced("gpt-4o-2024-08-06") {
		t.Error("gpt-4o should be priced")
	}
	if Priced("") || Priced("unknown/model") {
		t.Error("unknown models must not report priced")
	}
}
