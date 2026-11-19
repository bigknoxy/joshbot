package main

import (
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/session"
)

// The point of `sessions switch` and `sessions resume` is that the choice
// outlives the command that made it: the next conversation picks it up.
// These tests drive the commands the way a user does, and then check the
// recorded file - not the printed line.

func TestSessionsSwitchChoosesASessionAndRecordsIt(t *testing.T) {
	home := useTempHome(t)
	configPath, sessionsDir := sessionsEnv(t)
	seedCLISession(t, sessionsDir, "cli:alpha", "hello")
	seedCLISession(t, sessionsDir, "cli:beta", "hi")

	out, code := runSessionsCmd(t, configPath, "switch", "cli:beta")
	if code != 0 {
		t.Fatalf("sessions switch exited %d: %s", code, out)
	}
	if !strings.Contains(out, "cli:beta") {
		t.Errorf("the output does not name the chosen session:\n%s", out)
	}
	if !strings.Contains(out, "Start 'joshbot agent'") {
		t.Errorf("the output does not say what to do next:\n%s", out)
	}

	if got := readCurrentSession(home); got != "cli:beta" {
		t.Errorf("the recorded session is %q, want %q", got, "cli:beta")
	}
	if got := currentCLISessionKey(); got != "cli:beta" {
		t.Errorf("the interactive CLI would use %q, want %q", got, "cli:beta")
	}
}

// A user who does not know the id can pick by the number the list printed.
func TestSessionsSwitchTakesAListNumber(t *testing.T) {
	home := useTempHome(t)
	configPath, sessionsDir := sessionsEnv(t)
	seedCLISession(t, sessionsDir, "cli:older", "one")
	seedCLISession(t, sessionsDir, "cli:newer", "two")

	// The most recently updated session is the one the list prints first.
	mgr := seedCLISession(t, sessionsDir, "cli:newest", "three")
	sess, err := mgr.Load(t.Context(), "cli:newest")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sess.UpdatedAt = time.Now().Add(time.Hour)
	if err := mgr.Save(t.Context(), sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	out, code := runSessionsCmd(t, configPath, "switch", "1")
	if code != 0 {
		t.Fatalf("sessions switch exited %d: %s", code, out)
	}
	if got := readCurrentSession(home); got != "cli:newest" {
		t.Errorf("the recorded session is %q, want %q", got, "cli:newest")
	}
}

func TestSessionsSwitchRejectsASessionThatIsNotThere(t *testing.T) {
	useTempHome(t)
	configPath, sessionsDir := sessionsEnv(t)
	seedCLISession(t, sessionsDir, "cli:alpha", "hello")

	out, code := runSessionsCmd(t, configPath, "switch", "cli:nope")
	if code == 0 {
		t.Fatalf("expected a failure for an unknown session, got:\n%s", out)
	}
}

// A number that is not in the list is a mistake worth naming, not a silent
// fall back to the first session.
func TestSessionsSwitchRejectsAListNumberOutOfRange(t *testing.T) {
	useTempHome(t)
	configPath, sessionsDir := sessionsEnv(t)
	seedCLISession(t, sessionsDir, "cli:alpha", "hello")

	out, code := runSessionsCmd(t, configPath, "switch", "7")
	if code == 0 {
		t.Fatalf("expected a failure for an out-of-range number, got:\n%s", out)
	}
}

func TestResolveSessionTargetRejectsAnEmptyTarget(t *testing.T) {
	_, sessionsDir := sessionsEnv(t)
	mgr, err := session.NewManager(sessionsDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := resolveSessionTarget(mgr, t.Context(), "  "); err == nil {
		t.Fatal("expected an error for an empty target, got none")
	}
}

func TestSessionsSwitchNeedsASession(t *testing.T) {
	useTempHome(t)
	configPath, _ := sessionsEnv(t)

	out, code := runSessionsCmd(t, configPath, "switch")
	if code == 0 {
		t.Fatalf("expected a usage error, got:\n%s", out)
	}
}

// `resume` with no argument continues the most recent conversation.
func TestSessionsResumeContinuesTheMostRecentSession(t *testing.T) {
	home := useTempHome(t)
	configPath, sessionsDir := sessionsEnv(t)
	seedCLISession(t, sessionsDir, "cli:older", "one")
	mgr := seedCLISession(t, sessionsDir, "cli:newer", "two")

	sess, err := mgr.Load(t.Context(), "cli:newer")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sess.UpdatedAt = time.Now().Add(time.Hour)
	if err := mgr.Save(t.Context(), sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	out, code := runSessionsCmd(t, configPath, "resume")
	if code != 0 {
		t.Fatalf("sessions resume exited %d: %s", code, out)
	}
	if got := readCurrentSession(home); got != "cli:newer" {
		t.Errorf("the recorded session is %q, want %q", got, "cli:newer")
	}
}

// With nothing to resume, the command has to say so rather than pick
// nothing and print success.
func TestSessionsResumeWithNoSessions(t *testing.T) {
	useTempHome(t)
	configPath, _ := sessionsEnv(t)

	out, code := runSessionsCmd(t, configPath, "resume")
	if code == 0 {
		t.Fatalf("expected a failure when there is nothing to resume, got:\n%s", out)
	}
}

// `sessions current` is the way back: without it there would be no way to
// undo a choice short of deleting a file by hand.
func TestSessionsCurrentShowsTheChoiceAndResetsIt(t *testing.T) {
	home := useTempHome(t)
	configPath, _ := sessionsEnv(t)

	out, code := runSessionsCmd(t, configPath, "current")
	if code != 0 {
		t.Fatalf("sessions current exited %d: %s", code, out)
	}
	if !strings.Contains(out, defaultCLIKey) {
		t.Errorf("with no choice made, the output does not name the default session:\n%s", out)
	}

	if err := writeCurrentSession(home, "cli:chosen"); err != nil {
		t.Fatalf("writeCurrentSession: %v", err)
	}
	out, code = runSessionsCmd(t, configPath, "current")
	if code != 0 {
		t.Fatalf("sessions current exited %d: %s", code, out)
	}
	if !strings.Contains(out, "cli:chosen") {
		t.Errorf("the output does not name the chosen session:\n%s", out)
	}

	out, code = runSessionsCmd(t, configPath, "current", "--reset")
	if code != 0 {
		t.Fatalf("sessions current --reset exited %d: %s", code, out)
	}
	if got := readCurrentSession(home); got != "" {
		t.Errorf("the choice was not cleared, still %q", got)
	}
	if got := currentCLISessionKey(); got != defaultCLIKey {
		t.Errorf("the interactive CLI would use %q, want the default %q", got, defaultCLIKey)
	}
}

// The recorded key is the part after the channel prefix, and it has to
// survive a round trip through the file.
func TestCLISessionKeyRoundTrips(t *testing.T) {
	if got := cliSessionKey("someone_else"); got != "cli:someone_else" {
		t.Errorf("cliSessionKey = %q, want %q", got, "cli:someone_else")
	}
	if got := cliSessionKey("cli:someone_else"); got != "cli:someone_else" {
		t.Errorf("cliSessionKey = %q, want %q", got, "cli:someone_else")
	}
}

var _ = session.RoleUser
