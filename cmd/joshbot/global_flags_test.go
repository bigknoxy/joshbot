package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/bigknoxy/joshbot/internal/log"
)

// The global Before hook runs for every subcommand, so anything it gets wrong
// is wrong everywhere. Two behaviours matter. A misspelled --log-level must be
// a validation error, not a silent fall-through to the configured level: an
// operator who typed --log-level debgu and saw a normal run would conclude the
// bug they are chasing produces no debug output. And --verbose has to actually
// move the level, since it is the flag every bug report asks for.

func withLogLevel(t *testing.T) {
	t.Helper()
	prev := log.Get().Logger.GetLevel()
	t.Cleanup(func() { log.Get().Logger.SetLevel(prev) })
}

func TestLogLevelTypoIsAValidationErrorNotASilentDefault(t *testing.T) {
	withLogLevel(t)
	withTempHome(t)

	out, code := runCLI(t, "--log-level", "debgu", "status")
	if code != exitValidation {
		t.Errorf("exit code = %d, want %d for an unparseable log level\n%s", code, exitValidation, out)
	}
	if log.Get().Logger.GetLevel() == log.DebugLevel {
		t.Error("a rejected level still changed the logger")
	}
}

func TestVerboseTurnsOnDebugLogging(t *testing.T) {
	withLogLevel(t)
	withTempHome(t)
	log.Get().Logger.SetLevel(log.InfoLevel)

	runCLI(t, "--verbose", "status")
	if got := log.Get().Logger.GetLevel(); got != log.DebugLevel {
		t.Errorf("level = %v after --verbose, want debug; every bug report asks for this flag", got)
	}
}

// An explicit --log-level wins over the --verbose shorthand. Letting the
// shorthand override would make --verbose --log-level warn quietly noisy.
func TestExplicitLogLevelBeatsVerbose(t *testing.T) {
	withLogLevel(t)
	withTempHome(t)
	log.Get().Logger.SetLevel(log.InfoLevel)

	runCLI(t, "--verbose", "--log-level", "warn", "status")
	if got := log.Get().Logger.GetLevel(); got != log.WarnLevel {
		t.Errorf("level = %v, want warn; the explicit flag must win", got)
	}
}

// --no-color exists for output piped to a file or a CI log. Accepting the flag
// and still emitting escapes is the failure that matters, so assert the colour
// profile both renderers actually consult.
func TestNoColorStripsTheColourProfile(t *testing.T) {
	withLogLevel(t)
	withTempHome(t)

	prevLip := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(prevLip) })
	lipgloss.SetColorProfile(termenv.TrueColor)

	out, code := runCLI(t, "--no-color", "status")
	if code != 0 {
		t.Errorf("exit code = %d, want 0 with --no-color\n%s", code, out)
	}
	if got := lipgloss.ColorProfile(); got != termenv.Ascii {
		t.Errorf("lipgloss profile = %v after --no-color, want Ascii", got)
	}
}
