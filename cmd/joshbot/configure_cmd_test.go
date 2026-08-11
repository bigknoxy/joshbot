package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/output"
)

// runConfigureCmd invokes `joshbot configure` the way the real app does —
// same flag set, same JSON error wrapper, exit codes intact — and returns its
// stdout and its error.
func runConfigureCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.PathFlag{Name: "config"},
			&cli.StringFlag{Name: "output", Value: string(output.Text)},
		},
		Action: withJSONErrors(runConfigure),
		Writer: io.Discard,
		// urfave/cli calls os.Exit on a cli.ExitCoder by default, which would
		// take the test binary down with it. The error is still returned.
		ExitErrHandler: func(*cli.Context, error) {},
	}
	// The subcommand's own flags live on the root action here because
	// runConfigure reads them off the context it is handed.
	app.Flags = append(app.Flags,
		&cli.BoolFlag{Name: "list"},
		&cli.StringFlag{Name: "provider"},
		&cli.StringFlag{Name: "api-key"},
		&cli.StringFlag{Name: "api-base"},
		&cli.StringFlag{Name: "model"},
		&cli.StringFlag{Name: "set-default"},
		&cli.StringFlag{Name: "remove"},
	)
	var err error
	out := captureStdout(t, func() {
		err = app.Run(append([]string{"joshbot"}, args...))
	})
	return out, err
}

// readSavedConfig reads the config.json runConfigure wrote into the temp home.
func readSavedConfig(t *testing.T, home string) *config.Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("saved config is not valid JSON: %v\n%s", err, data)
	}
	return &cfg
}

// `configure --provider X --api-key K` is the scripted setup path. It must
// persist the credential AND report the model that will actually be used:
// with --model omitted the provider entry is left empty, and printing that
// empty field made a working setup read as `configured with model ""`.
func TestConfigureProviderPersistsAndReportsEffectiveModel(t *testing.T) {
	home := withTempHome(t)

	out, err := runConfigureCmd(t, "--provider", "openrouter", "--api-key", "sk-test-key-0123456789")
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	if !strings.Contains(out, "Configuration saved.") {
		t.Errorf("configure did not report saving:\n%s", out)
	}
	if strings.Contains(out, `model ""`) {
		t.Errorf("configure reported an empty effective model:\n%s", out)
	}

	cfg := readSavedConfig(t, home)
	p, ok := cfg.Providers["openrouter"]
	if !ok {
		t.Fatalf("openrouter was not written to config.json: %+v", cfg.Providers)
	}
	if p.APIKey != "sk-test-key-0123456789" {
		t.Errorf("api_key = %q, want the key that was passed", p.APIKey)
	}
	// A provider without "enabled": true is silently inert at runtime.
	if !p.Enabled {
		t.Error(`configured provider was written without "enabled": true`)
	}
	if cfg.Agents.Defaults.Model == "" {
		t.Error("no default model was recorded, so the agent has nothing to dial")
	}
	if !strings.Contains(out, cfg.Agents.Defaults.Model) {
		t.Errorf("stdout does not name the model that was saved (%q):\n%s",
			cfg.Agents.Defaults.Model, out)
	}
}

// Configuring a brand-new provider with no key must fail loudly and write
// nothing: "Configuration saved." over a provider with no credential is the
// exits-0-over-a-failed-state class this package keeps shipping.
func TestConfigureFirstTimeWithoutAPIKeyWritesNothing(t *testing.T) {
	home := withTempHome(t)

	out, err := runConfigureCmd(t, "--provider", "groq")
	if err == nil {
		t.Fatal("expected an error configuring a new provider with no API key")
	}
	if !strings.Contains(err.Error(), "API key is required") {
		t.Errorf("error does not tell the operator what is missing: %v", err)
	}
	if strings.Contains(out, "Configuration saved.") {
		t.Errorf("a failed configure reported success:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(home, "config.json")); statErr == nil {
		t.Error("a rejected provider must not be written to config.json")
	}
}

// `--set-default` naming a provider that was never configured must fail
// rather than pointing the agent's default at a provider with no credential.
func TestConfigureSetDefaultUnknownProviderFails(t *testing.T) {
	home := withTempHome(t)

	out, err := runConfigureCmd(t, "--set-default", "groq")
	if err == nil {
		t.Fatal("expected an error setting an unconfigured provider as default")
	}
	if !strings.Contains(err.Error(), "groq") {
		t.Errorf("error does not name the provider: %v", err)
	}
	if strings.Contains(out, "Configuration saved.") {
		t.Errorf("a rejected --set-default reported saving:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(home, "config.json")); statErr == nil {
		t.Error("a rejected --set-default must not write a config")
	}
}

// `--remove` must persist. Deleting the provider from the in-memory config and
// returning without saving would report "Removed provider" while the next run
// still dialled it.
func TestConfigureRemovePersistsAndMovesTheDefault(t *testing.T) {
	home := withTempHome(t)

	if _, err := runConfigureCmd(t, "--provider", "openrouter", "--api-key", "sk-a-0123456789"); err != nil {
		t.Fatalf("seed openrouter: %v", err)
	}
	if _, err := runConfigureCmd(t, "--provider", "groq", "--api-key", "sk-b-0123456789"); err != nil {
		t.Fatalf("seed groq: %v", err)
	}
	before := readSavedConfig(t, home)
	if before.ProviderDefaults.Default != "openrouter" {
		t.Fatalf("precondition: default = %q, want openrouter", before.ProviderDefaults.Default)
	}

	out, err := runConfigureCmd(t, "--remove", "openrouter")
	if err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	if !strings.Contains(out, "Configuration saved.") {
		t.Errorf("remove did not report saving:\n%s", out)
	}

	after := readSavedConfig(t, home)
	if _, still := after.Providers["openrouter"]; still {
		t.Error("removed provider is still in the saved config")
	}
	// A dangling default names a provider that no longer exists.
	if after.ProviderDefaults.Default != "groq" {
		t.Errorf("default = %q, want it moved to the surviving provider", after.ProviderDefaults.Default)
	}
}

// Removing something that was never configured must fail rather than report a
// removal that did not happen.
func TestConfigureRemoveUnknownProviderFails(t *testing.T) {
	withTempHome(t)

	out, err := runConfigureCmd(t, "--remove", "notaprovider")
	if err == nil {
		t.Fatal("expected an error removing an unconfigured provider")
	}
	if !strings.Contains(err.Error(), "notaprovider") {
		t.Errorf("error does not name the provider: %v", err)
	}
	if strings.Contains(out, "Removed provider") {
		t.Errorf("a failed remove claimed success:\n%s", out)
	}
}

// `configure --list --output json` is a script surface: the field names, the
// status vocabulary and the default marker are the contract. A renamed status
// string or a dropped provider silently breaks every caller.
func TestConfigureListJSONSchema(t *testing.T) {
	home := withTempHome(t)
	// copilotAuthenticated reads $HOME; keep it off the developer's real token.
	t.Setenv("HOME", home)

	if _, err := runConfigureCmd(t, "--provider", "openrouter", "--api-key", "sk-a-0123456789"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := runConfigureCmd(t, "--list", "--output", "json")
	if err != nil {
		t.Fatalf("configure --list: %v", err)
	}

	var doc output.Providers
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("--output json did not produce a document: %v\n%s", err, out)
	}
	if doc.SchemaVersion != output.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, output.SchemaVersion)
	}
	if doc.Default != "openrouter" {
		t.Errorf("default = %q, want openrouter", doc.Default)
	}

	want := []string{"nvidia", "openrouter", "groq", "ollama", "github-copilot", "poolside"}
	got := make(map[string]output.ConfiguredProvider, len(doc.Providers))
	for _, p := range doc.Providers {
		got[p.Name] = p
	}
	for _, name := range want {
		p, ok := got[name]
		if !ok {
			t.Errorf("provider %q missing from the document", name)
			continue
		}
		switch p.Status {
		case output.ProviderConfigured, output.ProviderNotConfigured,
			output.ProviderAuthenticated, output.ProviderOAuthRequired:
		default:
			t.Errorf("provider %q has status %q, outside the documented vocabulary", name, p.Status)
		}
	}
	if got["openrouter"].Status != output.ProviderConfigured {
		t.Errorf("openrouter status = %q, want %q", got["openrouter"].Status, output.ProviderConfigured)
	}
	if !got["openrouter"].IsDefault {
		t.Error("openrouter is the default but is_default is false")
	}
	if got["groq"].Status != output.ProviderNotConfigured {
		t.Errorf("groq status = %q, want %q", got["groq"].Status, output.ProviderNotConfigured)
	}
	if got["groq"].IsDefault {
		t.Error("an unconfigured provider is marked as the default")
	}
	// The credential must not survive the trip into a document made to be
	// pasted into an issue.
	if strings.Contains(out, "sk-a-0123456789") {
		t.Errorf("--list leaked the API key:\n%s", out)
	}
}

// A typo in --output must be a validation failure (exit 2), distinguishable
// from a command that ran and found a problem.
func TestConfigureListInvalidOutputExitsValidation(t *testing.T) {
	withTempHome(t)

	_, err := runConfigureCmd(t, "--list", "--output", "yaml")
	if err == nil {
		t.Fatal("an unknown --output value was accepted")
	}
	if got := exitCodeOf(t, err); got != exitValidation {
		t.Errorf("exit code = %d, want %d (exitValidation)", got, exitValidation)
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("error does not name the rejected value: %v", err)
	}
}

// An explicit --config must be the file that is written, not just the file
// that is read. loadConfig goes through config.LoadFrom for exactly this
// reason: an earlier version kept only the directory, discarded the file name
// and restored the global afterwards, so joshbot read one file and saved the
// operator's new credential into a different one.
func TestConfigureHonoursExplicitConfigPathOnWrite(t *testing.T) {
	home := withTempHome(t)
	chosen := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(chosen, []byte(`{}`), 0600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := runConfigureCmd(t, "--config", chosen,
		"--provider", "openrouter", "--api-key", "sk-explicit-0123456789"); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	data, err := os.ReadFile(chosen)
	if err != nil {
		t.Fatalf("read chosen config: %v", err)
	}
	if !strings.Contains(string(data), "sk-explicit-0123456789") {
		t.Errorf("--config file was not written to:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); err == nil {
		t.Error("configure wrote the default config path while --config named another file")
	}
}
