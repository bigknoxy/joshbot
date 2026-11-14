package main

import (
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/redact"
)

// permissionOut is the redacted writer every subcommand prints through.
var permissionOut = func() io.Writer { return redact.Writer(os.Stdout) }

// permissionCommand builds the `joshbot permission` command group.
func permissionCommand() *cli.Command {
	return &cli.Command{
		Name:  "permission",
		Usage: "Manage named permission tiers for tool access",
		Description: "Permission tiers are named presets that map onto the underlying\n" +
			"tool settings (shell sandbox, shell approval, send_file approval,\n" +
			"workspace restriction, etc.). Selecting a tier sets every relevant\n" +
			"flag consistently. An explicit per-tool override in config.json\n" +
			"still wins over the tier.\n\n" +
			"Tiers:\n" +
			"  read-only       - No shell, no send_file, workspace restricted, no network\n" +
			"  workspace-write - Shell in sandbox, send_file with approval, workspace restricted\n" +
			"  full            - No restrictions (danger-full-access)",
		Subcommands: []*cli.Command{
			{
				Name:   "status",
				Usage:  "Show the active permission tier and the flags it implies",
				Action: runPermissionStatus,
			},
			{
				Name:      "set",
				Usage:     "Set the active permission tier",
				ArgsUsage: "<read-only|workspace-write|full>",
				Action:    runPermissionSet,
			},
		},
	}
}

func runPermissionStatus(c *cli.Context) error {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}

	out := permissionOut()

	tier := cfg.Tools.PermissionTier
	if tier == "" {
		tier = "(none - individual settings control behaviour)"
	}

	fmt.Fprintf(out, "Active permission tier: %s\n\n", tier)

	// Show what the tier implies
	switch cfg.Tools.PermissionTier {
	case config.PermissionTierReadOnly:
		fmt.Fprintf(out, "Implied settings (from tier):\n")
		fmt.Fprintf(out, "  restrict_to_workspace: true\n")
		fmt.Fprintf(out, "  shell_sandbox: off\n")
		fmt.Fprintf(out, "  shell_sandbox_allow_network: false\n")
		fmt.Fprintf(out, "  shell_approval: always\n")
		fmt.Fprintf(out, "  send_file_approval: always\n")
		fmt.Fprintf(out, "  send_file_disabled: true\n")
	case config.PermissionTierWorkspaceWrite:
		fmt.Fprintf(out, "Implied settings (from tier):\n")
		fmt.Fprintf(out, "  restrict_to_workspace: true\n")
		fmt.Fprintf(out, "  shell_sandbox: workspace\n")
		fmt.Fprintf(out, "  shell_sandbox_allow_network: false\n")
		fmt.Fprintf(out, "  shell_approval: interactive\n")
		fmt.Fprintf(out, "  send_file_approval: interactive\n")
		fmt.Fprintf(out, "  send_file_disabled: false\n")
	case config.PermissionTierFull:
		fmt.Fprintf(out, "Implied settings (from tier):\n")
		fmt.Fprintf(out, "  restrict_to_workspace: false\n")
		fmt.Fprintf(out, "  shell_sandbox: off\n")
		fmt.Fprintf(out, "  shell_sandbox_allow_network: true\n")
		fmt.Fprintf(out, "  shell_approval: off\n")
		fmt.Fprintf(out, "  send_file_approval: off\n")
		fmt.Fprintf(out, "  send_file_disabled: false\n")
	default:
		fmt.Fprintf(out, "No tier active.\n")
	}

	// Show effective settings (after tier + explicit overrides)
	fmt.Fprintf(out, "\nEffective settings (after overrides):\n")
	fmt.Fprintf(out, "  restrict_to_workspace: %v\n", cfg.Tools.RestrictToWorkspace)
	fmt.Fprintf(out, "  shell_sandbox: %s\n", cfg.Tools.ShellSandbox)
	fmt.Fprintf(out, "  shell_sandbox_allow_network: %v\n", cfg.Tools.ShellSandboxAllowNetwork)
	fmt.Fprintf(out, "  shell_approval: %s\n", cfg.Tools.ShellApproval)
	fmt.Fprintf(out, "  send_file_approval: %s\n", cfg.Tools.SendFileApproval)
	fmt.Fprintf(out, "  send_file_disabled: %v\n", cfg.Tools.SendFileDisabled)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Note: Explicit per-tool overrides in config.json take precedence over the tier.")
	fmt.Fprintln(out, "Run 'joshbot permission set <tier>' to change the tier.")
	return nil
}

func runPermissionSet(c *cli.Context) error {
	if c.NArg() != 1 {
		return fmt.Errorf("usage: joshbot permission set <read-only|workspace-write|full>")
	}

	tier := c.Args().Get(0)
	validTiers := map[string]bool{
		config.PermissionTierReadOnly:       true,
		config.PermissionTierWorkspaceWrite: true,
		config.PermissionTierFull:           true,
	}

	if !validTiers[tier] {
		return fmt.Errorf("invalid tier %q; must be one of: read-only, workspace-write, full", tier)
	}

	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}

	cfg.Tools.PermissionTier = tier

	// Explicitly set the tier values in the config so they are written to the file.
	// This ensures the tier values persist and override any defaults.
	switch tier {
	case config.PermissionTierReadOnly:
		cfg.Tools.RestrictToWorkspace = true
		cfg.Tools.ShellSandbox = "off"
		cfg.Tools.ShellSandboxAllowNetwork = false
		cfg.Tools.ShellApproval = "always"
		cfg.Tools.SendFileApproval = "always"
		cfg.Tools.SendFileDisabled = true
	case config.PermissionTierWorkspaceWrite:
		cfg.Tools.RestrictToWorkspace = true
		cfg.Tools.ShellSandbox = "workspace"
		cfg.Tools.ShellSandboxAllowNetwork = false
		cfg.Tools.ShellApproval = "interactive"
		cfg.Tools.SendFileApproval = "interactive"
		cfg.Tools.SendFileDisabled = false
	case config.PermissionTierFull:
		cfg.Tools.RestrictToWorkspace = false
		cfg.Tools.ShellSandbox = "off"
		cfg.Tools.ShellSandboxAllowNetwork = true
		cfg.Tools.ShellApproval = "off"
		cfg.Tools.SendFileApproval = "off"
		cfg.Tools.SendFileDisabled = false
	}

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	out := permissionOut()
	fmt.Fprintf(out, "Permission tier set to %q.\n", tier)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "The following settings were updated:\n")

	switch tier {
	case config.PermissionTierReadOnly:
		fmt.Fprintf(out, "  restrict_to_workspace: true\n")
		fmt.Fprintf(out, "  shell_sandbox: off\n")
		fmt.Fprintf(out, "  shell_sandbox_allow_network: false\n")
		fmt.Fprintf(out, "  shell_approval: always\n")
		fmt.Fprintf(out, "  send_file_approval: always\n")
		fmt.Fprintf(out, "  send_file_disabled: true\n")
	case config.PermissionTierWorkspaceWrite:
		fmt.Fprintf(out, "  restrict_to_workspace: true\n")
		fmt.Fprintf(out, "  shell_sandbox: workspace\n")
		fmt.Fprintf(out, "  shell_sandbox_allow_network: false\n")
		fmt.Fprintf(out, "  shell_approval: interactive\n")
		fmt.Fprintf(out, "  send_file_approval: interactive\n")
		fmt.Fprintf(out, "  send_file_disabled: false\n")
	case config.PermissionTierFull:
		fmt.Fprintf(out, "  restrict_to_workspace: false\n")
		fmt.Fprintf(out, "  shell_sandbox: off\n")
		fmt.Fprintf(out, "  shell_sandbox_allow_network: true\n")
		fmt.Fprintf(out, "  shell_approval: off\n")
		fmt.Fprintf(out, "  send_file_approval: off\n")
		fmt.Fprintf(out, "  send_file_disabled: false\n")
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Note: Explicit per-tool overrides in config.json take precedence.\n")
	fmt.Fprintf(out, "Run 'joshbot permission status' to see the current effective settings.\n")

	return nil
}
