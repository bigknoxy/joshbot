package tuning

import (
	"testing"
	"time"
)

func testParams() RuleParams {
	return RuleParams{
		Threshold:  0.5,
		MinSamples: 4,
		Step:       5 * time.Second,
		MaxBump:    30 * time.Second,
		Cooldown:   10 * time.Minute,
	}
}

// TestRuleStepsUpExactlyAtThreshold: a failure rate exactly at the
// threshold, with enough samples and cooldown satisfied, proposes a step up.
func TestRuleStepsUpExactlyAtThreshold(t *testing.T) {
	p := testParams()
	now := time.Unix(100000, 0)
	decision, bump := Evaluate(p, 0, p.Threshold, p.MinSamples, time.Time{}, now)
	if decision != StepUp {
		t.Fatalf("decision = %v, want StepUp", decision)
	}
	if bump != p.Step {
		t.Fatalf("bump = %v, want %v", bump, p.Step)
	}
}

// TestRuleDoesNotStepUpOneBelowThreshold is the adjacent falsifier: a rate
// just under the threshold must never trigger a step up. With no bump
// currently active there is nothing to step down either, so the only
// correct decision is NoChange.
func TestRuleDoesNotStepUpOneBelowThreshold(t *testing.T) {
	p := testParams()
	now := time.Unix(100000, 0)
	rate := p.Threshold - 0.01
	decision, bump := Evaluate(p, 0, rate, p.MinSamples, time.Time{}, now)
	if decision == StepUp {
		t.Fatalf("decision = %v, a rate just under threshold must never step up", decision)
	}
	if decision != NoChange {
		t.Fatalf("decision = %v, want NoChange", decision)
	}
	if bump != 0 {
		t.Fatalf("bump changed to %v despite NoChange", bump)
	}
}

// TestRuleDoesNotActBelowMinSamples: a single bad sample is not a signal.
func TestRuleDoesNotActBelowMinSamples(t *testing.T) {
	p := testParams()
	now := time.Unix(100000, 0)
	decision, _ := Evaluate(p, 0, 1.0, p.MinSamples-1, time.Time{}, now)
	if decision != NoChange {
		t.Fatalf("decision = %v, want NoChange with too few samples", decision)
	}
}

// TestRuleStepsDownAfterSuccessStreak: once the failure rate over the window
// has fallen back under the threshold (a run of subsequent successes), the
// rule proposes stepping back down toward the base value.
func TestRuleStepsDownAfterSuccessStreak(t *testing.T) {
	p := testParams()
	now := time.Unix(100000, 0)
	lastTune := now.Add(-p.Cooldown - time.Second)
	decision, bump := Evaluate(p, 10*time.Second, 0.0, p.MinSamples, lastTune, now)
	if decision != StepDown {
		t.Fatalf("decision = %v, want StepDown", decision)
	}
	if bump != 5*time.Second {
		t.Fatalf("bump = %v, want %v (10s - one step)", bump, 5*time.Second)
	}
}

// TestRuleStepDownFloorsAtZero: stepping down must never propose a negative
// bump -- there is no timeout below the configured/default base.
func TestRuleStepDownFloorsAtZero(t *testing.T) {
	p := testParams()
	now := time.Unix(100000, 0)
	lastTune := now.Add(-p.Cooldown - time.Second)
	decision, bump := Evaluate(p, 3*time.Second, 0.0, p.MinSamples, lastTune, now)
	if decision != StepDown {
		t.Fatalf("decision = %v, want StepDown", decision)
	}
	if bump != 0 {
		t.Fatalf("bump = %v, want 0 (floored, not negative)", bump)
	}
}

// TestRuleNeverExceedsOperatorMaxBump: repeated step-ups, however long the
// failure streak runs, must never push the proposed bump past the
// operator-declared ceiling.
func TestRuleNeverExceedsOperatorMaxBump(t *testing.T) {
	p := testParams()
	bump := time.Duration(0)
	lastTune := time.Time{}
	now := time.Unix(100000, 0)

	for i := 0; i < 50; i++ {
		decision, newBump := Evaluate(p, bump, 1.0, p.MinSamples, lastTune, now)
		if decision == StepUp {
			bump = newBump
			lastTune = now
		}
		if bump > p.MaxBump {
			t.Fatalf("bump %v exceeded MaxBump %v after %d iterations", bump, p.MaxBump, i)
		}
		now = now.Add(p.Cooldown + time.Second)
	}
	if bump != p.MaxBump {
		t.Fatalf("bump = %v after sustained failures, want it to have reached MaxBump %v", bump, p.MaxBump)
	}
}

// TestRuleHysteresisPreventsThrashing is the oscillation-prevention proof the
// requirements explicitly demand: an alternating success/timeout sequence
// fed in on every simulated turn must not change the tuned value on every
// single turn. The cooldown is the mechanism that enforces this.
func TestRuleHysteresisPreventsThrashing(t *testing.T) {
	p := testParams()
	bump := time.Duration(0)
	lastTune := time.Time{}
	now := time.Unix(200000, 0)

	changes := 0
	const turns = 200
	// Advance "now" by much less than the cooldown each turn, and alternate
	// the failure rate every turn -- the worst case for thrashing.
	step := p.Cooldown / 5
	for i := 0; i < turns; i++ {
		rate := 0.0
		if i%2 == 0 {
			rate = 1.0
		}
		decision, newBump := Evaluate(p, bump, rate, p.MinSamples, lastTune, now)
		if decision != NoChange {
			changes++
			bump = newBump
			lastTune = now
		}
		now = now.Add(step)
	}

	if changes >= turns {
		t.Fatalf("value changed on every single turn (%d changes over %d turns) -- no hysteresis", changes, turns)
	}
	// With a cooldown 5x the per-turn advance, a change can only occur at
	// most once every 5 turns.
	maxExpectedChanges := turns/5 + 1
	if changes > maxExpectedChanges {
		t.Fatalf("value changed %d times over %d turns, want at most ~%d given the cooldown", changes, turns, maxExpectedChanges)
	}
}

// TestRuleCooldownBlocksAnImmediateSecondTune: two evaluations back to back
// (same "now") must not both act, even if both would otherwise qualify.
func TestRuleCooldownBlocksAnImmediateSecondTune(t *testing.T) {
	p := testParams()
	now := time.Unix(100000, 0)

	decision1, bump1 := Evaluate(p, 0, 1.0, p.MinSamples, time.Time{}, now)
	if decision1 != StepUp {
		t.Fatalf("first decision = %v, want StepUp", decision1)
	}

	decision2, bump2 := Evaluate(p, bump1, 1.0, p.MinSamples, now, now)
	if decision2 != NoChange {
		t.Fatalf("second decision (no time elapsed) = %v, want NoChange", decision2)
	}
	if bump2 != bump1 {
		t.Fatalf("bump changed despite cooldown blocking the tune: %v -> %v", bump1, bump2)
	}
}
