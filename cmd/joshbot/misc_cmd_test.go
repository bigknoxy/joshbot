package main

import (
	"context"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/config"
)

func TestRunVersionPrintsTheBuildVersion(t *testing.T) {
	out, code := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, Version) {
		t.Errorf("version output %q does not contain %q", out, Version)
	}
}

// A service that was never installed reports an empty status string. Printing a
// bare "Status: " told the operator nothing; the command must name the state.
func TestServiceStatusNamesTheStateWhenNotInstalled(t *testing.T) {
	out, code := runCLI(t, "service", "status")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0. Output:\n%s", code, out)
	}
	if strings.Contains(out, "Status: \n") {
		t.Errorf("printed an empty status line:\n%s", out)
	}
	if !strings.Contains(out, "Status:") {
		t.Errorf("no status line at all:\n%s", out)
	}
}

// closeMCPServers runs from a defer on every agent path, including ones where
// no server was ever started.
func TestCloseMCPServersWithNothingRunningIsSafe(t *testing.T) {
	closeMCPServers()
	closeMCPServers()
}

// A reminder delivered to a CLI session nobody is sitting at is lost, so a
// configured chat channel wins over "cli".
func TestDefaultReminderChannelPrefersAChatChannel(t *testing.T) {
	cfg := config.Defaults()
	if got := defaultReminderChannel(cfg); got != "cli" {
		t.Errorf("with no channel enabled = %q, want %q", got, "cli")
	}

	cfg.Channels.Discord.Enabled = true
	if got := defaultReminderChannel(cfg); got != "discord" {
		t.Errorf("with Discord enabled = %q, want %q", got, "discord")
	}

	cfg.Channels.Telegram.Enabled = true
	if got := defaultReminderChannel(cfg); got != "telegram" {
		t.Errorf("with both enabled = %q, want %q; Telegram is the documented winner", got, "telegram")
	}

	if got := defaultReminderChannel(nil); got != "cli" {
		t.Errorf("nil config = %q, want %q", got, "cli")
	}
}

// The first signal must both cancel the context and close done — a blocking
// read inside a select with a default makes shutdown unobservable, which is
// issue #104. signal.Notify also disables Go's default termination, so the
// handler must keep listening afterwards rather than being one-shot; that half
// is not signalled twice here (a second SIGINT exits the test process by
// design), but the loop is what makes the process killable.
func TestSetupGracefulShutdownCancelsAndSignalsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})

	setupGracefulShutdown(ctx, cancel, done)
	// Put default signal handling back before any other test runs, so a later
	// SIGINT is not swallowed by this handler.
	t.Cleanup(func() { signal.Reset(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP) })

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done was never closed: shutdown is unobservable to the caller")
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("context was not cancelled on the first signal")
	}
}
