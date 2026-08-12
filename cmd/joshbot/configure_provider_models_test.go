package main

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// configureProvider has one arm per provider, and each arm repeats the same
// model-selection block by hand. Copy-paste between arms is exactly how one of
// them ends up saving the default model over the operator's pick, or writing
// the wrong provider's default base — and neither is visible until a request
// 404s much later, naming a model the operator never chose.
//
// The nvidia and ollama arms are covered elsewhere. These pin the three arms
// that fetch a model list first: openrouter, groq and poolside.

// pickModelFromList drives configureProvider for a provider whose API base is
// already a local stub, so the list fetch succeeds without the network: the
// key prompt is left blank to keep the stored one, the base prompt blank to
// keep the stub, and "1" picks the first listed model.
func pickModelFromList(t *testing.T, provider, base string) config.ProviderConfig {
	t.Helper()
	withTempHome(t)
	withStdinInput(t, "\n\n1\n\n")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		provider: {Enabled: true, APIKey: "existing-key", APIBase: base, Model: "old/model"},
	}
	cfg.ProviderDefaults.Default = provider

	captureStdout(t, func() { cfg = configureProvider(cfg, provider) })
	return cfg.Providers[provider]
}

// The model picked out of the fetched list is the one saved. Falling through to
// the default here gives the operator a config they watched themselves choose
// against, with no error to explain it.
func TestConfigureProviderOpenRouterSavesTheModelPickedFromTheList(t *testing.T) {
	srv := modelsServer(t)
	p := pickModelFromList(t, "openrouter", srv.URL)

	if p.Model != "some/model" {
		t.Errorf("model = %q, want the model picked from the fetched list", p.Model)
	}
	if p.APIBase != srv.URL {
		t.Errorf("API base = %q, want the existing base kept", p.APIBase)
	}
	if p.APIKey != "existing-key" {
		t.Errorf("API key = %q, want the stored key kept", p.APIKey)
	}
}

func TestConfigureProviderGroqSavesTheModelPickedFromTheList(t *testing.T) {
	srv := modelsServer(t)
	p := pickModelFromList(t, "groq", srv.URL)

	if p.Model != "some/model" {
		t.Errorf("model = %q, want the model picked from the fetched list", p.Model)
	}
	if p.APIBase != srv.URL {
		t.Errorf("API base = %q, want the existing base kept", p.APIBase)
	}
}

func TestConfigureProviderPoolsideSavesTheModelPickedFromTheList(t *testing.T) {
	srv := modelsServer(t)
	p := pickModelFromList(t, "poolside", srv.URL)

	if p.Model != "some/model" {
		t.Errorf("model = %q, want the model picked from the fetched list", p.Model)
	}
	if p.APIBase != srv.URL {
		t.Errorf("API base = %q, want the existing base kept", p.APIBase)
	}
}

// When the list cannot be fetched the operator types the model by hand, and
// the typed name has to win. A provider that is merely unreachable at
// configure time must not cost the operator the model they asked for.
func TestConfigureProviderUnreachableListStillHonoursTheTypedModel(t *testing.T) {
	withTempHome(t)
	// key, base (a port nothing listens on), the model typed by hand, and y to
	// save past the credential warning the dead base produces.
	withStdinInput(t, "gsk-key\nhttp://127.0.0.1:1\nmeta/llama-3.3-70b\ny\n")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"groq": {Enabled: true, APIKey: "existing-key", APIBase: "http://127.0.0.1:1", Model: "old/model"},
	}
	cfg.ProviderDefaults.Default = "groq"

	out := captureStdout(t, func() { cfg = configureProvider(cfg, "groq") })

	if !strings.Contains(out, "Could not fetch models") {
		t.Errorf("output did not say the list fetch failed:\n%s", out)
	}
	if got := cfg.Providers["groq"].Model; got != "meta/llama-3.3-70b" {
		t.Errorf("model = %q, want the hand-typed name", got)
	}
	// The default provider's model is what the agent actually talks to.
	if cfg.Agents.Defaults.Model != "meta/llama-3.3-70b" {
		t.Errorf("agent model = %q, want it to track the typed model", cfg.Agents.Defaults.Model)
	}
}
