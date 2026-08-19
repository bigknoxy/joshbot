package main

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/api"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/session"
)

func serveCommand() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Start the OpenAI-compatible HTTP API",
		Description: "Serves POST /v1/chat/completions and GET /v1/models so any client that\n" +
			"speaks the OpenAI chat API can use joshbot as a backend. A chat request runs\n" +
			"the full agent — tools, memory and skills — so the answer is joshbot's, not a\n" +
			"pass-through of an upstream provider.\n\n" +
			"POST /v1/audio/transcriptions is also served when stt.provider is set. It is\n" +
			"not the agent: it transcribes an upload with the configured speech-to-text\n" +
			"provider and returns the text. Without stt.provider it answers 501.\n\n" +
			"POST /v1/embeddings is served when embeddings.provider is set, using the\n" +
			"configured embeddings.model. It is not the agent either. Without\n" +
			"embeddings.provider it answers 501.\n\n" +
			"Authentication is mandatory and there is no unauthenticated mode: set\n" +
			"api.api_keys in config.json, or JOSHBOT_API__API_KEYS as a comma-separated\n" +
			"list. The default bind address is loopback, because a caller reaching this\n" +
			"endpoint reaches the shell and filesystem tools.\n\n" +
			"A browser chat UI is served at / when api.webui is true. It is off by\n" +
			"default: the page is a login form that accepts an api_keys value, and that\n" +
			"key reaches the shell and filesystem tools. With it off those routes 404.",
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

// resolveListen picks the bind address: --listen beats api.listen beats the
// package default.
//
// It is a function rather than three lines inside runServe because the order is
// security-relevant and nothing else would catch it getting reversed. An
// operator who narrows the bind with --listen while config.json still holds
// 0.0.0.0 must win: silently keeping the config value publishes the shell and
// filesystem tools to the local network while the command line says otherwise.
// Whitespace counts as unset, because api.New refuses a blank address and a
// config key set to " " would otherwise fail at bind time rather than fall back.
func resolveListen(flag, configured string) string {
	for _, v := range []string{flag, configured} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return config.DefaultAPIListen
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

	listen := resolveListen(c.String("listen"), cfg.API.Listen)

	_, _, sessions, agentInstance, _, _, err := setupComponents(cfg)
	defer closeMCPServers()
	defer stopBackgroundServices()
	if err != nil {
		return err
	}

	// New fails when no API key is configured, before anything is listening —
	// so a misconfigured server never accepts a single unauthenticated request.
	// Transcription is optional. A broken stt block is fatal — the same rule
	// runGateway applies — because an ignored misconfiguration leaves the
	// operator on a 501 they configured their way out of.
	var transcriber api.Transcriber
	if cfg.STT.Provider != "" {
		t, terr := buildTranscriber(cfg)
		if terr != nil {
			return fmt.Errorf("speech-to-text config: %w", terr)
		}
		transcriber = t
	}

	// A broken embeddings block is fatal for the same reason: an operator who
	// configured the route their way out of a 501 must not be silently left on
	// the 501.
	var embedder api.Embedder
	if cfg.Embeddings.Provider != "" {
		e, eerr := buildEmbedder(cfg)
		if eerr != nil {
			return fmt.Errorf("embeddings config: %w", eerr)
		}
		embedder = e
	}

	srv, err := api.New(agentInstance, api.Options{
		Listen:      listen,
		APIKeys:     cfg.API.APIKeys,
		Transcriber: transcriber,
		Embedder:    embedder,
		WebUI:       cfg.API.WebUI,
		Version:     Version,
		Transcript:  transcriptReader(sessions),
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
	fmt.Printf("║  Listen: %-32s ║\n", listen)
	fmt.Printf("║  Model:  %-32s ║\n", api.ModelID)
	if cfg.API.WebUI {
		fmt.Printf("║  Web UI: %-32s ║\n", webUIURL(listen))
	}
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

// transcriptReader adapts the session manager to the read-only view the web UI
// needs. It returns nil when there is no manager, which the server reads as "no
// history", not as an error.
//
// It is read-only on purpose: the UI has no delete route, so "New conversation"
// in the browser mints a fresh session key and leaves the old transcript where
// `joshbot sessions` can still show it. A button that silently destroyed history
// is not something to reach through a cookie.
func transcriptReader(mgr *session.Manager) api.TranscriptReader {
	if mgr == nil {
		return nil
	}
	return func(user string) ([]api.TranscriptMessage, error) {
		sess, err := mgr.Load(context.Background(), api.ChannelName+":"+user)
		if err != nil {
			return nil, err
		}
		out := make([]api.TranscriptMessage, 0, len(sess.Messages))
		for _, m := range sess.Messages {
			// Only the turns a person said or was told. Tool and system
			// messages are the agent's internals, a compaction record is an
			// envelope the model reads and not something a human wrote, and an
			// empty assistant message is a tool-call carrier with no text.
			if m.Role != session.RoleUser && m.Role != session.RoleAssistant {
				continue
			}
			if m.Compaction || strings.TrimSpace(m.Content) == "" {
				continue
			}
			out = append(out, api.TranscriptMessage{Role: string(m.Role), Content: m.Content})
		}
		return out, nil
	}
}

// webUIURL turns a bind address into something an operator can click. A wildcard
// bind is rewritten to loopback rather than printed as "0.0.0.0:8080", which is
// not an address a browser resolves.
func webUIURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen + "/"
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	// net.JoinHostPort adds the brackets an IPv6 literal needs; SplitHostPort
	// already removed them, so wrapping by hand here double-brackets it.
	return "http://" + net.JoinHostPort(host, port) + "/"
}
