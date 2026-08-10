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
	validateTelegramToken = func(token string) error {
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
func TestSetupTelegram_FirstTokenFails_SecondSucceeds(t *testing.T) {
	withStdinInput(t, "1\n"+validOnboardToken+"\n"+validOnboardToken+"\n\n")
	calls := stubTokenValidator(t, func(string) error { return nil })

	cfg := setupTelegram(nil)
	if cfg == nil || cfg.Token != validOnboardToken {
		t.Errorf("second attempt should succeed, got %+v", cfg)
	}
	if *calls != 1 {
		t.Errorf("validator called %d times, want 1", *calls)
	}
}

func TestTelegramValidationFailed(t *testing.T) {
	keep := telegramValidationFailed(true, "keep-token", []string{"@x"})
	if !keep.Enabled || keep.Token != "keep-token" || len(keep.AllowFrom) != 1 || keep.AllowFrom[0] != "@x" {
		t.Errorf("existing-token fallback = %+v", keep)
	}

	disable := telegramValidationFailed(false, "", nil)
	if disable.Enabled || disable.Token != "" || len(disable.AllowFrom) != 0 {
		t.Errorf("fresh fallback = %+v", disable)
	}
}

func TestSelectProviderMapping(t *testing.T) {
	cases := []struct{ choice, want string }{
		{"1", "nvidia"},
		{"2", "openrouter"},
		{"3", "groq"},
		{"4", "ollama"},
		{"5", "github-copilot"},
		{"6", "poolside"},
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
	runOnboardCmd(t, "--force")

	cfgPath := filepath.Join(home, "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "SOUL.md")); err != nil {
		t.Errorf("workspace files not created: %v", err)
	}

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
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
