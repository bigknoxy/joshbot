package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
)

// writePermissionConfig writes body as a config file and returns its path.
func writePermissionConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// runPermission invokes the `permission` command and returns its stdout and
// the error returned by app.Run.
func runPermission(t *testing.T, configPath string, args ...string) (string, error) {
	t.Helper()
	app := &cli.App{
		Flags:          []cli.Flag{&cli.PathFlag{Name: "config"}},
		Commands:       []*cli.Command{permissionCommand()},
		Writer:         io.Discard,
		ExitErrHandler: func(*cli.Context, error) {},
	}
	var err error
	out := captureStdout(t, func() {
		err = app.Run(append([]string{"joshbot", "--config", configPath}, args...))
	})
	return out, err
}

func workingPermissionConfig(t *testing.T) string {
	t.Helper()
	return writePermissionConfig(t, `{
	  "schema_version": 1,
	  "agents": {
	    "defaults": {
	      "model": "openrouter/anthropic/claude-sonnet-4",
	      "max_tokens": 4096
	    }
	  },
	  "providers": {"openrouter": {"enabled": true, "api_key": "sk-test0123456789abcdefghij"}},
	  "models_config": {
	    "agent": {"model": "openrouter/anthropic/claude-sonnet-4"},
	    "models": [{"name": "openrouter/anthropic/claude-sonnet-4",
	                "model": "openrouter/anthropic/claude-sonnet-4",
	                "api_key": "sk-test0123456789abcdefghij"}]
	  }
	}`)
}

// The tier is written to the config file, but the command must not claim it
// is active in the running process: joshbot has no live config reload, so
// the change applies to the next start of `agent` or `gateway`.
func TestPermissionSet_WritesConfigAndSaysNextStart(t *testing.T) {
	configPath := workingPermissionConfig(t)

	out, err := runPermission(t, configPath, "permission", "set", "read-only")
	if err != nil {
		t.Fatalf("permission set read-only: %v", err)
	}
	if !strings.Contains(out, "next start") {
		t.Errorf("expected the message to say the tier applies on the next start, got:\n%s", out)
	}
	for _, phrase := range []string{"active now", "is now active", "tier is active"} {
		if strings.Contains(out, phrase) {
			t.Errorf("expected the message not to claim %q in this process, got:\n%s", phrase, out)
		}
	}

	// The config file really was rewritten, including the tier-level denial.
	loaded, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom saved config: %v", err)
	}
	if loaded.Tools.PermissionTier != config.PermissionTierReadOnly {
		t.Errorf("saved permission_tier = %q, want %q", loaded.Tools.PermissionTier, config.PermissionTierReadOnly)
	}
	if !loaded.Tools.ShellDenied {
		t.Error("saved config does not have shell_denied for the read-only tier")
	}
	if loaded.Tools.ShellApproval != "always" {
		t.Errorf("saved shell_approval = %q, want %q", loaded.Tools.ShellApproval, "always")
	}
	if !loaded.Tools.SendFileDisabled {
		t.Error("saved config does not disable send_file for the read-only tier")
	}
}

func TestPermissionSet_RejectsInvalidTier(t *testing.T) {
	configPath := workingPermissionConfig(t)

	_, err := runPermission(t, configPath, "permission", "set", "read-onlyy")
	if err == nil {
		t.Fatal("expected an error for an invalid tier, got none")
	}
	if !strings.Contains(err.Error(), "read-onlyy") {
		t.Errorf("error %q does not name the invalid tier", err)
	}
}

func TestPermissionStatus_ShowsShellDenied(t *testing.T) {
	configPath := workingPermissionConfig(t)

	if _, err := runPermission(t, configPath, "permission", "set", "read-only"); err != nil {
		t.Fatalf("permission set read-only: %v", err)
	}

	out, err := runPermission(t, configPath, "permission", "status")
	if err != nil {
		t.Fatalf("permission status: %v", err)
	}
	if !strings.Contains(out, "shell_denied: true") {
		t.Errorf("expected status to show shell_denied: true, got:\n%s", out)
	}
}

// Every tier is a promise about what the agent may do, so `permission status`
// has to be able to describe each of them - and say so plainly when no tier
// is active at all.
func TestPermissionStatus_DescribesEveryTier(t *testing.T) {
	cases := []struct {
		tier string
		want []string
	}{
		{config.PermissionTierReadOnly, []string{
			"restrict_to_workspace: true",
			"shell_sandbox: off",
			"shell_approval: always",
			"shell_denied: true",
			"send_file_disabled: true",
		}},
		{config.PermissionTierWorkspaceWrite, []string{
			"restrict_to_workspace: true",
			"shell_sandbox: workspace",
			"shell_approval: interactive",
			"send_file_approval: interactive",
			"send_file_disabled: false",
		}},
		{config.PermissionTierFull, []string{
			"restrict_to_workspace: false",
			"shell_sandbox: off",
			"shell_approval: off",
			"send_file_approval: off",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.tier, func(t *testing.T) {
			configPath := workingPermissionConfig(t)

			if _, err := runPermission(t, configPath, "permission", "set", tc.tier); err != nil {
				t.Fatalf("permission set %s: %v", tc.tier, err)
			}
			out, err := runPermission(t, configPath, "permission", "status")
			if err != nil {
				t.Fatalf("permission status: %v", err)
			}
			if !strings.Contains(out, "Active permission tier: "+tc.tier) {
				t.Errorf("status does not name the active tier %q:\n%s", tc.tier, out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("status for %q does not show %q:\n%s", tc.tier, want, out)
				}
			}
		})
	}
}

// With no tier set, the status must say that rather than imply one.
func TestPermissionStatus_SaysWhenNoTierIsActive(t *testing.T) {
	configPath := workingPermissionConfig(t)

	out, err := runPermission(t, configPath, "permission", "status")
	if err != nil {
		t.Fatalf("permission status: %v", err)
	}
	if !strings.Contains(out, "No tier active.") {
		t.Errorf("expected status to say no tier is active, got:\n%s", out)
	}
}

// `permission set` without a tier is a usage error, not a silent no-op.
func TestPermissionSet_RequiresATier(t *testing.T) {
	configPath := workingPermissionConfig(t)

	_, err := runPermission(t, configPath, "permission", "set")
	if err == nil {
		t.Fatal("expected an error when no tier is given, got none")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("error %q is not a usage error", err)
	}
}
