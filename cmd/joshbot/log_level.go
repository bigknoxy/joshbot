package main

import (
	"os"
	"strings"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/urfave/cli/v2"
)

// defaultConfiguredLogLevel is the level the config carries when the user
// says nothing. It matches the logger's own default.
const defaultConfiguredLogLevel = "info"

// applyConfiguredLogLevel gives the logger the level the user asked for,
// and gives an interactive terminal session the quiet one it wants.
//
// Precedence, as the CLI's help promises: an explicit flag wins, then the
// environment, then the config file. The flags are applied in the app's
// Before hook; the config's level was parsed and validated but never reached
// the logger, so "log_level": "debug" in the config file did nothing at all.
//
// With nothing asked for, an interactive session runs at Warn. The TUI
// renders its own progress — the spinner and the ⏺/⎿ frames — and the
// logger's INFO records duplicate it, land in the middle of the conversation
// and, when the answer does not end with a newline, glue themselves to the
// end of it. A person reads the conversation; the records are for --verbose.
//
// The config file's "info" is the built-in default rather than a request:
// `joshbot configure` writes it into every config it saves, so honouring it
// would leave every configured install as noisy as it was.
func applyConfiguredLogLevel(c *cli.Context, cfg *config.Config) {
	// An explicit flag was already applied by the Before hook, and it wins.
	if c.String("log-level") != "" || c.Bool("verbose") || c.Bool("debug") {
		return
	}

	level := strings.TrimSpace(cfg.LogLevel)
	// A level other than the built-in default is the user asking for it.
	explicit := level != "" && !strings.EqualFold(level, defaultConfiguredLogLevel)
	if env := strings.TrimSpace(os.Getenv("JOSHBOT_LOG_LEVEL")); env != "" {
		// The environment beats the file, as the help promises, and it is
		// always explicit — the user typed it.
		level, explicit = env, true
	}
	if explicit {
		if parsed, err := parseLogLevel(level); err == nil {
			log.SetLevel(parsed)
			return
		}
	}

	// Nothing asked for: an interactive terminal session is quiet, and
	// everything else keeps the logger's default.
	if isTTY(os.Stdout) && c.String("message") == "" {
		log.SetLevel(log.WarnLevel)
	}
}
