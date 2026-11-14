// Package usage aggregates token usage and estimated cost across the agent's
// LLM calls, persists the totals across restarts, and answers the spend
// queries the budget ceiling enforces against.
//
// It deliberately mirrors internal/incidents' shape: a small struct, one JSON
// file at owner-only mode under ~/.joshbot, a lenient load that starts fresh
// rather than refusing to run. Unlike the incident log this file is rewritten
// (not appended), because a ledger is a running total, not an audit trail.
//
// The package imports only stdlib and internal/providers, so internal/agent
// and cmd/joshbot can both hold it without a cycle in either direction.
package usage

import (
	"strings"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// Rate is a per-model price pair in US dollars per 1M tokens. Input prices
// cover prompt tokens, output prices cover completion tokens.
type Rate struct {
	InputUSD  float64 `json:"input_usd"`
	OutputUSD float64 `json:"output_usd"`
}

// prices is the documented pricing table. Keys are model-id substrings,
// matched case-insensitively against the whole id the provider reported, and
// the longest matching key wins so "claude-opus" cannot be shadowed by a
// shorter family key.
//
// The rates are the providers' published list prices (per 1M tokens, USD, as
// of November 2026) for the families joshbot's default configuration is
// likely to route through. They are ESTIMATES: providers discount cached
// tokens, resellers apply markup, and prices move. The table stays a fixed,
// reviewed literal in this file — it is never scraped at runtime, and an
// unknown model reports cost as unknown rather than guessing. Update a rate
// deliberately, with the date it was checked.
var prices = map[string]Rate{
	"claude-opus":   {InputUSD: 15.00, OutputUSD: 75.00},
	"claude-sonnet": {InputUSD: 3.00, OutputUSD: 15.00},
	"claude-haiku":  {InputUSD: 1.00, OutputUSD: 5.00},
	"gpt-4o":        {InputUSD: 2.50, OutputUSD: 10.00},
	"gpt-4.1":       {InputUSD: 2.00, OutputUSD: 8.00},
}

// Priced reports whether any entry in the table matches the model id.
func Priced(model string) bool {
	_, ok := rateFor(model)
	return ok
}

// rateFor returns the longest pricing-table key that appears (case-insensitively)
// as a substring of the model id. ok is false for unknown models — the caller
// must leave the cost unknown, never zero.
func rateFor(model string) (Rate, bool) {
	id := strings.ToLower(model)
	bestKey := ""
	best, ok := Rate{}, false
	for key, r := range prices {
		if !strings.Contains(id, strings.ToLower(key)) {
			continue
		}
		if len(key) > len(bestKey) {
			bestKey, best, ok = key, r, true
		}
	}
	return best, ok
}

// CostOf prices one call's token usage against the table. The second return
// is false when the model is not in the table: callers must treat the cost as
// UNKNOWN and surface it that way — reporting 0 understates spend, and a
// guessed number fabricates a cost (issue #436).
func CostOf(model string, u providers.Usage) (cost float64, known bool) {
	r, ok := rateFor(model)
	if !ok {
		return 0, false
	}
	cost = float64(u.PromptTokens)/1e6*r.InputUSD + float64(u.CompletionTokens)/1e6*r.OutputUSD
	return cost, true
}
