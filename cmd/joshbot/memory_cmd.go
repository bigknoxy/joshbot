package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/redact"
)

// Operator-facing commands for the Dream two-stage memory consolidation system
// (issue #193).
//
// Stage 1 rides on the history append and is invisible; Stage 2 otherwise only
// runs when something calls Consolidate. Without these commands an operator who
// set agents.defaults.dream_mode has no way to tell whether anything is being
// recorded, and no way to make consolidation happen — the feature would be
// indistinguishable from a no-op.

// memoryOut is the redacted writer the subcommands print through: a
// consolidated insight is derived from conversation text and can quote
// anything the user typed, including a key they pasted.
var memoryOut = func() io.Writer { return redact.Writer(os.Stdout) }

// memoryCommand builds the `joshbot memory` command group.
func memoryCommand() *cli.Command {
	return &cli.Command{
		Name:  "memory",
		Usage: "Inspect and consolidate the Dream memory system",
		Description: "Dream is a two-stage memory system: every turn is recorded to a raw\n" +
			"log, and consolidation clusters that log into durable insights that the\n" +
			"memory_search tool can surface. It is off unless\n" +
			"agents.defaults.dream_mode is set to \"record\" or \"full\".\n\n" +
			"Output is redacted: credentials and your home directory are stripped\n" +
			"before display.",
		Subcommands: []*cli.Command{
			{
				Name:   "status",
				Usage:  "Show the Dream mode, raw record count and stored insights",
				Action: runMemoryStatus,
			},
			{
				Name:   "consolidate",
				Usage:  "Run Stage 2 now, turning raw records into insights",
				Action: runMemoryConsolidate,
			},
		},
	}
}

// dreamForCLI builds a memory manager from config without starting the app, and
// returns its DreamManager. A nil manager means Dream is off — the caller has to
// say so rather than printing zeros, which read as "on and idle".
func dreamForCLI(c *cli.Context) (*memory.DreamManager, error) {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return nil, err
	}
	mode, err := memory.ParseDreamMode(cfg.Agents.Defaults.DreamMode)
	if err != nil {
		return nil, fmt.Errorf("agents.defaults.dream_mode: %w", err)
	}
	if mode == memory.DreamOff {
		return nil, nil
	}
	mgr, err := memory.New(cfg.Agents.Defaults.Workspace,
		memory.WithMaxSize(cfg.Agents.Defaults.MaxMemorySize),
		memory.WithDream(memory.WithDreamMode(mode)))
	if err != nil {
		return nil, fmt.Errorf("failed to init memory manager: %w", err)
	}
	return mgr.Dream(), nil
}

func runMemoryStatus(c *cli.Context) error {
	dm, err := dreamForCLI(c)
	if err != nil {
		return err
	}
	out := memoryOut()
	if dm == nil {
		fmt.Fprintln(out, "Dream memory: off")
		fmt.Fprintln(out, "Enable it with agents.defaults.dream_mode = \"record\" or \"full\".")
		return nil
	}

	modeName := "record"
	if dm.Mode() == memory.DreamFull {
		modeName = "full"
	}
	fmt.Fprintf(out, "Dream memory: %s\n", modeName)
	fmt.Fprintf(out, "Raw records:  %d\n", dm.CountRawRecords())

	insights, err := dm.ListConsolidated()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Insights:     %d\n", len(insights))
	now := time.Now()
	for _, in := range insights {
		fmt.Fprintf(out, "  - [%.0f%%] %s\n", in.DecayedConfidence(now)*100, in.Insight)
	}
	return nil
}

func runMemoryConsolidate(c *cli.Context) error {
	dm, err := dreamForCLI(c)
	if err != nil {
		return err
	}
	out := memoryOut()
	if dm == nil {
		return cli.Exit("Dream memory is off; set agents.defaults.dream_mode to \"record\" or \"full\" first.", 1)
	}
	// Consolidate is a no-op below DreamFull. Printing "Consolidated N records
	// into 0 insights" there is the agent -m anti-pattern: exit 0 over work
	// that never happened, while dream_raw.log grows without bound.
	if dm.Mode() != memory.DreamFull {
		return cli.Exit("Dream memory is in \"record\" mode, which only records; set agents.defaults.dream_mode to \"full\" to consolidate.", 1)
	}

	raw := dm.CountRawRecords()
	insights, err := dm.Consolidate(c.Context)
	if err != nil {
		return fmt.Errorf("consolidation failed: %w", err)
	}
	fmt.Fprintf(out, "Consolidated %d raw record(s) into %d insight(s).\n", raw, len(insights))
	now := time.Now()
	for _, in := range insights {
		fmt.Fprintf(out, "  - [%.0f%%] %s\n", in.DecayedConfidence(now)*100, in.Insight)
	}
	return nil
}
