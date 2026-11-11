package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A negative ceiling is a config typo, not a budget. It must be fatal, for
// the same reason validateTimeout is: an ordinary validation error makes Load
// log "Config unusable, using defaults" and substitute Defaults(), silently
// dropping every provider key over it.
func TestValidateSpendCeilingNegativeIsFatal(t *testing.T) {
	cfg := Defaults()
	cfg.Agents.Defaults.SpendCeilingUSD = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("negative ceiling should fail validation")
	}
	var fatal fatalConfigError
	if !errors.As(err, &fatal) {
		t.Errorf("negative ceiling must be a fatalConfigError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "spend_ceiling_usd") {
		t.Errorf("error should name the key: %v", err)
	}
}

func TestValidateSpendCeilingZeroAndPositiveOK(t *testing.T) {
	for _, v := range []float64{0, 0.01, 50} {
		cfg := Defaults()
		cfg.Agents.Defaults.SpendCeilingUSD = v
		if err := cfg.Validate(); err != nil {
			if strings.Contains(err.Error(), "spend_ceiling_usd") {
				t.Errorf("ceiling %f should validate: %v", v, err)
			}
		}
	}
}

// A ceiling written into a saved config survives a round-trip unchanged, and
// an absent one (a config from before the key existed) unmarshals to zero =
// disabled with no migration.
func TestSpendCeilingRoundTrips(t *testing.T) {
	cfg := Defaults()
	cfg.Agents.Defaults.SpendCeilingUSD = 12.5
	data, err := serializeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"spend_ceiling_usd": 12.5`) && !strings.Contains(string(data), `"spend_ceiling_usd":12.5`) {
		t.Errorf("saved config should carry the ceiling key:\n%s", data)
	}
	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Agents.Defaults.SpendCeilingUSD != 12.5 {
		t.Errorf("round-tripped ceiling = %f, want 12.5", back.Agents.Defaults.SpendCeilingUSD)
	}
	var stripped Config
	if err := json.Unmarshal([]byte(`{}`), &stripped); err != nil {
		t.Fatal(err)
	}
	if stripped.Agents.Defaults.SpendCeilingUSD != 0 {
		t.Errorf("absent key should decode to zero (disabled), got %f", stripped.Agents.Defaults.SpendCeilingUSD)
	}
}
