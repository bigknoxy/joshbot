package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/config"
)

// The interactive `joshbot configure` wizard. Everything here is driven through
// os.Stdin with withStdinInput, the same way the onboarding tests drive
// setupTelegram, because the wizard reads with fmt.Scanln directly.
//
// The behaviours worth pinning are the ones an operator cannot see went wrong:
// an empty answer must *keep* the existing value rather than blank it, an
// out-of-range menu choice must leave the config untouched rather than pick
// something, and the wizard must not spin forever on a stdin that will never
// produce another line.

// modelsServer stands in for a provider's /models endpoint so the wizard's
// closing credential check succeeds without reaching the network.
func modelsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"some/model"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A blank answer at every prompt keeps what was already configured. Treating
// "" as an answer would silently wipe a working API base or model.
func TestConfigureProviderEmptyAnswersKeepExistingValues(t *testing.T) {
	withTempHome(t)
	srv := modelsServer(t)
	withStdinInput(t, "\n\n\n")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"nvidia": {
			Enabled: true,
			APIKey:  "existing-key",
			APIBase: srv.URL,
			Model:   "existing/model",
		},
	}
	cfg.ProviderDefaults.Default = "nvidia"

	captureStdout(t, func() { cfg = configureProvider(cfg, "nvidia") })

	p := cfg.Providers["nvidia"]
	if p.APIKey != "existing-key" {
		t.Errorf("API key = %q, want the existing key kept", p.APIKey)
	}
	if p.APIBase != srv.URL {
		t.Errorf("API base = %q, want the existing base kept", p.APIBase)
	}
	if p.Model != "existing/model" {
		t.Errorf("model = %q, want the existing model kept", p.Model)
	}
	// nvidia is the default provider, so the agent model tracks it.
	if cfg.Agents.Defaults.Model != "existing/model" {
		t.Errorf("agent model = %q, want it to track the default provider's model",
			cfg.Agents.Defaults.Model)
	}
}

// Configuring the first provider makes it the default and pulls the agent's
// model from it. Without that, a fresh install ends up configured but with no
// model to talk to.
func TestConfigureProviderFirstProviderBecomesDefault(t *testing.T) {
	withTempHome(t)
	srv := modelsServer(t)
	// api key, api base, model. The base is a local stub so the credential
	// check at the end succeeds without touching the network.
	withStdinInput(t, "nv-key\n"+srv.URL+"\nsome/model\n")

	cfg := config.Defaults()
	cfg.Providers = nil
	cfg.ProviderDefaults.Default = ""

	captureStdout(t, func() { cfg = configureProvider(cfg, "nvidia") })

	p, ok := cfg.Providers["nvidia"]
	if !ok {
		t.Fatal("nvidia was not written into the provider map")
	}
	if !p.Enabled {
		t.Error("a configured provider must be enabled; without it the provider is silently inert")
	}
	if p.APIKey != "nv-key" {
		t.Errorf("API key = %q, want %q", p.APIKey, "nv-key")
	}
	if cfg.ProviderDefaults.Default != "nvidia" {
		t.Errorf("default provider = %q, want nvidia", cfg.ProviderDefaults.Default)
	}
	if cfg.Agents.Defaults.Model != "some/model" {
		t.Errorf("agent model = %q, want the model just chosen", cfg.Agents.Defaults.Model)
	}
}

// The Ollama branch: an unreachable daemon must not stop the operator
// configuring it by hand, and the timeout answer is read as seconds.
func TestConfigureProviderOllamaFallsBackToTypedModel(t *testing.T) {
	withTempHome(t)
	// API key (Ollama needs none, so it is left blank — a non-empty answer
	// would trigger the credential check), base URL on a port nothing listens
	// on, model name, timeout
	withStdinInput(t, "\nhttp://127.0.0.1:1\nllama3.2:3b\n45\n")

	cfg := config.Defaults()
	cfg.Providers = nil

	captureStdout(t, func() { cfg = configureProvider(cfg, "ollama") })

	p := cfg.Providers["ollama"]
	if p.Model != "llama3.2:3b" {
		t.Errorf("model = %q, want the hand-typed name", p.Model)
	}
	if p.Timeout != 45*time.Second {
		t.Errorf("timeout = %v, want 45s (the answer is in seconds)", p.Timeout)
	}
	if p.APIBase != "http://127.0.0.1:1" {
		t.Errorf("API base = %q, want the typed base", p.APIBase)
	}
}

// setDefaultProvider selects by 1-based menu index, and only over providers
// that actually carry a credential.
func TestSetDefaultProviderSelectsByIndex(t *testing.T) {
	withTempHome(t)

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"groq": {Enabled: true, APIKey: "gk", Model: "groq/model"},
	}
	cfg.ProviderDefaults.Default = ""

	withStdinInput(t, "1\n")
	captureStdout(t, func() { cfg = setDefaultProvider(cfg) })

	if cfg.ProviderDefaults.Default != "groq" {
		t.Fatalf("default provider = %q, want groq", cfg.ProviderDefaults.Default)
	}
	if cfg.Agents.Defaults.Model != "groq/model" {
		t.Errorf("agent model = %q, want the provider's own configured model",
			cfg.Agents.Defaults.Model)
	}
}

// An out-of-range choice leaves the config exactly as it was. Clamping or
// wrapping would set a default the operator never picked.
func TestSetDefaultProviderRejectsOutOfRangeChoice(t *testing.T) {
	withTempHome(t)

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"groq": {Enabled: true, APIKey: "gk"},
	}
	cfg.ProviderDefaults.Default = "openrouter"

	withStdinInput(t, "7\n")
	out := captureStdout(t, func() { cfg = setDefaultProvider(cfg) })

	if cfg.ProviderDefaults.Default != "openrouter" {
		t.Errorf("default provider = %q, want it untouched", cfg.ProviderDefaults.Default)
	}
	if !strings.Contains(out, "Invalid choice") {
		t.Errorf("expected the wizard to say the choice was invalid, got:\n%s", out)
	}
}

// With nothing configured there is nothing to make default, and the wizard
// says so instead of offering an empty menu.
func TestSetDefaultProviderWithNoCredentialsSaysSo(t *testing.T) {
	withTempHome(t)

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{"groq": {Enabled: true}}

	out := captureStdout(t, func() { cfg = setDefaultProvider(cfg) })

	if !strings.Contains(out, "No providers configured") {
		t.Errorf("expected a 'no providers configured' message, got:\n%s", out)
	}
	if cfg.ProviderDefaults.Default != "" {
		t.Errorf("default provider = %q, want it left empty", cfg.ProviderDefaults.Default)
	}
}

// A fallback order needs at least two credentialled providers; one is not a
// fallback chain and the wizard must not pretend otherwise.
func TestConfigureFallbackOrderNeedsTwoProviders(t *testing.T) {
	withTempHome(t)

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{"groq": {Enabled: true, APIKey: "gk"}}

	out := captureStdout(t, func() { cfg = configureFallbackOrder(cfg) })

	if !strings.Contains(out, "at least 2") {
		t.Errorf("expected the two-provider requirement to be stated, got:\n%s", out)
	}
	if cfg.ProviderDefaults.FallbackOrder != nil {
		t.Errorf("fallback order = %v, want it left unset", cfg.ProviderDefaults.FallbackOrder)
	}
}

// An empty answer clears the fallback order — that is the documented way to
// unset it, so it must not be confused with "keep what you had".
func TestConfigureFallbackOrderEmptyAnswerClearsIt(t *testing.T) {
	withTempHome(t)

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"groq":       {Enabled: true, APIKey: "gk"},
		"openrouter": {Enabled: true, APIKey: "or"},
	}
	cfg.ProviderDefaults.FallbackOrder = []string{"groq", "openrouter"}

	withStdinInput(t, "\n")
	out := captureStdout(t, func() { cfg = configureFallbackOrder(cfg) })

	if cfg.ProviderDefaults.FallbackOrder != nil {
		t.Errorf("fallback order = %v, want it cleared", cfg.ProviderDefaults.FallbackOrder)
	}
	if !strings.Contains(out, "cleared") {
		t.Errorf("expected the wizard to report the order was cleared, got:\n%s", out)
	}
}

// A comma list is parsed into provider names by 1-based index; entries out of
// range are dropped rather than panicking or indexing past the slice.
func TestConfigureFallbackOrderParsesCommaList(t *testing.T) {
	withTempHome(t)

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"groq":       {Enabled: true, APIKey: "gk"},
		"openrouter": {Enabled: true, APIKey: "or"},
	}

	withStdinInput(t, "2,1,9\n")
	captureStdout(t, func() { cfg = configureFallbackOrder(cfg) })

	order := cfg.ProviderDefaults.FallbackOrder
	if len(order) != 2 {
		t.Fatalf("fallback order = %v, want two entries (the out-of-range 9 dropped)", order)
	}
	if order[0] == order[1] {
		t.Errorf("fallback order = %v, want two distinct providers", order)
	}
}

// An order made entirely of nonsense keeps the current one rather than
// clearing it — clearing is what an *empty* answer means.
func TestConfigureFallbackOrderInvalidKeepsCurrent(t *testing.T) {
	withTempHome(t)

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"groq":       {Enabled: true, APIKey: "gk"},
		"openrouter": {Enabled: true, APIKey: "or"},
	}
	cfg.ProviderDefaults.FallbackOrder = []string{"groq"}

	withStdinInput(t, "99,abc\n")
	out := captureStdout(t, func() { cfg = configureFallbackOrder(cfg) })

	if len(cfg.ProviderDefaults.FallbackOrder) != 1 || cfg.ProviderDefaults.FallbackOrder[0] != "groq" {
		t.Errorf("fallback order = %v, want the previous order kept", cfg.ProviderDefaults.FallbackOrder)
	}
	if !strings.Contains(out, "Invalid order") {
		t.Errorf("expected an 'invalid order' message, got:\n%s", out)
	}
}

// Closed stdin must save and exit. The loop reads with fmt.Scanln, which
// leaves the answer empty at EOF; if empty were treated as an invalid choice
// the wizard would spin printing "Invalid choice" forever against a stdin that
// will never produce one.
func TestConfigureWizardEOFSavesAndExits(t *testing.T) {
	withTempHome(t)
	withStdinInput(t, "")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"groq": {Enabled: true, APIKey: "gk"},
	}

	done := make(chan error, 1)
	captureStdout(t, func() {
		go func() { done <- runConfigureWizard(cfg) }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runConfigureWizard: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("runConfigureWizard did not return on closed stdin; it is spinning on the menu")
		}
	})
	if t.Failed() {
		t.FailNow()
	}
	// The saved config must be readable back through the normal loader.
	saved, err := config.LoadStrict()
	if err != nil {
		t.Fatalf("LoadStrict after wizard save: %v", err)
	}
	if _, ok := saved.Providers["groq"]; !ok {
		t.Errorf("wizard did not persist the provider map; got %v", saved.Providers)
	}

}

// An unrecognised menu choice is reported and the wizard loops rather than
// exiting or acting on it.
func TestConfigureWizardInvalidChoiceLoops(t *testing.T) {
	withTempHome(t)
	withStdinInput(t, "42\n9\n")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"groq": {Enabled: true, APIKey: "gk"},
	}

	var err error
	out := captureStdout(t, func() { err = runConfigureWizard(cfg) })
	if err != nil {
		t.Fatalf("runConfigureWizard: %v", err)
	}
	if !strings.Contains(out, "Invalid choice") {
		t.Errorf("expected an 'invalid choice' message, got:\n%s", out)
	}
	if !strings.Contains(out, "Configuration saved") {
		t.Errorf("expected the wizard to save on choice 9, got:\n%s", out)
	}
}
