package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestDurationReadsEveryFormAConfigCanCarry is the whole point of the type. The
// bug it fixes was silent: `"timeout": 600` meant 600 nanoseconds, so every
// request died instantly with "context deadline exceeded" and blamed the
// provider (#240). Each case below is a config someone actually writes or that
// an older joshbot actually wrote.
func TestDurationReadsEveryFormAConfigCanCarry(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want time.Duration
	}{
		"duration string seconds":  {`"600s"`, 600 * time.Second},
		"duration string minutes":  {`"10m"`, 10 * time.Minute},
		"duration string compound": {`"1h30m"`, 90 * time.Minute},
		// The case that was broken: a human writes 600 meaning ten minutes.
		"bare number is seconds": {`600`, 600 * time.Second},
		"bare one is one second": {`1`, time.Second},
		// The case that must not break: joshbot configure wrote p.Timeout and
		// saved, so configs in the wild carry nanoseconds.
		"legacy nanoseconds": {`900000000000`, 900 * time.Second},
		"legacy one second":  {`1000000000`, time.Second},
		"zero stays unset":   {`0`, 0},
		"null stays unset":   {`null`, 0},
	} {
		t.Run(name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(tc.in), &d); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.in, err)
			}
			if d.Duration() != tc.want {
				t.Fatalf("%s parsed to %v, want %v", tc.in, d.Duration(), tc.want)
			}
		})
	}
}

// TestDurationRejectsWhatItCannotRead pins that a typo is an error naming the
// accepted forms, not a zero value that silently falls back to a default. A
// timeout that quietly becomes "unset" is the same class of failure as one that
// quietly becomes 600ns.
func TestDurationRejectsWhatItCannotRead(t *testing.T) {
	for name, in := range map[string]string{
		"unparseable string": `"ten minutes"`,
		"bare unit-less s":   `"600 s"`,
		"fractional number":  `600.5`,
		"object":             `{"seconds":600}`,
		"bool":               `true`,
	} {
		t.Run(name, func(t *testing.T) {
			var d Duration
			if err := json.Unmarshal([]byte(in), &d); err == nil {
				t.Fatalf("%s was accepted as %v", in, d.Duration())
			}
		})
	}
}

// TestDurationRoundTripsAsAString covers the other half: joshbot configure saves
// the config it just edited, and what it writes must read back as the same
// duration. Marshalling as a bare int64 is what put nanoseconds in people's
// config files in the first place.
func TestDurationRoundTripsAsAString(t *testing.T) {
	orig := Duration(900 * time.Second)
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `"15m0s"` {
		t.Fatalf("marshalled to %s, want a duration string", got)
	}
	var back Duration
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	if back != orig {
		t.Fatalf("round trip gave %v, want %v", back.Duration(), orig.Duration())
	}
}

// TestProviderTimeoutSurvivesAWholeConfig goes through the struct rather than
// the type, because the field could be correct and still be declared as a bare
// time.Duration somewhere. This is the assertion that fails if someone changes
// the field type back.
func TestProviderTimeoutSurvivesAWholeConfig(t *testing.T) {
	var cfg Config
	raw := `{"providers":{"ollama":{"enabled":true,"timeout":600}},
	         "agents":{"defaults":{"timeout":"10m"}}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := cfg.Providers["ollama"].Timeout.Duration(); got != 600*time.Second {
		t.Fatalf("provider timeout = %v, want 10m", got)
	}
	if got := cfg.Agents.Defaults.Timeout.Duration(); got != 10*time.Minute {
		t.Fatalf("agent timeout = %v, want 10m", got)
	}
}

// TestValidateRejectsASubSecondTimeout is the backstop for a unit mistake the
// parser cannot catch: "500ms" is a valid duration string and a nonsensical
// timeout. Rejecting it at load names the key; accepting it fails at the first
// request and names the context.
func TestValidateRejectsASubSecondTimeout(t *testing.T) {
	base := func() *Config {
		c := Defaults()
		return c
	}

	t.Run("provider", func(t *testing.T) {
		c := base()
		c.Providers = map[string]ProviderConfig{
			"ollama": {Enabled: true, Timeout: Duration(500 * time.Millisecond)},
		}
		err := c.Validate()
		if err == nil {
			t.Fatal("a 500ms provider timeout was accepted")
		}
		if !strings.Contains(err.Error(), "providers.ollama.timeout") {
			t.Fatalf("error does not name the key: %v", err)
		}
	})

	t.Run("agent", func(t *testing.T) {
		c := base()
		c.Agents.Defaults.Timeout = Duration(time.Millisecond)
		err := c.Validate()
		if err == nil {
			t.Fatal("a 1ms agent timeout was accepted")
		}
		if !strings.Contains(err.Error(), "agents.defaults.timeout") {
			t.Fatalf("error does not name the key: %v", err)
		}
	})

	t.Run("zero is unset, not invalid", func(t *testing.T) {
		c := base()
		c.Agents.Defaults.Timeout = 0
		c.Providers = map[string]ProviderConfig{"ollama": {Enabled: true}}
		if err := c.Validate(); err != nil {
			t.Fatalf("an unset timeout was rejected: %v", err)
		}
	})
}

// TestDefaultsCarryNoTimeout pins that this key needs no schema migration. A
// Config bool with no omitempty is written into every saved config, which is
// why flipping the streaming default cost a v4→v5 migration. A zero Duration
// with omitempty is absent from the file, so an existing config keeps
// agent.DefaultTimeout by having nothing to say about it.
func TestDefaultsCarryNoTimeout(t *testing.T) {
	b, err := json.Marshal(Defaults())
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("decode defaults: %v", err)
	}
	agents, _ := generic["agents"].(map[string]any)
	defaults, _ := agents["defaults"].(map[string]any)
	if _, present := defaults["timeout"]; present {
		t.Fatalf("defaults serialized a timeout key, which would pin the default into every saved config: %s", b)
	}
}
