package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bigknoxy/joshbot/internal/demo"
	"github.com/urfave/cli/v2"
)

// runDemo runs the scripted, provider-free demonstration.
//
// It is the fastest way to see what joshbot is: the real agent loop, the
// real tools, a real file written and read back - with no API key, no
// network and no account. That matters for a project whose README has to
// answer "what is this, in one line?" before anyone installs it.
func runDemo(c *cli.Context) error {
	workspace := c.String("workspace")
	if workspace == "" {
		dir, err := os.MkdirTemp("", "joshbot-demo-")
		if err != nil {
			return fmt.Errorf("create demo workspace: %w", err)
		}
		workspace = dir
		defer os.RemoveAll(dir)
	} else if err := os.MkdirAll(workspace, 0o700); err != nil {
		return fmt.Errorf("create demo workspace: %w", err)
	}

	fmt.Println()
	fmt.Println("  joshbot demo — no API key, no network, no account")
	fmt.Println("  ─────────────────────────────────────────────────")
	fmt.Println()

	answer, err := demo.Run(context.Background(), workspace, os.Stdout)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("  %-8s %s\n", "joshbot", answer)
	fmt.Println()
	fmt.Println("  That was the real agent loop: a tool call, the tool's real output,")
	fmt.Println("  and an answer built from it. Only the model's replies were scripted.")
	fmt.Println()
	fmt.Println("  Try it for real:  joshbot onboard")
	fmt.Println()
	return nil
}

func demoCommand() *cli.Command {
	return &cli.Command{
		Name:   "demo",
		Usage:  "Run a scripted demonstration — no API key, no network",
		Action: withJSONErrors(runDemo),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "workspace",
				Usage: "Directory the demo works in (a temporary one by default)",
			},
			&cli.PathFlag{
				Name:  "config",
				Usage: "Path to the config file",
			},
		},
	}
}

var _ = filepath.Join
