package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/config"
)

// configureProvider is one switch with a separately hand-written branch per
// provider. configure_wizard_test.go covers the nvidia branch, which is the
// only one that never calls providers.ListModels. These tests cover the
// branches that do — openrouter, groq and poolside — plus the credential
// rejection path that decides whether a key that just failed validation is
// written to disk.
//
// Two failure modes are silent. A provider entry written without
// `"enabled": true` is inert at runtime, so the wizard reports success and
// produces a joshbot that dials nothing. And an empty answer treated as an
// answer wipes the stored key, base or model of a working install — which is
// exactly what re-running the wizard to change one field looks like.

// emptyModelsServer answers /models with an empty list. Both
// providers.ListModels and validateProviderCredentials hit that path, so the
// wizard's model fetch succeeds-but-finds-nothing (falling through to the
// typed-model prompt, which keeps the stdin script deterministic) and the
// closing credential check passes.
func emptyModelsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// modelListingProviders are the branches that fetch a model list before
// prompting. They share the key / base / model prompt order.
var modelListingProviders = []string{"openrouter", "groq", "poolside"}

func TestConfigureProviderWizardWritesAnEnabledProviderAndSetsTheDefault(t *testing.T) {
	for _, provider := range modelListingProviders {
		t.Run(provider, func(t *testing.T) {
			withTempHome(t)
			base := emptyModelsServer(t).URL + "/v1"

			// key, api base, model. Validation succeeds against the fake, so
			// the "Save anyway?" branch is not reached.
			withStdinInput(t, "sk-wizard-key\n"+base+"\nwizard-model\n")

			cfg := config.Defaults()
			cfg.Providers = nil
			cfg.ProviderDefaults.Default = ""

			var got *config.Config
			captureStdout(t, func() { got = configureProvider(cfg, provider) })

			p, ok := got.Providers[provider]
			if !ok {
				t.Fatalf("%s was not written to the config: %+v", provider, got.Providers)
			}
			if !p.Enabled {
				t.Errorf(`%s was written without "enabled": true, so it is inert at runtime`, provider)
			}
			if p.APIKey != "sk-wizard-key" {
				t.Errorf("api_key = %q, want the key that was typed", p.APIKey)
			}
			if p.APIBase != base {
				t.Errorf("api_base = %q, want the base that was typed (%q)", p.APIBase, base)
			}
			if p.Model != "wizard-model" {
				t.Errorf("model = %q, want the model that was typed", p.Model)
			}
			// First provider configured becomes the default, and the agent's
			// model follows it — otherwise the wizard finishes with a
			// credential saved and nothing selected to use it.
			if got.ProviderDefaults.Default != provider {
				t.Errorf("default provider = %q, want %q", got.ProviderDefaults.Default, provider)
			}
			if got.Agents.Defaults.Model != "wizard-model" {
				t.Errorf("agents.defaults.model = %q, want the model just chosen", got.Agents.Defaults.Model)
			}
		})
	}
}

// Re-running the wizard and pressing Enter through every prompt must leave the
// stored credential, endpoint and model exactly as they were.
func TestConfigureProviderWizardEmptyAnswersKeepEveryExistingValue(t *testing.T) {
	for _, provider := range modelListingProviders {
		t.Run(provider, func(t *testing.T) {
			withTempHome(t)
			base := emptyModelsServer(t).URL + "/v1"

			withStdinInput(t, "\n\n\n")

			cfg := config.Defaults()
			cfg.Providers = map[string]config.ProviderConfig{
				provider: {Enabled: true, APIKey: "sk-existing", APIBase: base, Model: "existing-model"},
			}
			cfg.ProviderDefaults.Default = provider

			var got *config.Config
			captureStdout(t, func() { got = configureProvider(cfg, provider) })

			p := got.Providers[provider]
			if p.APIKey != "sk-existing" {
				t.Errorf("api_key = %q; pressing Enter must not clear the stored credential", p.APIKey)
			}
			if p.APIBase != base {
				t.Errorf("api_base = %q, want the existing %q", p.APIBase, base)
			}
			if p.Model != "existing-model" {
				t.Errorf("model = %q; pressing Enter must not clear the stored model", p.Model)
			}
			if !p.Enabled {
				t.Error(`re-running the wizard dropped "enabled": true`)
			}
		})
	}
}

// A credential the provider rejects must not be saved unless the operator
// explicitly confirms. Silently persisting a key that just failed validation
// is how an install ends up "configured" and unable to talk to anything.
func TestConfigureProviderWizardRejectedCredentialIsNotSavedWithoutConfirmation(t *testing.T) {
	withTempHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	// key, base, model, then "n" to the "Save anyway?" prompt.
	withStdinInput(t, "sk-bad\n"+srv.URL+"/v1\nsome-model\nn\n")

	cfg := config.Defaults()
	cfg.Providers = nil
	cfg.ProviderDefaults.Default = ""

	var got *config.Config
	out := captureStdout(t, func() { got = configureProvider(cfg, "openrouter") })

	if _, ok := got.Providers["openrouter"]; ok {
		t.Errorf("a rejected credential was saved anyway: %+v", got.Providers["openrouter"])
	}
	if got.ProviderDefaults.Default != "" {
		t.Errorf("default provider was set to %q despite the credential being rejected",
			got.ProviderDefaults.Default)
	}
	if !strings.Contains(out, "Save anyway") {
		t.Errorf("the operator was never asked to confirm:\n%s", out)
	}
}

// The other half of that contract: confirming must actually save, or an
// operator configuring a provider with no reachable /models endpoint could
// never finish the wizard.
func TestConfigureProviderWizardRejectedCredentialIsSavedWhenConfirmed(t *testing.T) {
	withTempHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	withStdinInput(t, "sk-bad\n"+srv.URL+"/v1\nsome-model\ny\n")

	cfg := config.Defaults()
	cfg.Providers = nil
	cfg.ProviderDefaults.Default = ""

	var got *config.Config
	captureStdout(t, func() { got = configureProvider(cfg, "openrouter") })

	p, ok := got.Providers["openrouter"]
	if !ok {
		t.Fatal("confirming 'Save anyway? y' did not save the provider")
	}
	if !p.Enabled || p.APIKey != "sk-bad" {
		t.Errorf("saved entry is wrong: %+v", p)
	}
}

// Re-running the wizard over an existing Ollama entry and pressing Enter must
// keep the endpoint, the model and — the one that bites CPU-only installs —
// the raised timeout. Resetting the timeout to the 300s default here would
// make every turn time out again with no visible cause.
//
// The base URL points at a port nothing listens on, so the model-list fetch
// fails and the wizard falls through to its typed-model prompt; that keeps the
// stdin script deterministic regardless of whether a real Ollama is running on
// the machine the tests run on.
func TestConfigureProviderWizardOllamaEmptyAnswersKeepTheExistingEntry(t *testing.T) {
	withTempHome(t)

	// API key (Ollama has none), base URL, model name, timeout — all Enter.
	withStdinInput(t, "\n\n\n\n")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {
			Enabled: true,
			APIBase: "http://127.0.0.1:1",
			Model:   "llama3.2:3b",
			Timeout: config.Duration(900 * time.Second),
		},
	}
	cfg.ProviderDefaults.Default = "ollama"

	var got *config.Config
	captureStdout(t, func() { got = configureProvider(cfg, "ollama") })

	p := got.Providers["ollama"]
	if p.APIBase != "http://127.0.0.1:1" {
		t.Errorf("api_base = %q; pressing Enter must keep the configured endpoint", p.APIBase)
	}
	if p.Model != "llama3.2:3b" {
		t.Errorf("model = %q; pressing Enter must keep the configured model", p.Model)
	}
	if p.Timeout.Duration() != 900*time.Second {
		t.Errorf("timeout = %v, want the configured 900s kept", p.Timeout)
	}
	if p.APIKey != "" {
		t.Errorf("ollama was given an API key (%q); it has no credential", p.APIKey)
	}
	if !p.Enabled {
		t.Error(`ollama was written without "enabled": true`)
	}
}
