package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
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
	// Reserve holds the worst case for one call against the spend ceiling
	// and returns an opaque reservation id. The hold and the check that
	// admitted the call happen together, so two turns cannot both pass a
	// check that neither has paid for yet. The error is returned when the
	// ceiling cannot cover the call.
	Reserve(model string, ceilingUSD float64, promptTokens, maxCompletionTokens int) (string, error)
	// Commit records the call's actual usage against its reservation: the
	// bound it held is released and the actual cost takes its place.
	Commit(reservationID, sessionID, model string, u providers.Usage)
	// Release drops a reservation whose call spent nothing, so the budget
	// it held returns to the ceiling.
	Release(reservationID string)
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

// CeilingError is what a ledger's Reserve returns when the spend ceiling
// cannot cover the call. It carries the figures the reply needs — what was
// left of the ceiling and what the call could have cost — so the agent can
// explain the stop instead of quoting the ledger's own wording.
type CeilingError interface {
	error
	// Remaining is what was left of the ceiling when the call was refused.
	Remaining() float64
	// Bound is what the refused call could have cost at most.
	Bound() float64
}

// errSpendCeiling is returned by reactLoop when recorded spend has reached
// agents.defaults.spend_ceiling_usd. process() translates it into the
// in-band budget reply — never into a raw error string, the same contract
// every turn-failure path carries.
var errSpendCeiling = errors.New("spend ceiling reached")

// reserveCallBudget holds the budget for one LLM call against the spend
// ceiling, when one is configured. The reservation is what makes the
// ceiling atomic: the check and the hold happen together, so a burst of
// concurrent turns cannot each pass a check that none of them has paid for
// yet, and the recorded spend cannot pass the ceiling while calls are in
// flight (#457).
//
// It returns the reservation id to settle the call with, or "" when no
// ceiling is in force — the call is then recorded directly, as it always
// was.
func (a *Agent) reserveCallBudget(req providers.ChatRequest) (string, error) {
	ceiling := a.cfg.Agents.Defaults.SpendCeilingUSD
	if a.usageLedger == nil || ceiling <= 0 {
		return "", nil
	}
	maxCompletion := req.MaxTokens
	if maxCompletion <= 0 {
		maxCompletion = config.DefaultMaxTokens
	}
	return a.usageLedger.Reserve(req.Model, ceiling, estimatePromptTokens(req), maxCompletion)
}

// releaseCallBudget gives back the budget a call held when it spent
// nothing — the provider failed before it billed the call.
func (a *Agent) releaseCallBudget(reservationID string) {
	if a.usageLedger == nil || reservationID == "" {
		return
	}
	a.usageLedger.Release(reservationID)
}

// recordCallUsage folds one call's usage into the ledger. A call that was
// admitted against a reservation settles it here: the bound it held is
// released and the actual cost takes its place.
func (a *Agent) recordCallUsage(sess *session.Session, reservationID, model string, u providers.Usage) {
	if a.usageLedger == nil {
		return
	}
	if reservationID != "" {
		a.usageLedger.Commit(reservationID, sess.ID, model, u)
		return
	}
	a.usageLedger.RecordUsage(sess.ID, model, u)
}

// estimatePromptTokens estimates the prompt tokens of one request: its
// messages (the system prompt included) and the tool schemas the provider
// is sent alongside them. The estimate is an upper bound on what the
// provider will bill for the prompt, not an exact count — the ledger
// applies its own margin on top.
func estimatePromptTokens(req providers.ChatRequest) int {
	total := 0
	for _, m := range req.Messages {
		total += ctxpkg.TokenEstimator(m.Content)
	}
	if len(req.Tools) > 0 {
		if b, err := json.Marshal(req.Tools); err == nil {
			total += ctxpkg.TokenEstimator(string(b))
		}
	}
	return total
}

// spendCeilingReply builds the in-band answer a ceiling stop reports to the
// user: what has been spent (this session and lifetime), what the ceiling
// is, and what to change. Only priced models count toward the total, and the
// message says so — a silent coverage gap would read as "we are fine".
//
// cause is the refusal the ledger returned; when it carries the figures, the
// reply says what was left of the ceiling and what the call would have cost,
// because a ceiling that holds back the worst case for the next call stops
// a turn before the recorded spend reaches it.
func (a *Agent) spendCeilingReply(sess *session.Session, cause error) string {
	ceiling := a.cfg.Agents.Defaults.SpendCeilingUSD
	if a.usageLedger == nil || ceiling <= 0 || sess == nil {
		// Unreachable in practice: the error is only raised when all three
		// are live. A constant is safer than guessing numbers.
		return "Stopped before overspending: the spend ceiling was reached. " +
			"Raise or clear agents.defaults.spend_ceiling_usd to continue."
	}
	sessionUSD, totalUSD := a.usageLedger.Spend(sess.ID)

	held := ""
	var ce CeilingError
	if errors.As(cause, &ce) {
		held = fmt.Sprintf(" $%.2f of the ceiling was left and the next call could have cost up to $%.2f, so it was not made.",
			ce.Remaining(), ce.Bound())
	}
	return fmt.Sprintf(
		"Stopped before overspending: joshbot has recorded $%.2f of usage ($%.2f from this session) against the configured spend ceiling of $%.2f — agents.defaults.spend_ceiling_usd in %s.%s Only models in the pricing table count toward it. Every further turn is refused until the ceiling is raised or removed.",
		totalUSD, sessionUSD, ceiling, filepath.Join(a.cfg.HomeDir(), "config.json"), held)
}
