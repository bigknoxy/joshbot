package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/mcp"
	"github.com/bigknoxy/joshbot/internal/output"
	"github.com/bigknoxy/joshbot/internal/redact"
	"github.com/urfave/cli/v2"
)

// Operator-facing commands for MCP server provenance.
//
// An MCP server's tool descriptions and schemas go into the model's context and
// become callable, which is the same power a workspace SKILL.md has — so it
// gets the same gate, and for the same reason the gate belongs to a person.
// Nothing the model can reach calls into these commands.

// mcpCLITimeout bounds the handshake and tools/list of one server. An operator
// is waiting at a terminal, so a server that never answers must fail the entry
// rather than hang the whole listing.
const mcpCLITimeout = 15 * time.Second

// mcpTrustStoreForCLI loads the MCP trust store for the configured home.
func mcpTrustStoreForCLI() (*mcp.TrustStore, error) {
	return mcp.LoadTrustStore(mcp.DefaultTrustStorePath(config.DefaultHome))
}

// inspectMCPServers connects to every enabled server and reports what it
// advertises, alongside its trust state.
//
// Connecting is unavoidable here: the manifest is the thing being approved and
// only the server can supply it. Failures are per-server and reported as such —
// an unreachable server must not stop the others being listed, because the most
// likely reason an operator runs this command is that one of them is broken.
func inspectMCPServers(ctx context.Context, cfg *config.Config, trust *mcp.TrustStore) []output.MCPServer {
	names := make([]string, 0, len(cfg.MCP.Servers))
	for name := range cfg.MCP.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]output.MCPServer, 0, len(names))
	for _, name := range names {
		sc := cfg.MCP.Servers[name]
		entry := output.MCPServer{Name: name, Enabled: sc.Enabled, Command: sc.Command}
		if !sc.Enabled || sc.Command == "" {
			entry.State = output.MCPDisabled
			entries = append(entries, entry)
			continue
		}

		infos, err := listMCPTools(ctx, name, sc)
		if err != nil {
			entry.State = output.MCPUnreachable
			// A connection error quotes the command line and whatever the
			// server wrote to stderr, either of which can carry a credential
			// from the server's env block. The JSON form skips the redacting
			// writer, so it is redacted here at construction.
			entry.Error = redact.String(err.Error())
			entries = append(entries, entry)
			continue
		}

		for _, info := range infos {
			entry.Tools = append(entry.Tools, output.MCPTool{
				Name:        info.Name,
				Description: redact.String(info.Description),
			})
		}
		if trust.IsTrusted(name, infos) {
			entry.State = output.MCPApproved
		} else {
			entry.State = output.MCPPending
		}
		entries = append(entries, entry)
	}
	return entries
}

// listMCPTools connects one configured server and returns its manifest,
// closing the process before returning. The client owns a bounded context, so
// a server that never answers fails rather than hanging the command.
func listMCPTools(ctx context.Context, name string, sc config.MCPServerConfig) ([]mcp.ToolInfo, error) {
	env := make([]string, 0, len(sc.Env))
	for k, v := range sc.Env {
		env = append(env, k+"="+v)
	}
	client := mcp.NewClient(mcp.Server{Name: name, Command: sc.Command, Args: sc.Args, Env: env})
	defer client.Close()

	connectCtx, cancel := context.WithTimeout(ctx, mcpCLITimeout)
	defer cancel()

	if err := client.Connect(connectCtx); err != nil {
		return nil, err
	}
	return client.ListTools(connectCtx)
}

func runMCPList(c *cli.Context) error {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}
	trust, err := mcpTrustStoreForCLI()
	if err != nil {
		return err
	}

	format, err := outputFormat(c)
	if err != nil {
		return err
	}

	doc := output.NewMCPServers(inspectMCPServers(c.Context, cfg, trust))

	// JSON bypasses the redacting writer — it rewrites encoded name/value pairs
	// and turns the document into something no parser accepts.
	if format == output.JSON {
		return output.WriteJSON(jsonWriter(), doc)
	}
	output.RenderMCPServersText(reportWriter(), doc)
	return nil
}

func runMCPTrust(c *cli.Context) error {
	name := c.Args().First()
	if name == "" {
		return fmt.Errorf("give an MCP server name; run 'joshbot mcp list' to see them")
	}
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}
	sc, ok := cfg.MCP.Servers[name]
	if !ok {
		return fmt.Errorf("no MCP server named %q is configured", name)
	}
	trust, err := mcpTrustStoreForCLI()
	if err != nil {
		return err
	}

	// Approve what the server advertises right now. If it is unreachable there
	// is nothing to approve: recording a digest we could not read would mean
	// approving a manifest sight unseen, which is the one thing this gate
	// exists to prevent.
	infos, err := listMCPTools(c.Context, name, sc)
	if err != nil {
		return fmt.Errorf("could not read the tool list from %q, so there is nothing to approve: %w", name, err)
	}
	if err := trust.Trust(name, infos); err != nil {
		return err
	}
	fmt.Printf("Approved %s (%d tool(s)).\n", name, len(infos))
	fmt.Println("If the server changes what it advertises, approval is revoked until you review it again.")
	return nil
}

func runMCPUntrust(c *cli.Context) error {
	name := c.Args().First()
	if name == "" {
		return fmt.Errorf("give an MCP server name to revoke")
	}
	trust, err := mcpTrustStoreForCLI()
	if err != nil {
		return err
	}
	if err := trust.Untrust(name); err != nil {
		return err
	}
	fmt.Printf("Revoked %s; its tools will not be used until approved again.\n", name)
	return nil
}

// mcpAddEnvName is the shape of an environment variable name. The value may
// be anything; the name may not.
var mcpAddEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateMCPServerName refuses a name that could not be a key.
//
// The name is a key in the config and part of the server's identity in the
// trust store, so a name carrying a path separator or a control character
// escapes into a path or into a message the operator reads.
func validateMCPServerName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("give the MCP server a name")
	}
	if len(name) > 64 {
		return fmt.Errorf("the MCP server name %q is too long (at most 64 characters)", name)
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("the MCP server name %q must not contain a path separator", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("the MCP server name %q must not contain control characters", name)
		}
	}
	return nil
}

// parseMCPEnv turns KEY=VALUE pairs into the map the config stores.
//
// A pair without an "=" is refused rather than stored as a key with an empty
// value: the server would then start with a variable nobody can account for.
func parseMCPEnv(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("environment entry %q is not KEY=VALUE", pair)
		}
		key = strings.TrimSpace(key)
		if !mcpAddEnvName.MatchString(key) {
			return nil, fmt.Errorf("environment variable name %q is not a valid name (letters, digits and underscore, not starting with a digit)", key)
		}
		env[key] = value
	}
	return env, nil
}

// runMCPAdd adds a new MCP server to the configuration.
//
// The server is configured, not approved: a configured server is inert until an
// operator reviews the tools it advertises, and adding one here must not be a
// way around that gate.
func runMCPAdd(c *cli.Context) error {
	args := c.Args().Slice()
	if len(args) < 2 {
		return fmt.Errorf("give the MCP server a name and the command to run; run 'joshbot mcp add --help' for details")
	}
	name, command := args[0], args[1]
	if err := validateMCPServerName(name); err != nil {
		return err
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("give the command to run for the MCP server %q", name)
	}

	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}
	if _, ok := cfg.MCP.Servers[name]; ok {
		return fmt.Errorf("an MCP server named %q is already configured", name)
	}

	env, err := parseMCPEnv(c.StringSlice("env"))
	if err != nil {
		return err
	}

	// The server's own arguments: those given after the command, plus any
	// given with --arg. --arg is the way to pass one that begins with a dash.
	serverArgs := append([]string(nil), args[2:]...)
	serverArgs = append(serverArgs, c.StringSlice("arg")...)

	sc := config.MCPServerConfig{
		Command: command,
		Args:    serverArgs,
		Env:     env,
		Enabled: !c.Bool("disabled"),
	}

	// A config with no MCP section has a nil map, and writing to a nil map
	// panics. This is the first-run case: the config was written before the
	// operator ever thought about MCP.
	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = map[string]config.MCPServerConfig{}
	}
	cfg.MCP.Servers[name] = sc

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("Added MCP server %q (command: %s).\n", name, command)
	fmt.Println("It is configured but not approved: its tools are not offered to the model until you approve them.")
	fmt.Printf("Run 'joshbot mcp list' to see what it advertises, then 'joshbot mcp trust %s' to approve it.\n", name)
	return nil
}

// mcpCommand builds the `joshbot mcp` command tree.
func mcpCommand() *cli.Command {
	return &cli.Command{
		Name:  "mcp",
		Usage: "Review and approve MCP servers",
		Description: "An MCP server supplies tool names, descriptions and schemas that go into\n" +
			"the agent's context and become callable, so a configured server is inert\n" +
			"until you approve what it advertises. Approval is bound to that list: a\n" +
			"server that changes it is revoked until you review it again.",
		Subcommands: []*cli.Command{
			{
				Name:   "list",
				Usage:  "List configured MCP servers, their tools and whether they are approved",
				Action: withJSONErrors(runMCPList),
			},
			{
				Name:      "trust",
				Usage:     "Approve an MCP server's current tool list after reviewing it",
				ArgsUsage: "<server name>",
				Action:    runMCPTrust,
			},
			{
				Name:      "untrust",
				Usage:     "Revoke approval for an MCP server",
				ArgsUsage: "<server name>",
				Action:    runMCPUntrust,
			},
			{
				Name:      "add",
				Usage:     "Add a new MCP server to the configuration",
				ArgsUsage: "<name> <command> [args...]",
				Description: "Flags come before the name: 'joshbot mcp add --env KEY=VALUE <name> <command>'.\n" +
					"The server is configured but not approved — its tools are not offered to the\n" +
					"model until 'joshbot mcp trust <name>' says so.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "arg",
						Usage: "an argument for the server's command; repeat for more (use this for an argument that starts with a dash)",
					},
					&cli.StringSliceFlag{
						Name:  "env",
						Usage: "an environment variable for the server, as KEY=VALUE; repeat for more",
					},
					&cli.BoolFlag{
						Name:  "disabled",
						Usage: "add the server without enabling it",
					},
				},
				Action: runMCPAdd,
			},
		},
	}
}
