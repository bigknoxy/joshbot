package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/incidents"
	"github.com/bigknoxy/joshbot/internal/redact"
)

// Operator-facing commands for the bounded turn-incident log
// (internal/incidents): show what went wrong recently and clear the record.
// Incident recording is always on (bounded, capped at incidents.maxIncidents
// entries); only self-healing is opt-in (agents.defaults.heal_timeouts), so
// these commands work on every install — a fresh one has an empty window to
// report truthfully.

// incidentsOut is the redacted writer every subcommand prints through,
// mirroring tuningOut — overridable in tests.
var incidentsOut = func() io.Writer { return redact.Writer(os.Stdout) }

// incidentsForCLI loads the config and opens the incidents log from it,
// mirroring tunerForCLI's shape.
func incidentsForCLI(c *cli.Context) (*incidents.Log, *config.Config, error) {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return nil, nil, err
	}

	path := filepath.Join(cfg.HomeDir(), incidents.FileName)
	log, err := incidents.NewLog(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open incident history: %w", err)
	}
	return log, cfg, nil
}

// incidentsCommand builds the `joshbot incidents` command group.
func incidentsCommand() *cli.Command {
	return &cli.Command{
		Name:  "incidents",
		Usage: "Inspect the bounded record of recent turn failures",
		Description: "The agent appends one bounded record per failed turn (turn timeout,\n" +
			"LLM failure, mid-stream death) to ~/.joshbot/incidents.jsonl so an\n" +
			"incident can be diagnosed without reproducing it. The window is capped\n" +
			"in memory and the file holds at most the last 500 records; this is a\n" +
			"troubleshooting aid, not an audit trail.",
		Subcommands: []*cli.Command{
			{
				Name:   "list",
				Usage:  "Show the most recent incidents, newest first",
				Action: runIncidentsList,
			},
			{
				Name:   "summary",
				Usage:  "Show counts by type over the last 24 hours and the self-heal bump",
				Action: runIncidentsSummary,
			},
			{
				Name:  "clear",
				Usage: "Delete every recorded incident",
				Description: "Clear removes the incidents file entirely — the record is a rolling\n" +
					"troubleshooting window, deliberately not an append-only audit trail,\n" +
					"so a cleared window loses nothing an operator needs to keep.",
				Action: runIncidentsClear,
			},
		},
	}
}

func runIncidentsList(c *cli.Context) error {
	log, _, err := incidentsForCLI(c)
	if err != nil {
		return err
	}

	limit := 20
	if c.Args().Len() > 0 {
		var n int
		if _, err := fmt.Sscanf(c.Args().First(), "%d", &n); err != nil || n <= 0 {
			return fmt.Errorf("invalid limit %q: must be a positive integer", c.Args().First())
		}
		limit = n
	}

	recent := log.Recent(limit, "")
	if len(recent) == 0 {
		fmt.Fprintln(incidentsOut(), "No incidents recorded.")
		return nil
	}

	fmt.Fprintf(incidentsOut(), "Recent incidents (newest first, limit %d):\n", limit)
	for _, inc := range recent {
		fmt.Fprintf(incidentsOut(), "  %s  %-12s", inc.Time.Format(time.RFC3339), inc.Type)
		if inc.Model != "" {
			fmt.Fprintf(incidentsOut(), " %s", inc.Model)
		}
		if inc.Iterations > 0 {
			fmt.Fprintf(incidentsOut(), " iters=%d tools=%d", inc.Iterations, inc.ToolCalls)
		}
		if inc.ElapsedNanos > 0 {
			fmt.Fprintf(incidentsOut(), " elapsed=%s", time.Duration(inc.ElapsedNanos).Round(time.Millisecond))
		}
		if inc.Session != "" {
			fmt.Fprintf(incidentsOut(), " session=%s", inc.Session)
		}
		fmt.Fprintln(incidentsOut())
		if inc.Detail != "" {
			fmt.Fprintf(incidentsOut(), "      %s\n", inc.Detail)
		}
	}
	return nil
}

func runIncidentsSummary(c *cli.Context) error {
	log, cfg, err := incidentsForCLI(c)
	if err != nil {
		return err
	}

	out := incidentsOut()
	fmt.Fprintf(out, "Incidents in the last %s:\n", incidents.HealLookbackWindow)
	shown := false
	for _, typ := range []string{incidents.TurnTimeout, incidents.LLMFailure, incidents.StreamDied} {
		if n := log.CountSince(incidents.HealLookbackWindow, typ); n > 0 {
			fmt.Fprintf(out, "  %-14s %d\n", typ+":", n)
			shown = true
		}
	}
	if !shown {
		fmt.Fprintln(out, "  none")
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Self-heal (agents.defaults.heal_timeouts): %s\n", cfg.Agents.Defaults.HealTimeouts)
	if bump := log.TimeoutBump(); bump > 0 {
		fmt.Fprintf(out, "  timeout bump on next restart: +%s\n", bump)
	}
	return nil
}

func runIncidentsClear(c *cli.Context) error {
	log, _, err := incidentsForCLI(c)
	if err != nil {
		return err
	}
	if err := log.Clear(); err != nil {
		return fmt.Errorf("failed to clear incident history: %w", err)
	}
	fmt.Fprintln(incidentsOut(), "Incident history cleared.")
	return nil
}
