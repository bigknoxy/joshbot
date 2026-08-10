package config

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/redact"
)

func loadFixture(t *testing.T, body string) *Config {
	t.Helper()
	writeConfigHome(t, body)
	// LoadStrict, not Load: Load replaces an invalid config with defaults, so
	// half of the fixtures here would be preflighted as a config nobody wrote.
	cfg, err := LoadStrict()
	if cfg == nil {
		t.Fatalf("LoadStrict returned no config: %v", err)
	}
	return cfg
}

func activeEntry(t *testing.T, r PreflightReport) PreflightEntry {
	t.Helper()
	for _, e := range r.Entries {
		if e.Role == "active" {
			return e
		}
	}
	t.Fatalf("report has no active entry: %+v", r)
	return PreflightEntry{}
}

// Load deliberately replaces an unusable config with defaults so a bad file
// cannot stop joshbot from starting. Preflight needs the opposite: reporting on
// the defaults would tell an operator their config is fine while the file they
// are staring at is the thing that is broken.
func TestLoadStrict_ReportsTheFileRatherThanSubstitutingDefaults(t *testing.T) {
	const broken = `{"models_config": {"agent": {"model": "openrouter/missing"},
	                  "models": [{"name": "openrouter/x", "model": "openrouter/x", "api_key": "k"}]}}`
	writeConfigHome(t, broken)

	strict, err := LoadStrict()
	if err == nil {
		t.Fatal("LoadStrict accepted a config whose active model is not defined")
	}
	if strict == nil {
		t.Fatal("LoadStrict returned no config, so preflight has nothing to describe")
	}
	if got := strict.ModelsConfig.Agent.Model; got != "openrouter/missing" {
		t.Errorf("active model = %q, want the file's own value", got)
	}

	// And Load's forgiving behaviour is unchanged — daemons keep starting.
	lenient, err := Load()
	if err != nil {
		t.Fatalf("Load stopped tolerating a broken config: %v", err)
	}
	if lenient.ModelsConfig.Agent.Model == "openrouter/missing" {
		t.Error("Load returned the invalid config instead of falling back to defaults")
	}
}

// The whole value of a preflight is that it agrees with what the agent will do.
// Poolside is the one provider whose prefix is part of the real model ID, so a
// second implementation of the prefix rules shows up here first: a stripped
// "laguna-s-2.1" is a 404 the operator would then chase in the wrong place.
func TestPreflight_ReportsTheModelIDActuallySent(t *testing.T) {
	cfg := loadFixture(t, `{
	  "models_config": {
	    "agent": {"model": "poolside/laguna-s-2.1"},
	    "models": [
	      {"name": "poolside/laguna-s-2.1", "model": "poolside/laguna-s-2.1", "api_key": "k"},
	      {"name": "openrouter/anthropic/claude-sonnet-4", "model": "openrouter/anthropic/claude-sonnet-4", "api_key": "k"}
	    ]
	  }
	}`)

	r := Preflight(cfg)
	if !r.OK() {
		t.Fatalf("preflight failed on a working config: %+v", r)
	}
	if got := activeEntry(t, r).ModelID; got != "poolside/laguna-s-2.1" {
		t.Errorf("ModelID = %q, want the prefix retained for poolside", got)
	}

	// And the ordinary case still strips.
	cfg.ModelsConfig.Agent.Model = "openrouter/anthropic/claude-sonnet-4"
	if got := activeEntry(t, Preflight(cfg)).ModelID; got != "anthropic/claude-sonnet-4" {
		t.Errorf("ModelID = %q, want the openrouter prefix stripped", got)
	}
}

// The report is meant to be pasted into an issue. A base URL can carry a
// credential in its path or query, so only the host is disclosed.
func TestPreflight_DisclosesTheHostNotTheURL(t *testing.T) {
	cfg := loadFixture(t, `{
	  "models_config": {
	    "agent": {"model": "custom/m"},
	    "models": [{"name": "custom/m", "model": "custom/m", "api_key": "k",
	                "api_base": "https://gw.example.com/v1?token=SUPERSECRET"}]
	  }
	}`)

	e := activeEntry(t, Preflight(cfg))
	if e.Endpoint != "gw.example.com" {
		t.Errorf("Endpoint = %q, want the host only", e.Endpoint)
	}
	if strings.Contains(e.Endpoint, "SUPERSECRET") {
		t.Errorf("Endpoint leaked a credential from the api_base: %q", e.Endpoint)
	}
}

// A credential must be reported by presence and source, never by value: this
// output goes to a terminal, into scrollback, and into issue reports.
func TestPreflight_NeverPrintsTheCredential(t *testing.T) {
	const secret = "sk-do-not-print-me"
	writeConfigHome(t, providerFixture(envField))
	t.Setenv(namedVar, secret)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	r := Preflight(cfg)
	e := activeEntry(t, r)
	if !e.HasCredential {
		t.Error("HasCredential = false for a provider whose api_key_env resolved")
	}
	if want := CredentialFromEnv(namedVar); e.CredentialSource != want {
		t.Errorf("CredentialSource = %q, want %q", e.CredentialSource, want)
	}
	for _, s := range []string{e.CredentialSource, e.Summary(), e.Detail, r.Detail} {
		if strings.Contains(s, secret) {
			t.Errorf("preflight output contains the credential: %q", s)
		}
	}
}

// Each failure gets its own classification, because "preflight failed" sends
// the operator to read the whole config while "not enabled" sends them to one
// line of it.
func TestPreflight_DistinguishesFailures(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		want    PreflightProblem
		detail  string
	}{
		{
			name: "provider configured but not enabled",
			fixture: `{"providers": {"openrouter": {"api_key": "k"}},
			           "provider_defaults": {"default": "openrouter"}}`,
			want:   ProblemNotEnabled,
			detail: `"enabled": true`,
		},
		{
			name: "provider enabled with no credential",
			fixture: `{"providers": {"openrouter": {"enabled": true}},
			           "provider_defaults": {"default": "openrouter"}}`,
			want:   ProblemNoCredential,
			detail: "JOSHBOT_PROVIDERS__OPENROUTER__API_KEY",
		},
		{
			name: "default names a provider with no entry",
			fixture: `{"providers": {"groq": {"enabled": true, "api_key": "k"}},
			           "provider_defaults": {"default": "nosuchprovider"}}`,
			want:   ProblemNoDefault,
			detail: "no entry under providers",
		},
		{
			name: "model-centric with no agent model",
			fixture: `{"models_config": {"models": [
			             {"name": "openrouter/x", "model": "openrouter/x", "api_key": "k"}]}}`,
			want:   ProblemNoDefault,
			detail: "models.agent.model",
		},
		{
			name: "active model is not defined",
			fixture: `{"models_config": {"agent": {"model": "openrouter/missing"},
			           "models": [{"name": "openrouter/x", "model": "openrouter/x", "api_key": "k"}]}}`,
			want:   ProblemUnresolvable,
			detail: "not defined",
		},
		{
			name: "active model is disabled",
			fixture: `{"models_config": {"agent": {"model": "openrouter/x"},
			           "models": [{"name": "openrouter/x", "model": "openrouter/x",
			                       "api_key": "k", "disabled": true}]}}`,
			want:   ProblemNotEnabled,
			detail: "disabled",
		},
		{
			name: "model has no credential",
			fixture: `{"models_config": {"agent": {"model": "openrouter/x"},
			           "models": [{"name": "openrouter/x", "model": "openrouter/x"}]}}`,
			want:   ProblemNoCredential,
			detail: "api_key_env",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Preflight(loadFixture(t, tc.fixture))
			if r.OK() {
				t.Fatalf("preflight reported OK on a broken config: %+v", r)
			}
			got, detail := r.FirstProblem()
			if got != tc.want {
				t.Errorf("problem = %q, want %q (detail %q)", got, tc.want, detail)
			}
			if !strings.Contains(detail, tc.detail) {
				t.Errorf("detail = %q, want it to mention %q", detail, tc.detail)
			}
		})
	}
}

// A working install must exit clean, or the command is noise that gets ignored
// exactly when it would have mattered.
func TestPreflight_OKOnWorkingConfigs(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		cfg := loadFixture(t, `{"providers": {"openrouter": {"enabled": true, "api_key": "k"}},
		                        "provider_defaults": {"default": "openrouter"}}`)
		if r := Preflight(cfg); !r.OK() {
			t.Errorf("not OK: %+v", r)
		}
	})

	t.Run("ollama needs no credential", func(t *testing.T) {
		cfg := loadFixture(t, `{"providers": {"ollama": {"enabled": true}},
		                        "provider_defaults": {"default": "ollama"}}`)
		r := Preflight(cfg)
		if !r.OK() {
			t.Errorf("a local ollama install was reported broken: %+v", r)
		}
		if got := activeEntry(t, r).Endpoint; got != "localhost:11434" {
			t.Errorf("Endpoint = %q, want the ollama default", got)
		}
	})

	t.Run("a broken fallback does not fail the report", func(t *testing.T) {
		cfg := loadFixture(t, `{
		  "models_config": {
		    "agent": {"model": "openrouter/x", "fallback": ["openrouter/broken"]},
		    "models": [{"name": "openrouter/x", "model": "openrouter/x", "api_key": "k"}]
		  }}`)
		r := Preflight(cfg)
		if !r.OK() {
			t.Errorf("an unusable fallback made the whole report fail: %+v", r)
		}
		if len(r.Entries) != 2 {
			t.Fatalf("want an entry per route, got %d: %+v", len(r.Entries), r.Entries)
		}
		if r.Entries[1].Problem == "" {
			t.Error("the broken fallback was reported as fine")
		}
	})
}

// FirstProblem reports the active route ahead of a fallback: a broken fallback
// is a warning, a broken active route is why the operator ran the command.
func TestPreflight_FirstProblemPrefersTheActiveRoute(t *testing.T) {
	cfg := loadFixture(t, `{
	  "models_config": {
	    "agent": {"model": "openrouter/active", "fallback": ["openrouter/fb"]},
	    "models": [
	      {"name": "openrouter/active", "model": "openrouter/active", "disabled": true, "api_key": "k"},
	      {"name": "openrouter/fb", "model": "openrouter/fb"}
	    ]
	  }}`)

	got, detail := Preflight(cfg).FirstProblem()
	if got != ProblemNotEnabled {
		t.Errorf("problem = %q, want the active route's problem (detail %q)", got, detail)
	}

	// Asserted against a hand-built report as well, because Preflight happens to
	// emit the active route first: an entry order that agrees with the intended
	// answer would let a FirstProblem that ignores Role pass anyway.
	r := PreflightReport{Entries: []PreflightEntry{
		{Name: "fb", Role: "fallback", Problem: ProblemNoCredential, Detail: "fallback detail"},
		{Name: "active", Role: "active", Problem: ProblemNotEnabled, Detail: "active detail"},
	}}
	if got, detail := r.FirstProblem(); got != ProblemNotEnabled || detail != "active detail" {
		t.Errorf("FirstProblem = %q/%q with the fallback listed first, want the active route", got, detail)
	}
}

// The command prints through internal/redact, so a credential source label that
// looks like an assignment is destroyed on its way to the terminal — the first
// version read "api_key in the config file" and printed "api_key [REDACTED] the
// config file", which looks like joshbot hiding the answer to the question that
// was asked.
func TestPreflight_SummarySurvivesRedaction(t *testing.T) {
	for _, source := range []string{
		CredentialFromFile,
		CredentialFromEnv(canonVar),
		CredentialMissing,
		"api_key on the model in the config file",
	} {
		e := PreflightEntry{Name: "m", Role: "active", CredentialSource: source}
		if got := redact.String(e.Summary()); got != e.Summary() {
			t.Errorf("redaction rewrote the summary for source %q:\n got  %s\n want %s",
				source, got, e.Summary())
		}
	}
}

// Preflight must not dial anything: it is the command an operator runs when
// something is already wrong, often on a machine with no network.
func TestPreflight_PerformsNoIO(t *testing.T) {
	cfg := loadFixture(t, `{"providers": {"openrouter": {"enabled": true, "api_key": "k"}},
	                        "provider_defaults": {"default": "openrouter"}}`)
	// A base URL that would hang or error if anything actually connected.
	p := cfg.Providers["openrouter"]
	p.APIBase = "https://127.0.0.1:1/v1"
	cfg.Providers["openrouter"] = p

	r := Preflight(cfg)
	if !r.OK() {
		t.Errorf("preflight consulted the endpoint instead of the config: %+v", r)
	}
	if got := activeEntry(t, r).Endpoint; got != "127.0.0.1:1" {
		t.Errorf("Endpoint = %q, want the configured host", got)
	}
}
