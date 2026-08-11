package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/output"
)

// walkCommands visits every command and subcommand, depth first, passing the
// dotted path ("sessions.prune") alongside it.
func walkCommands(t *testing.T, prefix string, cmds []*cli.Command, fn func(path string, c *cli.Command)) {
	t.Helper()
	for _, c := range cmds {
		path := c.Name
		if prefix != "" {
			path = prefix + "." + c.Name
		}
		fn(path, c)
		walkCommands(t, path, c.Subcommands, fn)
	}
}

// A command registered without an Action does not fail — urfave/cli prints
// help and exits 0. That is the exits-0-over-a-failed-state class this package
// has shipped twice: `joshbot <newcommand>` would look like it ran.
// A command with no Usage is invisible in `--help`, which is the only place
// the command list is discoverable.
func TestEveryCommandHasAnActionAndUsage(t *testing.T) {
	walkCommands(t, "", newApp().Commands, func(path string, c *cli.Command) {
		if c.Name == "help" {
			return
		}
		if c.Action == nil && len(c.Subcommands) == 0 {
			t.Errorf("command %q has neither an Action nor subcommands: invoking it exits 0 doing nothing", path)
		}
		if strings.TrimSpace(c.Usage) == "" {
			t.Errorf("command %q has no Usage, so it is undocumented in --help", path)
		}
	})
}

// urfave/cli resolves a name to the first command that claims it and never
// reports the collision, so a duplicate name or alias makes the newer command
// permanently unreachable.
func TestCommandNamesAndAliasesAreUnique(t *testing.T) {
	seen := map[string]string{} // "parent/name" -> first path that claimed it

	var check func(parent string, cmds []*cli.Command)
	check = func(parent string, cmds []*cli.Command) {
		for _, c := range cmds {
			for _, n := range append([]string{c.Name}, c.Aliases...) {
				key := parent + "/" + n
				if first, dup := seen[key]; dup {
					t.Errorf("%q is claimed by both %q and %q; the second is unreachable",
						n, first, parent+"."+c.Name)
					continue
				}
				seen[key] = parent + "." + c.Name
			}
			check(parent+"."+c.Name, c.Subcommands)
		}
	}
	check("", newApp().Commands)
}

// The command list is a documented interface (README.md, docs/INSTALL.md) and
// scripts invoke it by name. Dropping or renaming one of these is a breaking
// change that nothing else in the test suite would notice.
func TestDocumentedCommandsExist(t *testing.T) {
	want := []string{
		"agent", "gateway", "onboard", "status", "preflight", "skills",
		"sessions", "configure", "update", "version", "uninstall", "service", "auth",
	}
	have := map[string]bool{}
	for _, c := range newApp().Commands {
		have[c.Name] = true
		for _, a := range c.Aliases {
			have[a] = true
		}
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("documented command %q is not registered", name)
		}
	}
	// `config` is the documented alias for `configure`.
	if !have["config"] {
		t.Error("the documented alias `config` for `configure` is gone")
	}
}

// The subcommands that take both a positional argument and a flag are the ones
// urfave/cli v2 mis-parses (it stops at the first positional), which
// parseSessionArgs exists to work around. If a new flag is added to one of
// these without teaching parseSessionArgs about it, `prune <id> --newflag`
// silently reports an unknown flag or drops it. Pin the set so that addition
// has to be deliberate.
func TestSessionsSubcommandFlagsAreKnownToTheTrailingParser(t *testing.T) {
	known := map[string]bool{"force": true, "last": true, "older-than": true}

	var sessions *cli.Command
	for _, c := range newApp().Commands {
		if c.Name == "sessions" {
			sessions = c
		}
	}
	if sessions == nil {
		t.Fatal("sessions command is gone")
	}
	for _, sub := range sessions.Subcommands {
		if sub.ArgsUsage == "" {
			continue // takes no positional, so urfave parses its flags fine
		}
		for _, f := range sub.Flags {
			name := f.Names()[0]
			if !known[name] {
				t.Errorf("sessions %s has flag --%s, which parseSessionArgs does not handle: "+
					"`sessions %s <id> --%s` will be dropped or rejected",
					sub.Name, name, sub.Name, name)
			}
		}
	}
}

// --output is advertised in help text; a format added to internal/output that
// is not listed here is undiscoverable, and a format listed here that
// output.ParseFormat rejects is a documented value that fails at runtime.
func TestGlobalOutputFlagAdvertisesEveryFormat(t *testing.T) {
	var usage string
	for _, f := range newApp().Flags {
		if f.Names()[0] == "output" {
			usage = f.String()
		}
	}
	if usage == "" {
		t.Fatal("the global --output flag is gone")
	}
	for _, format := range output.Formats {
		if !strings.Contains(usage, format) {
			t.Errorf("--output help does not mention %q: %s", format, usage)
		}
		if _, err := output.ParseFormat(format); err != nil {
			t.Errorf("advertised format %q is rejected by ParseFormat: %v", format, err)
		}
	}
}

// `joshbot --help` must exit 0 and `joshbot <typo>` must not: a script that
// misspells a command has to be able to tell.
func TestUnknownCommandDoesNotExitZero(t *testing.T) {
	app := newApp()
	var buf bytes.Buffer
	app.Writer, app.ErrWriter = &buf, &buf
	app.ExitErrHandler = func(*cli.Context, error) {}
	app.CommandNotFound = nil

	if err := app.Run([]string{"joshbot", "--help"}); err != nil {
		t.Errorf("--help exited non-zero: %v", err)
	}
	if !strings.Contains(buf.String(), "COMMANDS:") {
		t.Errorf("--help did not print the command list:\n%s", buf.String())
	}

	err := app.Run([]string{"joshbot", "notacommand"})
	if err == nil {
		t.Fatal("an unknown command exited 0")
	}
	if code := exitCodeOf(t, err); code == 0 {
		t.Errorf("unknown command produced exit code %d", code)
	}
}
