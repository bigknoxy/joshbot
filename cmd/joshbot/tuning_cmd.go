package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/redact"
	"github.com/bigknoxy/joshbot/internal/tools"
	"github.com/bigknoxy/joshbot/internal/tuning"
)

// Operator-facing commands for the narrow per-tool timeout auto-tuner
// (internal/tuning): inspect what it has learned and, if needed, revert its
// adjustments. The tuner itself is off by default (tuning.enabled) and does
// nothing unless an operator opts in — these commands work regardless,
// since a fresh install with tuning off still has an (empty) overlay and
// event history to report on truthfully.

// tuningOut is the redacted writer every subcommand prints through, mirroring
// sessionsOut — overridable in tests.
var tuningOut = func() io.Writer { return redact.Writer(os.Stdout) }

// tuningPaths returns the events and overlay file paths under cfg's home
// directory.
func tuningPaths(cfg *config.Config) (eventsPath, overlayPath string) {
	home := cfg.HomeDir()
	return filepath.Join(home, tuning.EventsFileName), filepath.Join(home, tuning.OverlayFileName)
}

// tunerForCLI loads the config and constructs a Tracker+Tuner from it,
// mirroring sessionManagerForCLI's shape.
func tunerForCLI(c *cli.Context) (*tuning.Tuner, *config.Config, error) {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return nil, nil, err
	}

	eventsPath, overlayPath := tuningPaths(cfg)
	tracker, err := tuning.NewTracker(eventsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open tuning event history: %w", err)
	}
	tunerCfg := tuning.DefaultTunerConfig(cfg.Tuning.MaxBump.Duration(), cfg.Tuning.Step.Duration())
	tuner, err := tuning.NewTuner(tracker, overlayPath, tunerCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open tuning overlay: %w", err)
	}
	return tuner, cfg, nil
}

// baseWebTimeout returns the configured (or, absent that, the tool's real
// package default — tools.DefaultWebOperationTimeout, the single source of
// truth also used by internal/tools and by the tuner wiring in main.go)
// timeout for one of the four tunable web operations.
func baseWebTimeout(cfg *config.Config, tool string) time.Duration {
	var configured config.Duration
	switch tool {
	case "web_search":
		configured = cfg.Tools.Web.SearchTimeout
	case "web_research":
		configured = cfg.Tools.Web.ResearchTimeout
	case "web_code":
		configured = cfg.Tools.Web.CodeTimeout
	case "web_company":
		configured = cfg.Tools.Web.CompanyTimeout
	default:
		return 0
	}
	if d := configured.Duration(); d > 0 {
		return d
	}
	return tools.DefaultWebOperationTimeout(tool)
}

// tuningCommand builds the `joshbot tuning` command group.
func tuningCommand() *cli.Command {
	return &cli.Command{
		Name:  "tuning",
		Usage: "Inspect and reset the per-tool web-search timeout auto-tuner",
		Description: "The tuner (tools.web.*_timeout auto-adjustment) is off by default\n" +
			"(tuning.enabled). When enabled, it raises a web tool's per-call timeout\n" +
			"after a run of real timeouts, and lowers it back after a run of\n" +
			"successes, within an operator-declared bound (tuning.max_bump). Its\n" +
			"adjustment lives in a small overlay file under ~/.joshbot, never in\n" +
			"config.json.",
		Subcommands: []*cli.Command{
			{
				Name:   "status",
				Usage:  "Show each tool's effective timeout, recent failure rate and tune history",
				Action: runTuningStatus,
			},
			{
				Name:  "reset",
				Usage: "Clear tuned adjustments, reverting every tool to its configured timeout",
				Description: "Reset clears the overlay only. The persisted event history it is\n" +
					"computed from is an append-only log and is left untouched — a marker\n" +
					"event is appended instead of truncating it.",
				Action: runTuningReset,
			},
		},
	}
}

func runTuningStatus(c *cli.Context) error {
	tuner, cfg, err := tunerForCLI(c)
	if err != nil {
		return err
	}

	out := tuningOut()
	status := "disabled"
	if cfg.Tuning.Enabled {
		status = "enabled"
	}
	fmt.Fprintf(out, "Tuning: %s (max_bump=%s, step=%s)\n\n",
		status, cfg.Tuning.MaxBump.Duration(), cfg.Tuning.Step.Duration())

	overlay := tuner.OverlaySnapshot()
	for _, tool := range tuning.Tools {
		base := baseWebTimeout(cfg, tool)
		entry := overlay[tool]
		effective := base + entry.Bump()
		rate, n := tuner.Stats(tool, tuning.DefaultWindow)

		fmt.Fprintf(out, "%s\n", tool)
		fmt.Fprintf(out, "  base timeout:      %s\n", base)
		if entry.Bump() > 0 {
			fmt.Fprintf(out, "  tuned bump:        +%s (effective %s)\n", entry.Bump(), effective)
			fmt.Fprintf(out, "  last tuned:        %s (step %d)\n", entry.TunedAt.Format(time.RFC3339), entry.StepCount)
		} else {
			fmt.Fprintf(out, "  tuned bump:        none (effective %s)\n", effective)
		}
		if n > 0 {
			fmt.Fprintf(out, "  recent failure rate: %.0f%% (%d samples, %s window)\n",
				rate*100, n, tuning.DefaultWindow)
		} else {
			fmt.Fprintf(out, "  recent failure rate: no samples yet\n")
		}
		fmt.Fprintln(out)
	}

	recent := tuner.RecentEvents(10)
	if len(recent) > 0 {
		fmt.Fprintf(out, "Recent events (newest first):\n")
		for _, ev := range recent {
			outcome := "ok"
			if ev.TimedOut {
				outcome = "timed out"
			}
			fmt.Fprintf(out, "  %s  %-15s %s\n", ev.Time.Format(time.RFC3339), ev.Tool, outcome)
		}
	}

	return nil
}

func runTuningReset(c *cli.Context) error {
	tuner, _, err := tunerForCLI(c)
	if err != nil {
		return err
	}
	if err := tuner.Reset(); err != nil {
		return fmt.Errorf("failed to reset tuning overlay: %w", err)
	}
	fmt.Fprintln(tuningOut(), "Tuning overlay cleared; every tool reverts to its configured timeout.")
	return nil
}
