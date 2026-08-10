package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Preflight answers "would joshbot start, and with what?" without dialling
// anything.
//
// It is deliberately built on ResolveModelConfig and StripProviderPrefix rather
// than on a parallel reading of the config: a preflight that agrees with a
// broken reality is worthless, and the prefix rules in particular are exactly
// where a second implementation would drift (poolside keeps its prefix, every
// other provider has it stripped). Anything that changes routing changes this
// report by construction.
//
// Nothing here performs I/O, so it is safe to run on a machine with no network
// and no credentials in the environment.

// PreflightProblem classifies a failure so a caller can act on it without
// matching on message text.
type PreflightProblem string

const (
	// ProblemNoDefault means nothing is configured to answer a message.
	ProblemNoDefault PreflightProblem = "no-default-provider"
	// ProblemNotEnabled means a provider is configured but "enabled" is not
	// set — the silent-disable trap that reads as "joshbot ignored my config".
	ProblemNotEnabled PreflightProblem = "provider-not-enabled"
	// ProblemNoCredential means no api_key, api_key_env or environment
	// override supplied a credential.
	ProblemNoCredential PreflightProblem = "missing-credential"
	// ProblemUnresolvable covers a config the resolver itself rejected —
	// an unknown model name, a disabled active model, a missing api_base.
	ProblemUnresolvable PreflightProblem = "unresolvable"
)

// PreflightEntry is one resolved route: in model-centric configs a model, in
// legacy configs a provider.
type PreflightEntry struct {
	// Name is the model name, or the provider name in a legacy config.
	Name string `json:"name"`
	// Role is "active" or "fallback".
	Role string `json:"role"`
	// Provider is the provider the model routes to.
	Provider string `json:"provider"`
	// ModelID is the exact model ID sent on the wire, after prefix handling.
	ModelID string `json:"model_id"`
	// Endpoint is the API host. The host only, never the full URL: a base URL
	// can carry a credential in its path or query, and this output is meant to
	// be pasteable into an issue.
	Endpoint string `json:"endpoint"`
	// Enabled reports whether the entry would actually be registered.
	Enabled bool `json:"enabled"`
	// HasCredential reports presence, never the value.
	HasCredential bool `json:"has_credential"`
	// CredentialSource names where the credential came from — a variable name
	// or the config file. Never the credential.
	CredentialSource string `json:"credential_source"`
	// Problem is set when this entry would not work.
	Problem PreflightProblem `json:"problem,omitempty"`
	// Detail explains Problem in one line.
	Detail string `json:"detail,omitempty"`
}

// PreflightReport is the whole result. It is a struct rather than printed
// output so that rendering it as text or as JSON is a presentation choice.
type PreflightReport struct {
	ConfigPath   string           `json:"config_path"`
	ConfigFormat string           `json:"config_format"`
	Workspace    string           `json:"workspace"`
	Entries      []PreflightEntry `json:"entries"`
	// Problem and Detail describe a failure of the whole config, as opposed to
	// one route — principally having nothing at all to route to.
	Problem PreflightProblem `json:"problem,omitempty"`
	Detail  string           `json:"detail,omitempty"`
}

// OK reports whether joshbot would start and have at least one usable route.
func (r PreflightReport) OK() bool {
	if r.Problem != "" {
		return false
	}
	for _, e := range r.Entries {
		if e.Problem == "" {
			return true
		}
	}
	return false
}

// FirstProblem returns the problem a caller should report, preferring a
// whole-config failure over a per-entry one. Empty when the report is OK.
func (r PreflightReport) FirstProblem() (PreflightProblem, string) {
	if r.OK() {
		return "", ""
	}
	if r.Problem != "" {
		return r.Problem, r.Detail
	}
	// The active route first: a broken fallback is a warning, a broken active
	// route is why the operator ran this.
	for _, e := range r.Entries {
		if e.Role == "active" && e.Problem != "" {
			return e.Problem, e.Detail
		}
	}
	for _, e := range r.Entries {
		if e.Problem != "" {
			return e.Problem, e.Detail
		}
	}
	return "", ""
}

// endpointHost reduces a base URL to its host, which is all a preflight should
// disclose. A URL that will not parse is reported as-is rather than dropped:
// a malformed api_base is itself the thing the operator needs to see.
func endpointHost(apiBase string) string {
	if apiBase == "" {
		return ""
	}
	u, err := url.Parse(apiBase)
	if err != nil || u.Host == "" {
		return apiBase
	}
	return u.Host
}

// Preflight builds the report for cfg.
func Preflight(cfg *Config) PreflightReport {
	r := PreflightReport{
		ConfigPath: ConfigPath(),
		Workspace:  cfg.WorkspaceDir(),
	}
	if cfg.UseModelsConfig() {
		r.ConfigFormat = "model-centric"
		preflightModels(cfg, &r)
	} else {
		r.ConfigFormat = "legacy"
		preflightLegacy(cfg, &r)
	}
	return r
}

func preflightModels(cfg *Config, r *PreflightReport) {
	active := cfg.ModelsConfig.Agent.Model
	if active == "" {
		r.Problem = ProblemNoDefault
		r.Detail = "models.agent.model is not set, so no model would be used"
		return
	}

	names := []string{active}
	for _, f := range cfg.ModelsConfig.Agent.Fallback {
		if f != active {
			names = append(names, f)
		}
	}

	for i, name := range names {
		role := "fallback"
		if i == 0 {
			role = "active"
		}
		r.Entries = append(r.Entries, preflightModel(cfg, name, role))
	}
}

func preflightModel(cfg *Config, name, role string) PreflightEntry {
	e := PreflightEntry{Name: name, Role: role}

	model, ok := cfg.GetModel(name)
	if !ok {
		e.Problem = ProblemUnresolvable
		e.Detail = fmt.Sprintf("model %q is referenced but not defined under models.models", name)
		return e
	}
	e.Provider = DetectProvider(model.Model).Name
	// Reported even when resolution fails below, because the model ID and the
	// provider are usually what makes the failure obvious.
	e.ModelID = StripProviderPrefix(model.Model)
	e.CredentialSource = cfg.CredentialSource(e.Provider)
	e.Enabled = !model.Disabled

	resolved, err := cfg.ResolveModelConfig(name)
	if err != nil {
		if model.Disabled {
			e.Problem = ProblemNotEnabled
			e.Detail = fmt.Sprintf("model %q is disabled", name)
			return e
		}
		if model.APIKey == "" && e.Provider != "ollama" {
			e.Problem = ProblemNoCredential
			e.Detail = credentialHint(e.Provider, name)
			return e
		}
		e.Problem = ProblemUnresolvable
		e.Detail = err.Error()
		return e
	}

	e.Endpoint = endpointHost(resolved.APIBase)
	e.ModelID = resolved.ModelID
	e.Provider = resolved.Provider
	e.HasCredential = resolved.APIKey != ""
	if e.HasCredential && e.CredentialSource == CredentialMissing {
		// The key came from the model entry rather than a provider entry, so
		// the provider-keyed source lookup has nothing to say about it.
		e.CredentialSource = "api_key on the model in the config file"
	}
	return e
}

func preflightLegacy(cfg *Config, r *PreflightReport) {
	// Mirrors cmd/joshbot: an unset default falls back to openrouter rather
	// than failing, so preflight must agree or it reports a working install
	// as broken.
	names := []string{cfg.ProviderDefaults.Default}
	if names[0] == "" {
		names[0] = "openrouter"
	}
	for _, f := range cfg.ProviderDefaults.FallbackOrder {
		if f != names[0] {
			names = append(names, f)
		}
	}

	if len(cfg.Providers) == 0 {
		r.Problem = ProblemNoDefault
		r.Detail = "no providers are configured; run `joshbot configure` or `joshbot onboard`"
		return
	}

	for i, name := range names {
		role := "fallback"
		if i == 0 {
			role = "active"
		}
		r.Entries = append(r.Entries, preflightProvider(cfg, name, role))
	}
}

func preflightProvider(cfg *Config, name, role string) PreflightEntry {
	e := PreflightEntry{
		Name:             name,
		Role:             role,
		Provider:         name,
		CredentialSource: cfg.CredentialSource(name),
	}

	p, ok := cfg.Providers[name]
	if !ok {
		e.Problem = ProblemNoDefault
		e.Detail = fmt.Sprintf("provider %q is selected but has no entry under providers", name)
		return e
	}

	e.Enabled = p.Enabled
	e.HasCredential = p.APIKey != ""

	model := p.Model
	if model == "" {
		model = cfg.Agents.Defaults.Model
	}
	e.ModelID = StripProviderPrefix(model)

	apiBase := p.APIBase
	if apiBase == "" {
		apiBase = providerBaseURL(name)
	}
	e.Endpoint = endpointHost(apiBase)

	switch {
	case !p.Enabled:
		// Omitting "enabled" silently disables a provider, which is the single
		// most common way a joshbot config looks right and does nothing.
		e.Problem = ProblemNotEnabled
		e.Detail = fmt.Sprintf("provider %q is configured but not enabled; add \"enabled\": true", name)
	case !e.HasCredential && name != "ollama":
		e.Problem = ProblemNoCredential
		e.Detail = credentialHint(name, "")
	}
	return e
}

// providerBaseURL is the default endpoint for a provider named directly rather
// than via a model prefix.
func providerBaseURL(name string) string {
	return providerPrefixes[name+"/"].BaseURL
}

func credentialHint(provider, model string) string {
	subject := fmt.Sprintf("provider %q", provider)
	if model != "" {
		subject = fmt.Sprintf("model %q (provider %q)", model, provider)
	}
	return fmt.Sprintf("%s has no credential; set api_key, api_key_env, or %s",
		subject, providerEnvKey(provider))
}

// Summary is a one-line human description of an entry, with no credential in
// it. Kept next to the struct so the text and JSON renderings cannot disagree
// about what an entry means.
func (e PreflightEntry) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)", e.Name, e.Role)
	if e.Provider != "" && e.Provider != e.Name {
		fmt.Fprintf(&b, " → %s", e.Provider)
	}
	if e.ModelID != "" {
		fmt.Fprintf(&b, " model=%s", e.ModelID)
	}
	if e.Endpoint != "" {
		fmt.Fprintf(&b, " endpoint=%s", e.Endpoint)
	}
	fmt.Fprintf(&b, " credential=%s", e.CredentialSource)
	return b.String()
}
