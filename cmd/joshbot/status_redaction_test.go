package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

// captureStdout runs fn with os.Stdout redirected and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	os.Stdout = prev
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// statusConfig writes a config carrying a credential and returns its path.
func statusConfig(t *testing.T, dir, apiKey string) string {
	t.Helper()

	cfg := map[string]any{
		"default_provider": "openai",
		"providers": map[string]any{
			"openai": map[string]any{
				"enabled": true,
				"api_key": apiKey,
				"model":   "gpt-4o",
			},
		},
		"agents": map[string]any{
			"defaults": map[string]any{
				"model":     "openai/gpt-4o",
				"workspace": filepath.Join(dir, "workspace"),
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// runStatusWith invokes runStatus against a specific config path.
func runStatusWith(t *testing.T, configPath string) string {
	t.Helper()

	app := &cli.App{
		Flags:  []cli.Flag{&cli.PathFlag{Name: "config"}},
		Action: runStatus,
		Writer: io.Discard,
	}
	return captureStdout(t, func() {
		if err := app.Run([]string{"joshbot", "--config", configPath}); err != nil {
			t.Errorf("runStatus: %v", err)
		}
	})
}

// A configured credential must never appear in status output — this is the
// command users are most likely to paste into an issue.
func TestStatusDoesNotPrintCredentials(t *testing.T) {
	dir := t.TempDir()
	const apiKey = "sk-proj0123456789abcdefghij0123"

	out := runStatusWith(t, statusConfig(t, dir, apiKey))

	if strings.Contains(out, apiKey) {
		t.Errorf("status printed the API key:\n%s", out)
	}
	// The command must still be useful.
	if !strings.Contains(out, "joshbot status") {
		t.Errorf("status output looks empty or malformed:\n%s", out)
	}
}

// The home directory carries the account name, which identifies the machine's
// user in anything shared publicly.
func TestStatusRedactsHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := runStatusWith(t, statusConfig(t, home, "sk-proj0123456789abcdefghij0123"))

	if strings.Contains(out, home) {
		t.Errorf("status printed the raw home path %q:\n%s", home, out)
	}
	if !strings.Contains(out, "~") {
		t.Errorf("expected ~-rooted paths in status output:\n%s", out)
	}
}
