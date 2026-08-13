package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/api"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
)

func serveCommand() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Start the OpenAI-compatible HTTP API",
		Description: "Serves POST /v1/chat/completions and GET /v1/models so any client that\n" +
			"speaks the OpenAI chat API can use joshbot as a backend. A request runs the\n" +
			"full agent — tools, memory and skills — so the answer is joshbot's, not a\n" +
			"pass-through of an upstream provider.\n\n" +
			"Authentication is mandatory and there is no unauthenticated mode: set\n" +
			"api.api_keys in config.json, or JOSHBOT_API__API_KEYS as a comma-separated\n" +
			"list. The default bind address is loopback, because a caller reaching this\n" +
			"endpoint reaches the shell and filesystem tools.",
		Flags: []cli.Flag{
			profileFlag(),
			&cli.StringFlag{
				Name:  "listen",
				Usage: "Bind address, host:port (overrides api.listen)",
			},
		},
		Action: runServe,
	}
}

func runServe(c *cli.Context) error {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}
	if err := applyProfile(c, cfg); err != nil {
		return err
	}
	if !cfg.UseModelsConfig() && len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured. Run 'joshbot onboard' first")
	}

	listen := cfg.API.Listen
	if flag := c.String("listen"); flag != "" {
		listen = flag
	}
	if listen == "" {
		listen = config.DefaultAPIListen
	}

	_, _, _, agentInstance, _, _, err := setupComponents(cfg)
	defer closeMCPServers()
	if err != nil {
		return err
	}

	// New fails when no API key is configured, before anything is listening —
	// so a misconfigured server never accepts a single unauthenticated request.
	srv, err := api.New(agentInstance, api.Options{
		Listen:  listen,
		APIKeys: cfg.API.APIKeys,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	setupGracefulShutdown(ctx, cancel, done)

	// Serve blocks, so the shutdown signal has to reach it through the context
	// rather than through `done` — this goroutine is the only thing joining the
	// two lifecycles.
	go func() {
		<-done
		cancel()
	}()

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║        joshbot API server running         ║")
	fmt.Printf("║  Listen: %-33s ║\n", listen)
	fmt.Printf("║  Model:  %-33s ║\n", api.ModelID)
	fmt.Println("║                                           ║")
	fmt.Println("║  Press Ctrl+C to stop                     ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	if err := srv.Serve(ctx); err != nil {
		return err
	}
	log.Info("API server stopped")
	return nil
}
