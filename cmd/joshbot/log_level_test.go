package main

import (
	"flag"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/urfave/cli/v2"
)

// levelContext builds a context with the global log flags unset, the way the
// agent command sees them when the user passes nothing.
func levelContext(t *testing.T) *cli.Context {
	t.Helper()
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "log-level"},
			&cli.BoolFlag{Name: "verbose"},
			&cli.BoolFlag{Name: "debug"},
			&cli.StringFlag{Name: "message"},
		},
	}
	set := flag.NewFlagSet("test", 0)
	for _, f := range app.Flags {
		f.Apply(set)
	}
	return cli.NewContext(app, set, nil)
}

// The TUI renders its own progress; the logger's INFO records duplicate it
// and land in the middle of the conversation. With nothing asked for, an
// interactive session is quiet (#505).
func TestApplyConfiguredLogLevelQuietsTheInteractiveSession(t *testing.T) {
	restore := log.GetLevel()
	defer log.SetLevel(restore)
	log.SetLevel(log.InfoLevel)
	withTTY(t, true)

	cfg := &config.Config{LogLevel: "info"}
	applyConfiguredLogLevel(levelContext(t), cfg)
	if got := log.GetLevel(); got != log.WarnLevel {
		t.Errorf("log level = %v, want warn", got)
	}
}

// An explicit flag wins over everything else.
func TestApplyConfiguredLogLevelHonoursAnExplicitFlag(t *testing.T) {
	restore := log.GetLevel()
	defer log.SetLevel(restore)
	log.SetLevel(log.InfoLevel)

	c := levelContext(t)
	if err := c.Set("log-level", "debug"); err != nil {
		t.Fatal(err)
	}
	// The Before hook applies the flag before this runs; emulate that.
	log.SetLevel(log.DebugLevel)
	applyConfiguredLogLevel(c, &config.Config{LogLevel: "warn"})
	if got := log.GetLevel(); got != log.DebugLevel {
		t.Errorf("log level = %v, want debug (the flag must win)", got)
	}
}

// The config's level was parsed and validated but never reached the logger:
// "log_level": "debug" in the config file did nothing at all (#505).
func TestApplyConfiguredLogLevelHonoursTheConfig(t *testing.T) {
	restore := log.GetLevel()
	defer log.SetLevel(restore)
	log.SetLevel(log.InfoLevel)
	withTTY(t, true)

	applyConfiguredLogLevel(levelContext(t), &config.Config{LogLevel: "error"})
	if got := log.GetLevel(); got != log.ErrorLevel {
		t.Errorf("log level = %v, want error", got)
	}
}

// The config file's "info" is the built-in default, not a request: every
// config `joshbot configure` saves carries it, so honouring it would leave
// every configured install as noisy as it was.
func TestApplyConfiguredLogLevelTreatsTheConfigsInfoAsTheDefault(t *testing.T) {
	restore := log.GetLevel()
	defer log.SetLevel(restore)
	log.SetLevel(log.InfoLevel)
	withTTY(t, true)

	applyConfiguredLogLevel(levelContext(t), &config.Config{LogLevel: "info"})
	if got := log.GetLevel(); got != log.WarnLevel {
		t.Errorf("log level = %v, want warn", got)
	}
}

// The environment is the next precedence step after the flags, and it is
// always explicit — the user typed it.
func TestApplyConfiguredLogLevelHonoursTheEnvironment(t *testing.T) {
	restore := log.GetLevel()
	defer log.SetLevel(restore)
	log.SetLevel(log.InfoLevel)
	withTTY(t, true)

	t.Setenv("JOSHBOT_LOG_LEVEL", "error")
	applyConfiguredLogLevel(levelContext(t), &config.Config{LogLevel: "info"})
	if got := log.GetLevel(); got != log.ErrorLevel {
		t.Errorf("log level = %v, want error", got)
	}
}

// The environment can ask for the records too, even at the default level.
func TestApplyConfiguredLogLevelHonoursAnExplicitInfoFromTheEnvironment(t *testing.T) {
	restore := log.GetLevel()
	defer log.SetLevel(restore)
	log.SetLevel(log.InfoLevel)
	withTTY(t, true)

	t.Setenv("JOSHBOT_LOG_LEVEL", "info")
	applyConfiguredLogLevel(levelContext(t), &config.Config{LogLevel: "info"})
	if got := log.GetLevel(); got != log.InfoLevel {
		t.Errorf("log level = %v, want info", got)
	}
}

// A non-interactive run keeps the logger's default: a script reading stdout
// is not disturbed by the TUI's quietness.
func TestApplyConfiguredLogLevelLeavesNonInteractiveRunsAlone(t *testing.T) {
	restore := log.GetLevel()
	defer log.SetLevel(restore)
	log.SetLevel(log.InfoLevel)
	withTTY(t, true)

	c := levelContext(t)
	if err := c.Set("message", "hello"); err != nil {
		t.Fatal(err)
	}
	applyConfiguredLogLevel(c, &config.Config{LogLevel: "info"})
	if got := log.GetLevel(); got != log.InfoLevel {
		t.Errorf("log level = %v, want info", got)
	}
}
