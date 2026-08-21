package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/channels"
	"github.com/bigknoxy/joshbot/internal/config"
)

// validOnboardToken passes setupTelegram's offline format check; it is only
// ever handed to the stubbed validator, never sent over the wire.
const validOnboardToken = "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZabcde"

// withStdinInput redirects os.Stdin to a pipe carrying input, and restores it
// when the test ends. fmt.Scanln resolves os.Stdin on every call and reads one
// byte at a time, so a multi-prompt flow reads the lines back in order.
func withStdinInput(t *testing.T, input string) {
	t.Helper()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := pw.WriteString(input); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = pw.Close()

	prev := os.Stdin
	os.Stdin = pr
	t.Cleanup(func() {
		os.Stdin = prev
		_ = pr.Close()
	})
}

// stubTokenValidator replaces validateTelegramToken for the duration of a test
// and returns a counter of how many times it was called.
func stubTokenValidator(t *testing.T, fn func(token string) error) *int {
	t.Helper()

	prev := validateTelegramToken
	var calls int
	validateTelegramToken = func(token, apiURL string) error {
		calls++
		return fn(token)
	}
	t.Cleanup(func() { validateTelegramToken = prev })
	return &calls
}

func networkError() error {
	return fmt.Errorf("failed to connect to Telegram API: %w: dial tcp: timeout", channels.ErrTelegramNetwork)
}

func TestSetupTelegram_Fresh_SetupWithNewToken(t *testing.T) {
	withStdinInput(t, "1\n"+validOnboardToken+"\n@alice, bob\n")
	calls := stubTokenValidator(t, func(string) error { return nil })

	out := captureStdout(t, func() {
		cfg := setupTelegram(nil)
		if cfg == nil {
			t.Fatal("expected a Telegram config")
		}
		if !cfg.Enabled || cfg.Token != validOnboardToken {
			t.Errorf("got enabled=%v token=%q", cfg.Enabled, cfg.Token)
		}
		if len(cfg.AllowFrom) != 2 || cfg.AllowFrom[0] != "@alice" || cfg.AllowFrom[1] != "@bob" {
			t.Errorf("AllowFrom = %v, want [@alice @bob]", cfg.AllowFrom)
		}
	})

	if *calls != 1 {
		t.Errorf("validator called %d times, want 1", *calls)
	}
	if !strings.Contains(out, "Token validated successfully!") {
		t.Errorf("missing success message in output:\n%s", out)
	}
	if strings.Contains(out, validOnboardToken) {
		t.Errorf("output leaks the token:\n%s", out)
	}
}

func TestSetupTelegram_Fresh_Skip(t *testing.T) {
	withStdinInput(t, "2\n")

	if cfg := setupTelegram(nil); cfg != nil {
		t.Errorf("expected nil config for skip, got %+v", cfg)
	}
}

func TestSetupTelegram_Fresh_Cancel(t *testing.T) {
	withStdinInput(t, "1\ncancel\n")

	if cfg := setupTelegram(nil); cfg != nil {
		t.Errorf("expected nil config for cancel, got %+v", cfg)
	}
}

func TestSetupTelegram_Existing_Keep(t *testing.T) {
	existing := &config.Config{Channels: config.ChannelsConfig{Telegram: config.TelegramConfig{
		Enabled: true, Token: "old-token", AllowFrom: []string{"@old"},
	}}}
	withStdinInput(t, "1\n")
	calls := stubTokenValidator(t, func(string) error { return nil })

	cfg := setupTelegram(existing)
	if cfg == nil {
		t.Fatal("expected a Telegram config")
	}
	if !cfg.Enabled || cfg.Token != "old-token" {
		t.Errorf("keep did not preserve existing token: %+v", cfg)
	}
	if *calls != 0 {
		t.Errorf("keep path must not validate, got %d calls", *calls)
	}
}

func TestSetupTelegram_Existing_Disable(t *testing.T) {
	existing := &config.Config{Channels: config.ChannelsConfig{Telegram: config.TelegramConfig{
		Enabled: true, Token: "old-token", AllowFrom: []string{"@old"},
	}}}
	withStdinInput(t, "3\n")

	cfg := setupTelegram(existing)
	if cfg == nil {
		t.Fatal("expected a Telegram config")
	}
	if cfg.Enabled {
		t.Error("disable must clear Enabled")
	}
	if cfg.Token != "" || len(cfg.AllowFrom) != 0 {
		t.Errorf("disable must clear token/allow_from, got %+v", cfg)
	}
}

func TestSetupTelegram_Existing_ChangeToken(t *testing.T) {
	existing := &config.Config{Channels: config.ChannelsConfig{Telegram: config.TelegramConfig{
		Enabled: true, Token: "old-token", AllowFrom: []string{"@old"},
	}}}
	// Empty usernames line reuses the existing allow list.
	withStdinInput(t, "2\n"+validOnboardToken+"\n\n")
	calls := stubTokenValidator(t, func(string) error { return nil })

	cfg := setupTelegram(existing)
	if cfg == nil {
		t.Fatal("expected a Telegram config")
	}
	if cfg.Token != validOnboardToken {
		t.Errorf("got token %q, want %q", cfg.Token, validOnboardToken)
	}
	if len(cfg.AllowFrom) != 1 || cfg.AllowFrom[0] != "@old" {
		t.Errorf("AllowFrom = %v, want existing [@old]", cfg.AllowFrom)
	}
	if *calls != 1 {
		t.Errorf("validator called %d times, want 1", *calls)
	}
}

// The bug from the field: a working bot, a valid token, one transient network
// failure, and the old code returned nil — so the config was saved with
// Telegram disabled and the live bot went silent. A failed re-validation must
// preserve the existing working token instead.
func TestSetupTelegram_Existing_ChangeFails_KeepsExisting(t *testing.T) {
	existing := &config.Config{Channels: config.ChannelsConfig{Telegram: config.TelegramConfig{
		Enabled: true, Token: "old-token", AllowFrom: []string{"@old"},
	}}}
	withStdinInput(t, "2\n"+validOnboardToken+"\n"+validOnboardToken+"\n")
	calls := stubTokenValidator(t, func(string) error { return networkError() })

	out := captureStdout(t, func() {
		cfg := setupTelegram(existing)
		if cfg == nil {
			t.Fatal("expected a Telegram config")
		}
		if !cfg.Enabled || cfg.Token != "old-token" {
			t.Errorf("failed change must keep the existing token, got %+v", cfg)
		}
		if len(cfg.AllowFrom) != 1 || cfg.AllowFrom[0] != "@old" {
			t.Errorf("AllowFrom = %v, want existing [@old]", cfg.AllowFrom)
		}
	})

	if *calls != 2 {
		t.Errorf("validator called %d times, want 2 (two prompts)", *calls)
	}
	if !strings.Contains(out, "Keeping the existing Telegram configuration") {
		t.Errorf("missing keep-existing message:\n%s", out)
	}
	if !strings.Contains(out, "network problem") {
		t.Errorf("missing network guidance for a connectivity failure:\n%s", out)
	}
}

// On a fresh install with no token to fall back to, persistent failure must
// disable Telegram but tell the user why, and never leak the token.
func TestSetupTelegram_Fresh_ValidationFails_DisablesWithClearMessage(t *testing.T) {
	withStdinInput(t, "1\n"+validOnboardToken+"\n"+validOnboardToken+"\n")
	stubTokenValidator(t, func(string) error { return networkError() })

	out := captureStdout(t, func() {
		cfg := setupTelegram(nil)
		if cfg == nil {
			t.Fatal("expected a Telegram config")
		}
		if cfg.Enabled || cfg.Token != "" {
			t.Errorf("failed fresh setup must leave Telegram disabled, got %+v", cfg)
		}
	})

	if strings.Contains(out, validOnboardToken) {
		t.Errorf("output leaks the token:\n%s", out)
	}
	if !strings.Contains(out, "Telegram setup skipped") {
		t.Errorf("missing skip message:\n%s", out)
	}
}

// An invalid (rejected) token must carry the "check the token" guidance, not
// the network guidance, and after two prompts fall back to the existing setup.
func TestSetupTelegram_RejectedToken_SaysCheckToken(t *testing.T) {
	existing := &config.Config{Channels: config.ChannelsConfig{Telegram: config.TelegramConfig{
		Enabled: true, Token: "old-token",
	}}}
	withStdinInput(t, "2\n"+validOnboardToken+"\n"+validOnboardToken+"\n")
	stubTokenValidator(t, func(string) error { return fmt.Errorf("token validation failed: Unauthorized") })

	out := captureStdout(t, func() {
		cfg := setupTelegram(existing)
		if cfg == nil || cfg.Token != "old-token" {
			t.Fatalf("expected existing token preserved, got %+v", cfg)
		}
	})

	if !strings.Contains(out, "rejected by Telegram") {
		t.Errorf("missing rejected-by-Telegram guidance:\n%s", out)
	}
	if strings.Contains(out, "network problem") {
		t.Errorf("API rejection misreported as a network problem:\n%s", out)
	}
}

// The user gets exactly one retry: the first token fails, the second succeeds.
// The stub fails once so this actually exercises the fail->succeed recovery
// path, and the trailing empty usernames line keeps AllowFrom empty.
func TestSetupTelegram_FirstTokenFails_SecondSucceeds(t *testing.T) {
	withStdinInput(t, "1\n"+validOnboardToken+"\n"+validOnboardToken+"\n\n")
	var first = true
	calls := stubTokenValidator(t, func(string) error {
		if first {
			first = false
			return networkError()
		}
		return nil
	})

	cfg := setupTelegram(nil)
	if cfg == nil || cfg.Token != validOnboardToken {
		t.Errorf("second attempt should succeed, got %+v", cfg)
	}
	if *calls != 2 {
		t.Errorf("validator called %d times, want 2 (one failure, one success)", *calls)
	}
	if len(cfg.AllowFrom) != 0 {
		t.Errorf("AllowFrom = %v, want empty (usernames prompt left blank)", cfg.AllowFrom)
	}
}

// Aborting a token change (cancel / empty) must keep the existing working
// token; the old code returned nil and runOnboard saved Telegram as disabled,
// disconnecting a live bot.
func TestSetupTelegram_Existing_ChangeCancelled_KeepsExisting(t *testing.T) {
	existing := &config.Config{Channels: config.ChannelsConfig{Telegram: config.TelegramConfig{
		Enabled: true, Token: "old-token", AllowFrom: []string{"@old"},
	}}}

	for _, input := range []string{"2\ncancel\n", "2\n\n"} {
		withStdinInput(t, input)
		out := captureStdout(t, func() {
			cfg := setupTelegram(existing)
			if cfg == nil {
				t.Fatalf("input %q: expected a Telegram config", input)
			}
			if !cfg.Enabled || cfg.Token != "old-token" {
				t.Errorf("input %q: aborting a change must keep the existing token, got %+v", input, cfg)
			}
		})
		if !strings.Contains(out, "Keeping the existing Telegram configuration") {
			t.Errorf("input %q: missing keep-existing message:\n%s", input, out)
		}
	}
}

func TestTelegramValidationFailed(t *testing.T) {
	keep := telegramValidationFailed(config.TelegramConfig{Enabled: true, Token: "keep-token", AllowFrom: []string{"@x"}})
	if !keep.Enabled || keep.Token != "keep-token" || len(keep.AllowFrom) != 1 || keep.AllowFrom[0] != "@x" {
		t.Errorf("existing-token fallback = %+v", keep)
	}

	disable := telegramValidationFailed(config.TelegramConfig{})
	if disable.Enabled || disable.Token != "" || len(disable.AllowFrom) != 0 {
		t.Errorf("fresh fallback = %+v", disable)
	}
}

func TestSelectProviderMapping(t *testing.T) {
	// The menu is the recommended default first, then every provider the
	// guided path can finish with just a credential, in SupportedProviders
	// order. Anthropic and OpenAI key-holders must find their provider here.
	cases := []struct{ choice, want string }{
		{"1", "nvidia"},
		{"2", "openrouter"},
		{"3", "openai"},
		{"4", "groq"},
		{"5", "ollama"},
		{"6", "anthropic"},
		{"7", "poolside"},
		{"8", "github-copilot"},
		{"", "nvidia"},
		{"99", "nvidia"},
	}
	for _, tc := range cases {
		t.Run(tc.choice, func(t *testing.T) {
			withStdinInput(t, tc.choice+"\n")
			if got := selectProvider(nil); got != tc.want {
				t.Errorf("selectProvider(%q) = %q, want %q", tc.choice, got, tc.want)
			}
		})
	}
}

func TestPromptServiceInstall(t *testing.T) {
	withStdinInput(t, "1\n")
	if !promptServiceInstall() {
		t.Error("choice 1 should install the service")
	}

	withStdinInput(t, "2\n")
	if promptServiceInstall() {
		t.Error("choice 2 should not install the service")
	}

	withStdinInput(t, "\n")
	if promptServiceInstall() {
		t.Error("empty choice should default to no")
	}
}

func TestCheckExistingInstall(t *testing.T) {
	home := t.TempDir()

	if c, w, files := checkExistingInstall(home); c || w || len(files) != 0 {
		t.Errorf("empty home: config=%v workspace=%v files=%v", c, w, files)
	}

	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if c, w, files := checkExistingInstall(home); !c || w || len(files) != 1 {
		t.Errorf("config only: config=%v workspace=%v files=%v", c, w, files)
	}

	ws := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(ws, "memory"), 0755); err != nil {
		t.Fatal(err)
	}
	if c, w, files := checkExistingInstall(home); !c || !w {
		t.Errorf("full home: config=%v workspace=%v", c, w)
	} else if len(files) != 3 {
		t.Errorf("full home files = %v, want config+workspace+memory", files)
	}
}

func TestBackupExisting(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, ".joshbot")
	if err := os.MkdirAll(filepath.Join(home, "workspace"), 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "config.json")
	if err := os.WriteFile(marker, []byte(`{"x":1}`), 0600); err != nil {
		t.Fatal(err)
	}

	backupPath, err := backupExisting(home)
	if err != nil {
		t.Fatalf("backupExisting: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("original home still exists after backup")
	}
	if _, err := os.Stat(filepath.Join(backupPath, "config.json")); err != nil {
		t.Errorf("config.json not found in backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupPath, "workspace")); err != nil {
		t.Errorf("workspace not found in backup: %v", err)
	}
}

func TestBackupExisting_MissingHome(t *testing.T) {
	if _, err := backupExisting(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error backing up a missing directory")
	}
}

func TestCreateWorkspaceFiles(t *testing.T) {
	ws := t.TempDir()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = ws

	if err := createWorkspaceFiles(cfg, getPersonalitySoul("2")); err != nil {
		t.Fatalf("createWorkspaceFiles: %v", err)
	}

	expected := []string{
		"SOUL.md", "USER.md", "AGENTS.md", "IDENTITY.md",
		filepath.Join("memory", "MEMORY.md"), filepath.Join("memory", "HISTORY.md"),
	}
	for _, rel := range expected {
		if _, err := os.Stat(filepath.Join(ws, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}

	soul, err := os.ReadFile(filepath.Join(ws, "SOUL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(soul), "warm, approachable") {
		t.Errorf("SOUL.md does not carry the friendly personality:\n%s", soul)
	}
}

// onboardApp builds a minimal CLI app exposing just the onboard command, the
// same way the sessions tests expose theirs.
func onboardApp() *cli.App {
	return &cli.App{
		Flags: []cli.Flag{&cli.PathFlag{Name: "config"}},
		Commands: []*cli.Command{{
			Name: "onboard",
			Flags: []cli.Flag{
				&cli.BoolFlag{Name: "force"},
				&cli.BoolFlag{Name: "keep-data"},
				&cli.StringFlag{Name: "model", Aliases: []string{"m"}},
				&cli.StringFlag{Name: "provider"},
				&cli.StringFlag{Name: "api-key"},
				&cli.StringFlag{Name: "api-base"},
			},
			Action: runOnboard,
		}},
		Writer:                io.Discard,
		ErrWriter:             io.Discard,
		ExitErrHandler:        func(*cli.Context, error) {},
		CustomAppHelpTemplate: " ",
	}
}

// setHome points joshbot at an isolated home for the duration of a test,
// restoring the globals — including the config-file override set by LoadFrom —
// afterwards.
func setHome(t *testing.T, home string) {
	t.Helper()
	prevHome, prevWs := config.DefaultHome, config.DefaultWorkspace
	config.SetHome(home)
	t.Cleanup(func() {
		config.SetHome(prevHome)
		config.DefaultWorkspace = prevWs
	})
}

// runOnboardCmdExpectingError runs onboard and requires it to fail, returning
// the error so the caller can assert on the message.
func runOnboardCmdExpectingError(t *testing.T, args ...string) error {
	t.Helper()

	withStdinInput(t, "")

	full := append([]string{"joshbot", "--config", filepath.Join(config.DefaultHome, "config.json"), "onboard"}, args...)
	err := onboardApp().Run(full)
	if err == nil {
		t.Fatal("onboard succeeded, want an error")
	}
	return err
}

func runOnboardCmd(t *testing.T, args ...string) {
	t.Helper()

	// force mode must not read stdin; keep it closed so a regression would
	// hang the test rather than silently skipping input.
	withStdinInput(t, "")

	full := append([]string{"joshbot", "--config", filepath.Join(config.DefaultHome, "config.json"), "onboard"}, args...)
	if err := onboardApp().Run(full); err != nil {
		t.Fatalf("onboard failed: %v", err)
	}
}

// onboard --force on a fresh home must write a config and the workspace files
// and never block on stdin, even with no input at all.
func TestRunOnboard_Force_FreshHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".joshbot")
	setHome(t, home)
	// --force with no credential available still writes the config and
	// workspace scaffold, but must exit non-zero rather than reporting success
	// over an unusable config (#142).
	err := runOnboardCmdExpectingError(t, "--force")
	if !strings.Contains(err.Error(), "did not configure any provider") {
		t.Errorf("error = %v, want it to name the missing provider", err)
	}

	cfgPath := filepath.Join(home, "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "SOUL.md")); err != nil {
		t.Errorf("workspace files not created: %v", err)
	}

	cfg, loadErr := config.LoadFrom(cfgPath)
	if loadErr != nil {
		t.Fatalf("load config: %v", loadErr)
	}
	if cfg.Channels.Telegram.Enabled {
		t.Error("fresh --force must not enable Telegram")
	}
	if cfg.Providers["openrouter"].Enabled {
		t.Error("fresh --force has no API key, must not enable a provider")
	}
}

// onboard --force must preserve a configured API key instead of prompting for
// one and, on a closed stdin, silently dropping it (the old behaviour).
// The bug from the field: reconfiguring an existing NVIDIA install, then
// pressing Enter at the API key prompt to keep the current key, saved a config
// with no enabled provider at all. promptProviderAPIKey returned "" for
// "keep current", runOnboard read that as "no provider configured" and skipped
// the provider block, so the config fell back to config.Defaults()'s seed of a
// disabled openrouter entry and `joshbot agent` reported "no providers enabled".
func TestRunOnboard_Interactive_KeepCurrentAPIKey(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".joshbot")
	setHome(t, home)

	existing := config.Defaults()
	existing.Providers = map[string]config.ProviderConfig{
		"nvidia": {APIKey: "nvapi-keepme", Enabled: true},
	}
	existing.ProviderDefaults.Default = "nvidia"
	existing.Agents.Defaults.Model = "z-ai/glm-5.2"
	if err := config.Save(existing); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// 1: keep existing data; 1: nvidia; Enter: keep current key;
	// 2: personality; Enter: keep name; Enter: accept model;
	// 2: skip telegram; 2: skip service install.
	withStdinInput(t, "1\n1\n\n2\n\n\n2\n2\n")
	calls := stubTokenValidator(t, func(string) error { return nil })

	if err := onboardApp().Run([]string{
		"joshbot", "--config", filepath.Join(home, "config.json"), "onboard",
	}); err != nil {
		t.Fatalf("onboard failed: %v", err)
	}

	cfg, err := config.LoadFrom(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	p, ok := cfg.Providers["nvidia"]
	if !ok {
		t.Fatalf("nvidia provider missing from config: %+v", cfg.Providers)
	}
	if p.APIKey != "nvapi-keepme" {
		t.Errorf("nvidia api_key = %q, want existing key preserved", p.APIKey)
	}
	if !p.Enabled {
		t.Error("nvidia must stay enabled after keep-current onboard")
	}
	if cfg.ProviderDefaults.Default != "nvidia" {
		t.Errorf("default provider = %q, want nvidia", cfg.ProviderDefaults.Default)
	}
	if *calls != 0 {
		t.Errorf("token validator called %d times, want 0 (telegram skipped)", *calls)
	}
}

func TestRunOnboard_Force_PreservesExistingAPIKey(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".joshbot")
	setHome(t, home)

	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	existing := config.Defaults()
	existing.Providers = map[string]config.ProviderConfig{
		"openrouter": {APIKey: "sk-or-v1-keepme", Enabled: true},
	}
	existing.Agents.Defaults.Model = "openrouter/free"
	if err := config.Save(existing); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	runOnboardCmd(t, "--force")

	cfg, err := config.LoadFrom(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Providers["openrouter"].APIKey; got != "sk-or-v1-keepme" {
		t.Errorf("API key = %q, want the existing key preserved", got)
	}
	if !cfg.Providers["openrouter"].Enabled {
		t.Error("existing enabled provider must stay enabled")
	}
}

// A second provider's key in the environment becomes a fallback: in --force
// mode automatically (a non-interactive run cannot ask, and an exported env
// var is an explicit choice), with the fallback order written primary-first.
func TestOfferEnvFallbacksForce(t *testing.T) {
	t.Setenv("JOSHBOT_PROVIDERS__GROQ__API_KEY", "gsk-env")
	t.Setenv("JOSHBOT_PROVIDERS__ANTHROPIC__API_KEY", "")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"nvidia": {APIKey: "k", Enabled: true},
	}
	cfg.ProviderDefaults.Default = "nvidia"

	captureStdout(t, func() { offerEnvFallbacks(cfg, "nvidia", true) })

	p, ok := cfg.Providers["groq"]
	if !ok || !p.Enabled || p.APIKey != "gsk-env" {
		t.Fatalf("groq not added from env: %+v", cfg.Providers)
	}
	order := cfg.ProviderDefaults.FallbackOrder
	if len(order) != 2 || order[0] != "nvidia" || order[1] != "groq" {
		t.Errorf("FallbackOrder = %v, want [nvidia groq]", order)
	}
	if _, ok := cfg.Providers["anthropic"]; ok {
		t.Error("an empty env var must not add a provider")
	}
}

// Interactively, 'n' declines and nothing is written.
func TestOfferEnvFallbacksInteractiveDecline(t *testing.T) {
	t.Setenv("JOSHBOT_PROVIDERS__GROQ__API_KEY", "gsk-env")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{"nvidia": {APIKey: "k", Enabled: true}}
	cfg.ProviderDefaults.Default = "nvidia"

	withStdinInput(t, "n\n")
	captureStdout(t, func() { offerEnvFallbacks(cfg, "nvidia", false) })

	if _, ok := cfg.Providers["groq"]; ok {
		t.Error("declined provider was added anyway")
	}
	if cfg.ProviderDefaults.FallbackOrder != nil {
		t.Errorf("FallbackOrder = %v, want none", cfg.ProviderDefaults.FallbackOrder)
	}
}

// Interactively, bare Enter (and EOF) accepts — the default is yes.
func TestOfferEnvFallbacksInteractiveDefaultYes(t *testing.T) {
	t.Setenv("JOSHBOT_PROVIDERS__GROQ__API_KEY", "gsk-env")

	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{"nvidia": {APIKey: "k", Enabled: true}}
	cfg.ProviderDefaults.Default = "nvidia"

	withStdinInput(t, "\n")
	captureStdout(t, func() { offerEnvFallbacks(cfg, "nvidia", false) })

	if _, ok := cfg.Providers["groq"]; !ok {
		t.Error("bare Enter should accept the fallback")
	}
}

// The wizard must validate the token against the configured api_url (#321): a
// LAN-only self-hosted Bot API server is unreachable via api.telegram.org, so
// validating there reports a working token as invalid. Mutation: drop the
// plumbing in setupTelegram and this goes red.
func TestSetupTelegram_ValidatesAgainstConfiguredAPIURL(t *testing.T) {
	withStdinInput(t, "2\n"+validOnboardToken+"\n\n")

	prev := validateTelegramToken
	var gotAPIURL string
	validateTelegramToken = func(token, apiURL string) error {
		gotAPIURL = apiURL
		return nil
	}
	t.Cleanup(func() { validateTelegramToken = prev })

	existing := &config.Config{}
	existing.Channels.Telegram = config.TelegramConfig{
		Enabled: true,
		Token:   "1111111111:OLDOLDOLDOLDOLDOLDOLDOLDOLDOLDx",
		APIURL:  "http://127.0.0.1:8081",
	}

	captureStdout(t, func() {
		if cfg := setupTelegram(existing); cfg == nil {
			t.Fatal("expected a Telegram config")
		}
	})

	if gotAPIURL != "http://127.0.0.1:8081" {
		t.Errorf("validator got apiURL %q, want the configured api_url", gotAPIURL)
	}
}

// Every setupTelegram return derives from the existing config: runOnboard
// assigns the result wholesale, so a field the wizard does not collect
// (api_url, reactions, stream_drafts) must ride through or a re-run erases it.
func TestSetupTelegram_PreservesFieldsTheWizardDoesNotCollect(t *testing.T) {
	existing := &config.Config{}
	existing.Channels.Telegram = config.TelegramConfig{
		Enabled:      true,
		Token:        "1111111111:OLDOLDOLDOLDOLDOLDOLDOLDOLDOLDx",
		AllowFrom:    []string{"@x"},
		APIURL:       "http://127.0.0.1:8081",
		Reactions:    true,
		StreamDrafts: true,
	}

	check := func(t *testing.T, cfg *config.TelegramConfig) {
		t.Helper()
		if cfg == nil {
			t.Fatal("expected a Telegram config")
		}
		if cfg.APIURL != "http://127.0.0.1:8081" || !cfg.Reactions || !cfg.StreamDrafts {
			t.Errorf("uncollected fields dropped: %+v", cfg)
		}
	}

	t.Run("keep current token", func(t *testing.T) {
		withStdinInput(t, "1\n")
		captureStdout(t, func() { check(t, setupTelegram(existing)) })
	})

	t.Run("new token accepted", func(t *testing.T) {
		withStdinInput(t, "2\n"+validOnboardToken+"\n\n")
		stubTokenValidator(t, func(string) error { return nil })
		captureStdout(t, func() { check(t, setupTelegram(existing)) })
	})

	t.Run("validation failed, existing kept", func(t *testing.T) {
		withStdinInput(t, "2\n"+validOnboardToken+"\n"+validOnboardToken+"\n")
		stubTokenValidator(t, func(string) error { return fmt.Errorf("rejected") })
		captureStdout(t, func() { check(t, setupTelegram(existing)) })
	})

	t.Run("cancelled", func(t *testing.T) {
		withStdinInput(t, "2\ncancel\n")
		captureStdout(t, func() { check(t, setupTelegram(existing)) })
	})
}

// onboard --force with a provider key in the environment must configure that
// provider: JOSHBOT_PROVIDERS__NVIDIA__API_KEY names nvidia explicitly, and
// the old openrouter default ignored it and then failed telling the operator
// to set exactly the variable they had set.
func TestRunOnboard_Force_PicksProviderFromEnvKey(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".joshbot")
	setHome(t, home)
	t.Setenv("JOSHBOT_PROVIDERS__NVIDIA__API_KEY", "nvapi-from-env")

	runOnboardCmd(t, "--force")

	cfg, err := config.LoadFrom(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p, ok := cfg.Providers["nvidia"]
	if !ok || !p.Enabled || p.APIKey != "nvapi-from-env" {
		t.Errorf("nvidia not configured from env: %+v", cfg.Providers)
	}
}

// An explicit --provider flag still beats the environment scan.
func TestRunOnboard_Force_ProviderFlagBeatsEnvKey(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".joshbot")
	setHome(t, home)
	t.Setenv("JOSHBOT_PROVIDERS__NVIDIA__API_KEY", "nvapi-from-env")

	err := runOnboardCmdExpectingError(t, "--force", "--provider", "groq")
	if err == nil || !strings.Contains(err.Error(), "GROQ") {
		t.Fatalf("--provider groq with only an nvidia env key must fail naming GROQ, got: %v", err)
	}
}
