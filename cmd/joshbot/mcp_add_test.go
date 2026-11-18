package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/mcp"
	"github.com/bigknoxy/joshbot/internal/output"
)

// runMCPAddCmd runs `mcp add` through the real command tree, so the flags
// under test are the ones an operator actually gets.
func runMCPAddCmd(t *testing.T, configPath string, args ...string) (string, error) {
	t.Helper()
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.PathFlag{Name: "config"},
			&cli.StringFlag{Name: "output", Value: string(output.Text)},
		},
		Commands:       []*cli.Command{mcpCommand()},
		Writer:         io.Discard,
		ExitErrHandler: func(*cli.Context, error) {},
	}
	full := append([]string{"joshbot", "--config", configPath, "mcp", "add"}, args...)
	var err error
	out := captureStdout(t, func() { err = app.Run(full) })
	return out, err
}

// mcpConfigWithoutMCPSection writes a config with no MCP section at all, which
// is what a first-run config looks like: the operator never configured a
// server, so there is no map to add one to.
func mcpConfigWithoutMCPSection(t *testing.T) string {
	t.Helper()
	home := withTempHome(t)
	configPath := filepath.Join(home, "config.json")
	body := []byte(`{"agents":{"defaults":{"workspace":` + quoteJSON(filepath.Join(home, "workspace")) + `}}}`)
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}

// loadMCPConfig reads the config back after `mcp add` wrote it.
func loadMCPConfig(t *testing.T, configPath string) *config.Config {
	t.Helper()
	cfg, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// TestMCPAddAddsAServerWithoutApprovingIt is the whole point of the command:
// the first step of using MCP is a step the CLI now offers, and it writes the
// same fields the hand-authored path writes.
func TestMCPAddAddsAServerWithoutApprovingIt(t *testing.T) {
	configPath := mcpEnv(t, map[string]any{})

	out, err := runMCPAddCmd(t, configPath,
		"--env", "TOKEN=secret", "--env", "MODE=fast",
		"fake", "/bin/sh", "server.sh")
	if err != nil {
		t.Fatalf("mcp add: %v", err)
	}
	if !strings.Contains(out, "not approved") {
		t.Errorf("the output must say the server is not approved, got: %s", out)
	}

	cfg := loadMCPConfig(t, configPath)
	sc, ok := cfg.MCP.Servers["fake"]
	if !ok {
		t.Fatalf("the server must be in the config, got %v", cfg.MCP.Servers)
	}
	if sc.Command != "/bin/sh" {
		t.Errorf("command = %q, want %q", sc.Command, "/bin/sh")
	}
	if len(sc.Args) != 1 || sc.Args[0] != "server.sh" {
		t.Errorf("args = %v, want [server.sh]", sc.Args)
	}
	if sc.Env["TOKEN"] != "secret" || sc.Env["MODE"] != "fast" {
		t.Errorf("env = %v, want TOKEN=secret and MODE=fast", sc.Env)
	}
	if !sc.Enabled {
		t.Error("a server added without --disabled must be enabled")
	}

	// The gate: adding a server must not be a way around approving one.
	trust, err := mcp.LoadTrustStore(mcp.DefaultTrustStorePath(config.DefaultHome))
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	for _, name := range trust.TrustedNames() {
		if name == "fake" {
			t.Fatal("mcp add must not approve the server it adds")
		}
	}
}

// TestMCPAddOnAConfigWithNoMCPSection covers the first-run case: the config
// has no MCP section, so there is no map, and writing to a nil map panics.
func TestMCPAddOnAConfigWithNoMCPSection(t *testing.T) {
	configPath := mcpConfigWithoutMCPSection(t)

	if _, err := runMCPAddCmd(t, configPath, "fresh", "/bin/echo"); err != nil {
		t.Fatalf("mcp add on a config with no mcp section: %v", err)
	}
	cfg := loadMCPConfig(t, configPath)
	if _, ok := cfg.MCP.Servers["fresh"]; !ok {
		t.Fatalf("the server must be in the config, got %v", cfg.MCP.Servers)
	}
}

// TestMCPAddRefusesAnInvalidConfiguration pins that an invalid server is
// refused with a reason, rather than written and left to fail at start-up.
func TestMCPAddRefusesAnInvalidConfiguration(t *testing.T) {
	configPath := mcpEnv(t, map[string]any{})

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no command", []string{"onlyname"}, "name and the command"},
		{"a path separator in the name", []string{"../escape", "/bin/echo"}, "path separator"},
		{"an env entry that is not KEY=VALUE", []string{"--env", "NOEQUALS", "x", "/bin/echo"}, "KEY=VALUE"},
		{"an env name that is not a name", []string{"--env", "9BAD=1", "x", "/bin/echo"}, "not a valid name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runMCPAddCmd(t, configPath, tc.args...)
			if err == nil {
				t.Fatalf("mcp add %v must fail", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q must explain %q", err, tc.want)
			}
		})
	}

	// None of the refused attempts may have left a server behind.
	cfg := loadMCPConfig(t, configPath)
	if len(cfg.MCP.Servers) != 0 {
		t.Fatalf("a refused add must not write a server, got %v", cfg.MCP.Servers)
	}
}

// TestMCPAddRefusesADuplicateName keeps one server under one name, so the
// trust store's identity is not ambiguous.
func TestMCPAddRefusesADuplicateName(t *testing.T) {
	configPath := mcpEnv(t, map[string]any{
		"fake": map[string]any{"command": "/bin/sh", "enabled": true},
	})

	_, err := runMCPAddCmd(t, configPath, "fake", "/bin/echo")
	if err == nil {
		t.Fatal("adding a server that already exists must fail")
	}
	if !strings.Contains(err.Error(), "already configured") {
		t.Errorf("error %q must say the server already exists", err)
	}
}

// TestMCPAddHonoursTheDisabledAndArgFlags covers the two flags that have no
// positional equivalent: --arg is how an argument that begins with a dash is
// passed, since everything after the command is otherwise taken as the
// server's own arguments.
func TestMCPAddHonoursTheDisabledAndArgFlags(t *testing.T) {
	configPath := mcpEnv(t, map[string]any{})

	if _, err := runMCPAddCmd(t, configPath, "--disabled", "--arg", "--port", "--arg", "8080", "fake", "/bin/echo"); err != nil {
		t.Fatalf("mcp add: %v", err)
	}
	cfg := loadMCPConfig(t, configPath)
	sc, ok := cfg.MCP.Servers["fake"]
	if !ok {
		t.Fatalf("the server must be in the config, got %v", cfg.MCP.Servers)
	}
	if sc.Enabled {
		t.Error("--disabled must leave the server disabled")
	}
	if len(sc.Args) != 2 || sc.Args[0] != "--port" || sc.Args[1] != "8080" {
		t.Errorf("args = %v, want [--port 8080]", sc.Args)
	}
}
