package config

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that survives a config file written by a human.
//
// A bare time.Duration is an int64 of nanoseconds, and encoding/json marshals
// and unmarshals it as exactly that. So `"timeout": 600` in config.json set a
// six-hundred *nanosecond* timeout, and every request to that provider failed
// instantly with "context deadline exceeded" — a symptom indistinguishable from
// a dead provider (#240). Nothing in joshbot's docs stated the unit, and no
// other numeric key in the config is in nanoseconds.
//
// This type accepts three forms and states which it read:
//
//   - a duration string — "600s", "10m", "1h30m" — the preferred form. This is
//     time.ParseDuration's grammar exactly. It is *not* the cron tool's
//     spelling: internal/cron.ParseDuration additionally accepts a "d" suffix,
//     and "1d" here is an error, not a day.
//   - a small bare number — seconds, the unit a caller writing 600 means
//   - a large bare number — nanoseconds, because that is what an older joshbot
//     *wrote*: `joshbot configure` set p.Timeout and saved, so configs in the
//     wild contain values like 900000000000
//
// legacyNanosecondCutoff splits the last two. Anything at or above it is read
// as nanoseconds. The cutoff is one second expressed in nanoseconds, which as a
// seconds value would be over thirty years — so no operator ever meant it, and
// every value an old joshbot wrote for a timeout of a second or more lands
// above it. The one genuinely ambiguous case is a sub-second legacy value,
// which Validate rejects anyway.
type Duration time.Duration

// legacyNanosecondCutoff is the bare-integer value at or above which a number
// is read as nanoseconds rather than seconds. See Duration.
const legacyNanosecondCutoff = int64(time.Second) // 1_000_000_000

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String renders the duration in the form this type prefers to read back.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON always writes the string form, so a config joshbot saves is a
// config a human can read and edit without knowing any of the above.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts a duration string or a bare number. See Duration for
// how a bare number's unit is decided.
func (d *Duration) UnmarshalJSON(b []byte) error {
	// A JSON null leaves the field at its zero value, which every caller
	// already treats as "unset". Empty input is not reachable through
	// encoding/json, but a hand-rolled caller reaching b[0] below would panic
	// rather than report anything.
	if len(b) == 0 || string(b) == "null" {
		return nil
	}

	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: use a form like \"600s\", \"10m\" or \"1h30m\"", s)
		}
		*d = Duration(parsed)
		return nil
	}

	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("invalid duration %s: expected a string like \"10m\" or a number of seconds", b)
	}
	i, err := n.Int64()
	if err != nil {
		return fmt.Errorf("invalid duration %s: expected a whole number of seconds", b)
	}

	neg := int64(1)
	if i < 0 {
		// math.MinInt64 negates to itself, so splitting the sign off would
		// carry the negative through and land on 0s with a nil error.
		if i == math.MinInt64 {
			return fmt.Errorf("invalid duration %s: out of range", b)
		}
		neg, i = -1, -i
	}
	if i >= legacyNanosecondCutoff {
		*d = Duration(neg * i)
		return nil
	}
	*d = Duration(time.Duration(neg*i) * time.Second)
	return nil
}

// parseDurationEnv reads a duration from an environment variable under exactly
// the rules the config file uses, by routing the raw text through
// UnmarshalJSON. A bare "600" therefore means 600 seconds here as well; env
// and file must not disagree about the unit, which is the whole bug (#240).
func parseDurationEnv(v string) (Duration, error) {
	v = strings.TrimSpace(v)
	// An env var carries no JSON quoting, so supply it: a bare integer is the
	// number form, anything else is the string form. json.Marshal escapes the
	// value, so a hostile string cannot break out into other JSON.
	b := []byte(v)
	if _, err := strconv.ParseInt(v, 10, 64); err != nil {
		if b, err = json.Marshal(v); err != nil {
			return 0, err
		}
	}
	var d Duration
	if err := d.UnmarshalJSON(b); err != nil {
		return 0, err
	}
	return d, nil
}

// YAML support is deliberately absent. The struct tags carry yaml names, but
// nothing in joshbot decodes a YAML config today, and a second unmarshal path
// is a second place for the unit to be wrong.

// minConfiguredTimeout is the floor for any timeout an operator sets. Below it
// the value is certainly a unit mistake rather than a choice: no provider and
// no agent turn completes in under a second.
const minConfiguredTimeout = time.Second

// validateTimeout rejects a nonzero timeout that is too small to be meant. Zero
// is left alone everywhere: it is how every caller spells "unset, use the
// default".
func validateTimeout(key string, d Duration) error {
	if d == 0 {
		return nil
	}
	if d.Duration() < minConfiguredTimeout {
		// fatalConfigError, because the alternative is worse than the mistake.
		// Load answers a plain validation failure by logging "Config unusable,
		// using defaults" and substituting Defaults() — so a mistyped
		// "500ms" would silently take every provider, API key and allowlist
		// with it and leave joshbot dialling the default model with no
		// credential. That is the same silent-degradation class this type
		// exists to fix, one layer up.
		return fatalConfigError{fmt.Errorf("%s is %s, which is below the %s minimum; write a duration string like \"600s\" or \"10m\", or a whole number of seconds",
			key, d, minConfiguredTimeout)}
	}
	return nil
}
