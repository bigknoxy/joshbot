package tuning

import "time"

// Decision is the pure-Go rule's proposal for one evaluation of one tool.
type Decision string

const (
	// NoChange proposes leaving the tool's timeout bump exactly as it is.
	NoChange Decision = "no_change"
	// StepUp proposes raising the bump by one Step, clamped to MaxBump.
	StepUp Decision = "step_up"
	// StepDown proposes lowering the bump by one Step, floored at zero (the
	// base/configured value, with no adjustment at all).
	StepDown Decision = "step_down"
)

// RuleParams bounds and paces the step-up/step-down rule. Every field is
// operator-declared (config.TuningConfig) or a fixed constant this package
// chooses — never learned, never an LLM's judgment: the acceptance criterion
// for a self-tune must always be this arithmetic, per the explicit design
// requirement that a self-tune must never trust the model's own judgment.
type RuleParams struct {
	// Threshold is the failure rate (0..1) at or above which a step-up is
	// proposed, given enough samples.
	Threshold float64
	// MinSamples is the minimum number of events inside the window before
	// the rule will act at all. A single timeout out of one sample is not a
	// signal; Evaluate returns NoChange below this regardless of rate.
	MinSamples int
	// Step is how much one step-up or step-down changes the bump.
	Step time.Duration
	// MaxBump is the operator-declared ceiling from config.TuningConfig — a
	// step up never proposes a value above it, however long a failure
	// streak runs.
	MaxBump time.Duration
	// Cooldown is the minimum time between two tune events for the same
	// tool. This is the whole hysteresis mechanism: without it, a tool
	// whose failure rate straddles Threshold from turn to turn would step
	// up and down on every single evaluation.
	Cooldown time.Duration
}

// Evaluate proposes a decision for one tool given its current bump above the
// configured/default timeout, the tool's recent failure rate and sample
// count (from Tracker.Stats), and when it was last tuned.
//
// It is a pure function — no I/O, no clock read, no LLM call — precisely so
// it is trivially callable from a table-driven test and so the acceptance
// criterion for every tune is exactly this arithmetic, reproducible from its
// five inputs.
func Evaluate(p RuleParams, currentBump time.Duration, failureRate float64, sampleCount int, lastTuneAt, now time.Time) (Decision, time.Duration) {
	if sampleCount < p.MinSamples {
		return NoChange, currentBump
	}
	if !lastTuneAt.IsZero() && now.Sub(lastTuneAt) < p.Cooldown {
		return NoChange, currentBump
	}

	if failureRate >= p.Threshold {
		if currentBump >= p.MaxBump {
			return NoChange, currentBump
		}
		newBump := currentBump + p.Step
		if newBump > p.MaxBump {
			newBump = p.MaxBump
		}
		return StepUp, newBump
	}

	if currentBump > 0 {
		newBump := currentBump - p.Step
		if newBump < 0 {
			newBump = 0
		}
		return StepDown, newBump
	}

	return NoChange, currentBump
}
