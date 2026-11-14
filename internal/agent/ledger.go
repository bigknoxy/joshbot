package agent

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

// UsageLedger records every main-loop LLM call's token usage onto a
// persistent per-model / per-session ledger, and answers the spend queries
// the budget ceiling (agents.defaults.spend_ceiling_usd) enforces against
// before each further call in a turn.
//
// The interface lives here so the agent stays decoupled from
// internal/usage's persistence: *usage.Ledger satisfies it structurally,
// with no import in either direction, the way incidents.Log does for the
// optional incident log. It is injected once via WithUsageLedger before the
// agent runs and is mutex-guarded inside, so concurrent turns (e.g. two
// Telegram senders) recording at once is safe — that is why the value can
// live on the shared Agent struct where the per-request sinks cannot.
//
// Scope note, deliberate: only the main ReAct loop records here. The
// compaction summarizer and subagent runners dial their own calls through
// other paths, so a configuration that leans on those will spend more than
// the ceiling sees; the ledger's totals are a floor, not a meter.
type UsageLedger interface {
	// RecordUsage folds one call into the ledger. model is the id the
	// provider actually answered with (the response's Model field), never
	// the addressed one — the pricing lookup is per model.
	RecordUsage(sessionID, model string, u providers.Usage)
	// Spend returns the known-cost spend (USD) for one session and for the
	// process lifetime. Unpriced models contribute tokens but no cost —
	// see internal/usage/pricing.go.
	Spend(sessionID string) (sessionUSD, totalUSD float64)
}

// WithUsageLedger injects the usage-and-cost ledger. Passing nil is a no-op —
// a nil interface value or a typed nil pointer either way. The typed-nil case
// is not theoretical: cmd/joshbot nils its ledger variable when the file open
// fails and passes it unconditionally, the same trap WithIncidentLog's nil
// check exists for.
func WithUsageLedger(l UsageLedger) Option {
	return func(a *Agent) {
		if l == nil {
			return
		}
		if v := reflect.ValueOf(l); v.Kind() == reflect.Pointer && v.IsNil() {
			return
		}
		a.usageLedger = l
	}
}

// errSpendCeiling is returned by reactLoop when recorded spend has reached
// agents.defaults.spend_ceiling_usd. process() translates it into the
// in-band budget reply — never into a raw error string, the same contract
// every turn-failure path carries.
var errSpendCeiling = errors.New("spend ceiling reached")

// spendCeilingReply builds the in-band answer a ceiling stop reports to the
// user: what has been spent (this session and lifetime), what the ceiling
// is, and what to change. Only priced models count toward the total, and the
// message says so — a silent coverage gap would read as "we are fine".
func (a *Agent) spendCeilingReply(sess *session.Session) string {
	ceiling := a.cfg.Agents.Defaults.SpendCeilingUSD
	if a.usageLedger == nil || ceiling <= 0 || sess == nil {
		// Unreachable in practice: the error is only raised when all three
		// are live. A constant is safer than guessing numbers.
		return "Stopped before overspending: the spend ceiling was reached. " +
			"Raise or clear agents.defaults.spend_ceiling_usd to continue."
	}
	sessionUSD, totalUSD := a.usageLedger.Spend(sess.ID)
	return fmt.Sprintf(
		"Stopped before overspending: joshbot has recorded $%.2f of usage ($%.2f from this session) against the configured spend ceiling of $%.2f — agents.defaults.spend_ceiling_usd. Only models in the pricing table count toward it. Every further turn is refused until the ceiling is raised or removed.",
		totalUSD, sessionUSD, ceiling)
}
