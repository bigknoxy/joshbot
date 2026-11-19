package main

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/tools"
	"github.com/bigknoxy/joshbot/internal/usage"
)

// The status document's usage totals are the ledger's totals under different
// names, and the adapter's spill configuration is what the subagent path
// asks the registry for. Both are small, but both sit on a path a user walks
// (`joshbot status`, and a subagent's tool call), so they are pinned here.

func TestOutputUsageTotalsCarriesEveryField(t *testing.T) {
	in := usage.Totals{
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		Calls:            2,
		CostUSD:          0.25,
		UnknownCostCalls: 1,
	}

	got := outputUsageTotals(in)
	if got.PromptTokens != 10 || got.CompletionTokens != 20 || got.TotalTokens != 30 {
		t.Errorf("token counts were not carried over: %+v", got)
	}
	if got.Calls != 2 {
		t.Errorf("Calls = %d, want 2", got.Calls)
	}
	if got.CostUSD != 0.25 {
		t.Errorf("CostUSD = %v, want 0.25", got.CostUSD)
	}
	if got.UnknownCostCalls != 1 {
		t.Errorf("UnknownCostCalls = %d, want 1", got.UnknownCostCalls)
	}
}

func TestToolExecutorAdapterReportsTheSpillConfig(t *testing.T) {
	reg := tools.RegistryWithDefaults(t.TempDir(), true, 5, 5, nil, nil, nil, nil,
		tools.WithSpillConfig(tools.SpillConfig{
			Threshold: 4096,
			MaxSize:   1024,
		}))
	adapter := &toolExecutorAdapter{registry: reg}

	got := adapter.GetSpillConfig()
	if got.Threshold != 4096 {
		t.Errorf("Threshold = %d, want 4096", got.Threshold)
	}
	if got.MaxSize != 1024 {
		t.Errorf("MaxSize = %d, want 1024", got.MaxSize)
	}
}
