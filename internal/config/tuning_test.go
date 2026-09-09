package config

import (
	"strings"
	"testing"
	"time"
)

// TestValidateRejectsNonPositiveTuningMaxBumpWhenEnabled mirrors
// TestValidateRejectsASubSecondTimeout: an operator-declared bound of zero or
// less is never a choice, it is a config with tuning turned on and no ceiling
// on it -- which the design explicitly forbids ("never unbounded, even when
// enabled").
func TestValidateRejectsNonPositiveTuningMaxBumpWhenEnabled(t *testing.T) {
	c := Defaults()
	c.Tuning.Enabled = true
	c.Tuning.MaxBump = 0
	c.Tuning.Step = Duration(5 * time.Second)

	err := c.Validate()
	if err == nil {
		t.Fatal("a zero tuning.max_bump with tuning enabled was accepted")
	}
	if !strings.Contains(err.Error(), "tuning.max_bump") {
		t.Fatalf("error does not name tuning.max_bump: %v", err)
	}
}

// TestValidateRejectsTuningStepAboveMaxBumpWhenEnabled: a step larger than
// the max bump can never be taken without immediately busting the ceiling, so
// it is rejected at load and names both keys involved.
func TestValidateRejectsTuningStepAboveMaxBumpWhenEnabled(t *testing.T) {
	c := Defaults()
	c.Tuning.Enabled = true
	c.Tuning.MaxBump = Duration(30 * time.Second)
	c.Tuning.Step = Duration(60 * time.Second)

	err := c.Validate()
	if err == nil {
		t.Fatal("tuning.step > tuning.max_bump with tuning enabled was accepted")
	}
	if !strings.Contains(err.Error(), "tuning.max_bump") || !strings.Contains(err.Error(), "tuning.step") {
		t.Fatalf("error does not name both tuning.max_bump and tuning.step: %v", err)
	}
}

// TestValidateIgnoresTuningBoundsWhenDisabled: the bounds only matter once
// the feature can act on them. An operator who has never touched tuning, or
// has deliberately left it off with stale/inverted values sitting in an old
// config, must not be blocked at every startup by a knob doing nothing.
func TestValidateIgnoresTuningBoundsWhenDisabled(t *testing.T) {
	c := Defaults()
	c.Tuning.Enabled = false
	c.Tuning.MaxBump = 0
	c.Tuning.Step = Duration(999 * time.Second) // inverted: step > max_bump, but disabled

	if err := c.Validate(); err != nil {
		t.Fatalf("disabled tuning with inverted bounds was rejected: %v", err)
	}
}

// TestTuningDefaultsAreDisabledButUsable pins the STT-style pattern this
// mirrors: the feature itself is off by default (Enabled's zero value), but
// MaxBump/Step ship non-zero so flipping tuning.enabled on with no other
// config produces sane, bounded behaviour rather than a step of zero.
func TestTuningDefaultsAreDisabledButUsable(t *testing.T) {
	c := Defaults()
	if c.Tuning.Enabled {
		t.Fatal("tuning must be disabled by default")
	}
	if c.Tuning.MaxBump.Duration() <= 0 {
		t.Fatalf("tuning.max_bump default must be positive, got %v", c.Tuning.MaxBump.Duration())
	}
	if c.Tuning.Step.Duration() <= 0 {
		t.Fatalf("tuning.step default must be positive, got %v", c.Tuning.Step.Duration())
	}
	if c.Tuning.Step.Duration() > c.Tuning.MaxBump.Duration() {
		t.Fatalf("tuning.step default (%v) exceeds tuning.max_bump default (%v)",
			c.Tuning.Step.Duration(), c.Tuning.MaxBump.Duration())
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults did not validate: %v", err)
	}
}
