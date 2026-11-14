package output

import (
	"bytes"
	"strings"
	"testing"
)

// -- #436: `joshbot status` usage block. Absent (not zero) until anything has
// been recorded, and the unknown-cost gap is stated, not folded into spend.
func TestRenderStatusTextUsageBlock(t *testing.T) {
	var b bytes.Buffer
	RenderStatusText(&b, Status{
		Usage: &UsageSummary{
			Lifetime: UsageTotals{PromptTokens: 1000, CompletionTokens: 235, TotalTokens: 1235, Calls: 9, CostUSD: 1.23},
			Models: []UsageEntry{
				{Name: "openrouter/z-ai/glm-5.2", UsageTotals: UsageTotals{TotalTokens: 735, Calls: 4, CostUSD: 0.50}},
			},
			Sessions: []UsageEntry{
				{Name: "cli:cli_user", UsageTotals: UsageTotals{TotalTokens: 735, Calls: 4, CostUSD: 0.50}},
			},
		},
	})
	out := b.String()
	for _, want := range []string{
		"Usage:          1,235 tokens across 9 calls — est. spend $1.23",
		"  model:   openrouter/z-ai/glm-5.2",
		"  session: cli:cli_user",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status text missing %q:\n%s", want, out)
		}
	}
	// Thousands grouping, not a raw integer.
	if !strings.Contains(out, "1,235") {
		t.Error("token counts should use thousands grouping")
	}
}

func TestRenderStatusTextUsageUnknownCostFlagged(t *testing.T) {
	var b bytes.Buffer
	RenderStatusText(&b, Status{
		Usage: &UsageSummary{
			Lifetime: UsageTotals{TotalTokens: 500, Calls: 3, UnknownCostCalls: 3},
		},
	})
	out := b.String()
	if !strings.Contains(out, "cost unknown on 3 call(s)") {
		t.Errorf("unknown-cost calls must be visible, got:\n%s", out)
	}
	if !strings.Contains(out, "$0.00") {
		t.Errorf("zero-cost (all-unpriced) should still print $0.00, got:\n%s", out)
	}
}

func TestRenderStatusTextNoUsageBlock(t *testing.T) {
	var b bytes.Buffer
	RenderStatusText(&b, Status{Version: "test"})
	if strings.Contains(b.String(), "Usage:") {
		t.Error("no ledger: the Usage block must be absent, not zero")
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234567: "-1,234,567"}
	for in, want := range cases {
		if got := formatTokens(in); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", in, got, want)
		}
	}
}
