package config

import (
	"strings"
	"testing"
)

// dream_mode is a string rather than a bool so the default can change later
// without a schema migration (bools have no omitempty and are always
// serialized). That makes a typo possible, so an unknown value has to be
// rejected: silently falling back to off means the operator gets no
// consolidation and no explanation.

func TestValidateRejectsAnUnknownDreamModeAndAcceptsEveryRealOne(t *testing.T) {
	base := func(mode string) *Config {
		c := Defaults()
		c.Agents.Defaults.DreamMode = mode
		return c
	}

	if got := Defaults().Agents.Defaults.DreamMode; got != "" {
		t.Fatalf("default dream_mode is %q; it must stay empty (off) so existing installs are unaffected", got)
	}

	for _, mode := range []string{"", DreamModeOff, DreamModeRecord, DreamModeFull} {
		if err := base(mode).Validate(); err != nil {
			t.Errorf("Validate() rejected dream_mode %q: %v", mode, err)
		}
	}

	err := base("record-only").Validate()
	if err == nil {
		t.Fatal("Validate() accepted dream_mode \"record-only\"")
	}
	if !strings.Contains(err.Error(), "dream_mode") {
		t.Errorf("the error does not name the key: %v", err)
	}
}

func TestDreamModeEnvOverride(t *testing.T) {
	t.Setenv("JOSHBOT_AGENTS__DEFAULTS__DREAM_MODE", "full")
	cfg := Defaults()
	applyEnvOverrides(cfg)
	if cfg.Agents.Defaults.DreamMode != DreamModeFull {
		t.Errorf("env override did not reach dream_mode: got %q", cfg.Agents.Defaults.DreamMode)
	}
}
