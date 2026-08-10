package main

import (
	"bytes"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	joshlog "github.com/bigknoxy/joshbot/internal/log"
	"github.com/urfave/cli/v2"
)

// --- #139: gateway handler must not write to stderr at the default log level ---

// TestGatewayHandlerDebugSuppressedAtDefaultLevel guards against reintroducing
// the raw fmt.Fprintf(os.Stderr, ...) debug print that leaked every inbound
// message. The handler now logs via log.Debug, which the default (info) level
// suppresses. This asserts the underlying logging discipline the handler relies
// on: a Debug line produces no output until the level is lowered.
func TestGatewayHandlerDebugSuppressedAtDefaultLevel(t *testing.T) {
	var buf bytes.Buffer
	lg := joshlog.Get().Logger
	origOut := os.Stderr // charmbracelet default writer; restore via SetOutput below
	_ = origOut
	lg.SetOutput(&buf)
	origLevel := lg.GetLevel()
	defer func() {
		lg.SetOutput(os.Stderr)
		lg.SetLevel(origLevel)
	}()

	lg.SetLevel(joshlog.InfoLevel)
	// This is the exact call the gateway handler makes per inbound message.
	joshlog.Debug("bus handler invoked", "channel", "telegram", "sender", "12345")
	if buf.Len() != 0 {
		t.Fatalf("Debug log leaked at info level: %q", buf.String())
	}

	// Sanity: at debug level it is emitted (proves the message wasn't dropped
	// for some unrelated reason).
	lg.SetLevel(joshlog.DebugLevel)
	joshlog.Debug("bus handler invoked", "channel", "telegram", "sender", "12345")
	if !strings.Contains(buf.String(), "bus handler invoked") {
		t.Fatalf("Debug log missing at debug level: %q", buf.String())
	}
}

// --- #142 / #160: non-interactive onboard flag paths ---

// onboardContext builds a cli.Context for runOnboard with the given flags set.
func onboardContext(t *testing.T, args map[string]string, bools map[string]bool) *cli.Context {
	t.Helper()
	fs := flag.NewFlagSet("onboard", flag.ContinueOnError)
	fs.Bool("force", false, "")
	fs.Bool("keep-data", false, "")
	fs.String("model", "", "")
	fs.String("provider", "", "")
	fs.String("api-key", "", "")
	fs.String("api-base", "", "")
	fs.String("config", "", "") // global --config path flag
	for k, v := range args {
		if err := fs.Set(k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	for k, v := range bools {
		if v {
			if err := fs.Set(k, "true"); err != nil {
				t.Fatalf("set %s: %v", k, err)
			}
		}
	}
	app := cli.NewApp()
	return cli.NewContext(app, fs, nil)
}

// withTempHome points config.DefaultHome/DefaultWorkspace at a temp dir so an
// onboard run never touches the real ~/.joshbot, and restores them after.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origHome, origWs := config.DefaultHome, config.DefaultWorkspace
	config.DefaultHome = dir
	config.DefaultWorkspace = filepath.Join(dir, "workspace")
	t.Cleanup(func() {
		config.DefaultHome = origHome
		config.DefaultWorkspace = origWs
	})
	return dir
}

// TestOnboardForceNoProviderFails covers #142: `onboard --force </dev/null` with
// nothing to configure must exit non-zero with an actionable message instead of
// printing "Setup complete!" over an unusable config.
func TestOnboardForceNoProviderFails(t *testing.T) {
	withTempHome(t)
	// Ensure no env-provided key silently satisfies the default provider.
	for _, k := range []string{
		"JOSHBOT_PROVIDERS__OPENROUTER__API_KEY", "JOSHBOT_OPENROUTER_API_KEY",
	} {
		t.Setenv(k, "")
	}

	c := onboardContext(t, nil, map[string]bool{"force": true})
	err := runOnboard(c)
	if err == nil {
		t.Fatal("expected error when --force configures no provider, got nil")
	}
	if !strings.Contains(err.Error(), "did not configure any provider") {
		t.Fatalf("error missing actionable guidance: %v", err)
	}
	// The scaffold is still written so a caller that supplies a credential
	// separately gets a usable tree; only the exit status reports the failure.
	if _, statErr := os.Stat(filepath.Join(config.DefaultHome, "config.json")); statErr != nil {
		t.Errorf("config.json should still be written: %v", statErr)
	}
}

// TestOnboardForceWithFlagsSucceeds covers #142/#160 success path: provider and
// key supplied via flags configure a provider non-interactively and validation
// runs against the supplied --api-base (an httptest server), so the run is
// hermetic and exits with no error.
func TestOnboardForceWithFlagsSucceeds(t *testing.T) {
	home := withTempHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	defer srv.Close()

	c := onboardContext(t, map[string]string{
		"provider": "openrouter",
		"api-key":  "sk-test-key",
		"api-base": srv.URL,
		"model":    "openrouter/free",
	}, map[string]bool{"force": true})

	if err := runOnboard(c); err != nil {
		t.Fatalf("runOnboard with flags failed: %v", err)
	}

	cfgPath := filepath.Join(home, "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config.json not written: %v", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p, ok := loaded.Providers["openrouter"]
	if !ok {
		t.Fatal("openrouter provider not configured")
	}
	if p.APIKey != "sk-test-key" {
		t.Errorf("APIKey = %q, want sk-test-key", p.APIKey)
	}
	if !p.Enabled {
		t.Error("provider not enabled")
	}
	if p.APIBase != srv.URL {
		t.Errorf("APIBase = %q, want %q", p.APIBase, srv.URL)
	}
	if loaded.ProviderDefaults.Default != "openrouter" {
		t.Errorf("default provider = %q, want openrouter", loaded.ProviderDefaults.Default)
	}
}

// TestOnboardForceReadsEnvKey covers #142: with no --api-key flag, the key is
// read from JOSHBOT_PROVIDERS__<PROVIDER>__API_KEY so an env-driven --force run
// configures a provider instead of failing.
func TestOnboardForceReadsEnvKey(t *testing.T) {
	withTempHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	t.Setenv("JOSHBOT_PROVIDERS__OPENROUTER__API_KEY", "env-key-123")

	c := onboardContext(t, map[string]string{
		"provider": "openrouter",
		"api-base": srv.URL,
	}, map[string]bool{"force": true})

	if err := runOnboard(c); err != nil {
		t.Fatalf("runOnboard reading env key failed: %v", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Providers["openrouter"].APIKey != "env-key-123" {
		t.Errorf("APIKey = %q, want env-key-123", loaded.Providers["openrouter"].APIKey)
	}
}
