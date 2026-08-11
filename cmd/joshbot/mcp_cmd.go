package main

import (
	"context"
	"fmt"
	"sort"
	"time"

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
		},
	}
}
