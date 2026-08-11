package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/session"
)

// sessionsEnv sets up an isolated home with a config, and returns the config
// path plus the sessions directory.
func sessionsEnv(t *testing.T) (configPath, sessionsDir string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionsDir = filepath.Join(home, ".joshbot", "sessions")
	if err := os.MkdirAll(sessionsDir, 0750); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	cfg := map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"workspace": filepath.Join(home, "workspace"),
			},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	configPath = filepath.Join(home, ".joshbot", "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, sessionsDir
}

func seedCLISession(t *testing.T, dir, id string, contents ...string) *session.Manager {
	t.Helper()

	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	sess := session.NewSession(id)
	for _, c := range contents {
		sess.AddMessage(session.Message{Role: session.RoleUser, Content: c, Timestamp: time.Now().UTC()})
	}
	if err := mgr.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return mgr
}

// runSessionsCmd invokes the sessions command group and returns stdout plus the
// exit code the CLI would produce.
func runSessionsCmd(t *testing.T, configPath string, args ...string) (string, int) {
	t.Helper()

	app := &cli.App{
		Flags:                 []cli.Flag{&cli.PathFlag{Name: "config"}},
		Commands:              []*cli.Command{sessionsCommand()},
		Writer:                io.Discard,
		ErrWriter:             io.Discard,
		ExitErrHandler:        func(*cli.Context, error) {}, // do not call os.Exit
		CustomAppHelpTemplate: " ",
	}

	full := append([]string{"joshbot", "--config", configPath, "sessions"}, args...)

	var code int
	out := captureStdout(t, func() {
		err := app.Run(full)
		if err != nil {
			code = 1
			var ec cli.ExitCoder
			if ok := asExitCoder(err, &ec); ok {
				code = ec.ExitCode()
			}
		}
	})
	return out, code
}

func asExitCoder(err error, target *cli.ExitCoder) bool {
	if ec, ok := err.(cli.ExitCoder); ok {
		*target = ec
		return true
	}
	return false
}

// An empty sessions directory is a normal state, not an error.
func TestSessionsListEmptyExitsZero(t *testing.T) {
	cfg, _ := sessionsEnv(t)

	out, code := runSessionsCmd(t, cfg, "list")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(strings.ToLower(out), "no sessions") {
		t.Errorf("expected a 'no sessions' message, got:\n%s", out)
	}
}

func TestSessionsListReportsSessions(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "cli:cli_user", "hello", "world")

	out, code := runSessionsCmd(t, cfg, "list")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{"cli:cli_user", "MESSAGES", "SIZE", "UPDATED"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in listing:\n%s", want, out)
		}
	}
}

// The compaction archive ends in .jsonl but is not a session.
func TestSessionsListIgnoresArchives(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "real", "hi")
	if err := os.WriteFile(filepath.Join(dir, "real.history.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	out, _ := runSessionsCmd(t, cfg, "list")
	if strings.Contains(out, "real.history") {
		t.Errorf("the compaction archive was listed as a session:\n%s", out)
	}
}

// show must redact: a transcript is the most credential-dense thing joshbot
// stores, and this command exists to be read and pasted.
func TestSessionsShowRedactsCredentials(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	const secret = "sk-ant-api03-ShowCommandSecretAbCdEfGh"
	seedCLISession(t, dir, "leaky", "my key is "+secret)

	out, code := runSessionsCmd(t, cfg, "show", "leaky")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("show printed a credential:\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected a redaction placeholder:\n%s", out)
	}

	// The file itself is untouched.
	raw, err := os.ReadFile(filepath.Join(dir, "leaky.jsonl"))
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !strings.Contains(string(raw), secret) {
		t.Error("show altered the stored session; it must be read-only")
	}
}

func TestSessionsShowLastN(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "many", "first", "second", "third", "fourth")

	out, code := runSessionsCmd(t, cfg, "show", "many", "--last", "2")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	if strings.Contains(out, "first") || strings.Contains(out, "second") {
		t.Errorf("--last 2 showed older messages:\n%s", out)
	}
	for _, want := range []string{"third", "fourth"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// N larger than the message count is not an error.
func TestSessionsShowLastLargerThanCount(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "few", "only one")

	out, code := runSessionsCmd(t, cfg, "show", "few", "--last", "99")
	if code != 0 {
		t.Errorf("exit code = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "only one") {
		t.Errorf("expected the message in output:\n%s", out)
	}
}

func TestSessionsShowRejectsNonPositiveLast(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "any", "x")

	for _, n := range []string{"0", "-3"} {
		_, code := runSessionsCmd(t, cfg, "show", "any", "--last", n)
		if code != 2 {
			t.Errorf("--last %s exit code = %d, want 2 (usage error)", n, code)
		}
	}
}

func TestSessionsShowMissingSessionExitsNonZero(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "present", "x")

	out, code := runSessionsCmd(t, cfg, "show", "absent")
	if code == 0 {
		t.Errorf("expected a non-zero exit for a missing session, got 0:\n%s", out)
	}
}

// A traversing id must be rejected, and nothing outside the directory touched.
func TestSessionsRejectsTraversingID(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "safe", "x")

	victim := filepath.Join(filepath.Dir(dir), "victim.jsonl")
	if err := os.WriteFile(victim, []byte("precious\n"), 0600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	for _, sub := range [][]string{
		{"show", "../victim"},
		{"prune", "../victim", "--force"},
		{"new", "../victim", "--force"},
	} {
		_, code := runSessionsCmd(t, cfg, sub...)
		if code == 0 {
			t.Errorf("%v accepted a traversing session id", sub)
		}
	}

	if data, err := os.ReadFile(victim); err != nil || string(data) != "precious\n" {
		t.Errorf("a file outside the sessions directory was touched: %q %v", data, err)
	}
}

func TestSessionsPruneDeletesOne(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "doomed", "x")
	seedCLISession(t, dir, "keeper", "x")

	out, code := runSessionsCmd(t, cfg, "prune", "doomed", "--force")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "doomed.jsonl")); !os.IsNotExist(err) {
		t.Error("session was not deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "keeper.jsonl")); err != nil {
		t.Error("an unrelated session was deleted")
	}
}

// A typo must fail loudly and change nothing.
func TestSessionsPruneMissingIDDeletesNothing(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "keeper", "x")

	out, code := runSessionsCmd(t, cfg, "prune", "typo", "--force")
	if code == 0 {
		t.Errorf("expected non-zero exit for a missing session:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "keeper.jsonl")); err != nil {
		t.Error("an unrelated session was deleted")
	}
}

func TestSessionsPruneOlderThan(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	for _, id := range []string{"ancient", "stale", "fresh"} {
		seedCLISession(t, dir, id, "x")
	}
	now := time.Now()
	chtime(t, filepath.Join(dir, "ancient.jsonl"), now.Add(-90*24*time.Hour))
	chtime(t, filepath.Join(dir, "stale.jsonl"), now.Add(-45*24*time.Hour))
	chtime(t, filepath.Join(dir, "fresh.jsonl"), now.Add(-time.Hour))

	out, code := runSessionsCmd(t, cfg, "prune", "--older-than", "30d", "--force")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}
	for _, gone := range []string{"ancient", "stale"} {
		if _, err := os.Stat(filepath.Join(dir, gone+".jsonl")); !os.IsNotExist(err) {
			t.Errorf("%s should have been pruned", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.jsonl")); err != nil {
		t.Error("the recent session was pruned")
	}
}

func TestSessionsPruneRejectsBadDuration(t *testing.T) {
	cfg, _ := sessionsEnv(t)

	for _, spec := range []string{"0 9 * * *", "banana", "-5m"} {
		_, code := runSessionsCmd(t, cfg, "prune", "--older-than", spec, "--force")
		if code != 2 {
			t.Errorf("--older-than %q exit code = %d, want 2", spec, code)
		}
	}
}

func TestSessionsPruneRequiresAnArgument(t *testing.T) {
	cfg, _ := sessionsEnv(t)

	if _, code := runSessionsCmd(t, cfg, "prune"); code != 2 {
		t.Errorf("bare prune exit code = %d, want 2", code)
	}
	if _, code := runSessionsCmd(t, cfg, "prune", "an-id", "--older-than", "30d"); code != 2 {
		t.Errorf("prune with both id and --older-than exit code = %d, want 2", code)
	}
}

// Pruning a session removes its quarantine copy too.
//
// The quarantine exists so an unreadable load does not silently destroy a
// conversation — it survives loads, not an explicit delete. Leaving it behind
// would mean `sessions prune` reported a session gone while a verbatim
// transcript of it stayed on disk, which is the opposite of what an operator
// running prune is asking for.
func TestSessionsPruneRemovesQuarantineFile(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "damaged", "x")
	quarantine := filepath.Join(dir, "damaged.jsonl.corrupt")
	if err := os.WriteFile(quarantine, []byte("torn\n"), 0600); err != nil {
		t.Fatalf("seed quarantine: %v", err)
	}

	if _, code := runSessionsCmd(t, cfg, "prune", "damaged", "--force"); code != 0 {
		t.Fatal("prune failed")
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Errorf("prune left the quarantined transcript on disk (err=%v)", err)
	}
}

func TestSessionsNewArchivesAndEmpties(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "cli:cli_user", "remember this")

	out, code := runSessionsCmd(t, cfg, "new", "cli:cli_user", "--force")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}

	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	sess, err := mgr.GetOrCreate(context.Background(), "cli:cli_user")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if len(sess.Messages) != 0 {
		t.Errorf("expected an empty session, got %d messages", len(sess.Messages))
	}

	// The old conversation still exists somewhere.
	entries, _ := os.ReadDir(dir)
	var archived bool
	for _, e := range entries {
		if strings.Contains(e.Name(), ".jsonl.archive-") {
			archived = true
		}
	}
	if !archived {
		t.Error("previous history was destroyed rather than archived")
	}
}

// Without a terminal, a destructive command must decline rather than block
// waiting for input that will never arrive.
func TestDestructiveCommandsDeclineWithoutTTYOrForce(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "survivor", "x")

	// Non-zero on refusal: an unattended run that gets no confirmation did not
	// do the work, and exit 0 would tell a script that it did.
	out, code := runSessionsCmd(t, cfg, "prune", "survivor")
	if code == 0 {
		t.Fatalf("a refusal exited 0, which a script reads as success:\n%s", out)
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("expected the refusal to mention --force:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "survivor.jsonl")); err != nil {
		t.Error("the session was deleted without confirmation")
	}
}

func chtime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

// urfave/cli v2 stops flag parsing at the first positional argument, so this
// parser handles the flags that trail the session id. Both orders must work:
// `prune id --force` silently declining is a footgun, and `show id --last 2`
// silently printing the whole transcript is a privacy one.
func TestParseSessionArgs(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		allowLast      bool
		allowOlderThan bool
		want           sessionArgs
		wantErr        bool
	}{
		{"id only", []string{"my-id"}, false, false, sessionArgs{id: "my-id"}, false},
		{"id then force", []string{"my-id", "--force"}, false, false, sessionArgs{id: "my-id", force: true}, false},
		{"force then id", []string{"--force", "my-id"}, false, false, sessionArgs{id: "my-id", force: true}, false},
		{"last spaced", []string{"my-id", "--last", "5"}, true, false, sessionArgs{id: "my-id", last: 5, lastSet: true}, false},
		{"last equals", []string{"my-id", "--last=5"}, true, false, sessionArgs{id: "my-id", last: 5, lastSet: true}, false},
		{"last negative parses", []string{"my-id", "--last", "-3"}, true, false, sessionArgs{id: "my-id", last: -3, lastSet: true}, false},
		{"empty", nil, false, false, sessionArgs{}, false},

		{"last when not allowed", []string{"my-id", "--last", "5"}, false, false, sessionArgs{}, true},
		{"last without value", []string{"my-id", "--last"}, true, false, sessionArgs{}, true},
		{"last non-numeric", []string{"my-id", "--last", "five"}, true, false, sessionArgs{}, true},
		{"unknown flag", []string{"my-id", "--forse"}, false, false, sessionArgs{}, true},
		{"two positionals", []string{"a", "b"}, false, false, sessionArgs{}, true},

		// The flag that the original parser did not know about: prune takes
		// three flags, and --older-than after an id was reported as unknown.
		{"older-than spaced", []string{"an-id", "--older-than", "30d"}, false, true,
			sessionArgs{id: "an-id", olderThan: "30d", olderThanSet: true}, false},
		{"older-than equals", []string{"an-id", "--older-than=30d"}, false, true,
			sessionArgs{id: "an-id", olderThan: "30d", olderThanSet: true}, false},
		{"older-than when not allowed", []string{"an-id", "--older-than", "30d"}, false, false, sessionArgs{}, true},
		{"older-than without value", []string{"an-id", "--older-than"}, false, true, sessionArgs{}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSessionArgs(tc.args, tc.allowLast, tc.allowOlderThan, false)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A typo in a flag must fail loudly rather than being treated as the id.
func TestSessionsRejectsUnknownTrailingFlag(t *testing.T) {
	cfg, dir := sessionsEnv(t)
	seedCLISession(t, dir, "safe", "x")

	if _, code := runSessionsCmd(t, cfg, "prune", "safe", "--forse"); code != 2 {
		t.Errorf("unknown flag exit code = %d, want 2", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "safe.jsonl")); err != nil {
		t.Error("session was deleted despite a bad flag")
	}
}

// Both flag orders must behave identically.
func TestSessionsFlagOrderIndependence(t *testing.T) {
	for _, args := range [][]string{
		{"show", "many", "--last", "2"},
		{"show", "--last", "2", "many"},
	} {
		cfg, dir := sessionsEnv(t)
		seedCLISession(t, dir, "many", "first", "second", "third", "fourth")

		out, code := runSessionsCmd(t, cfg, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d:\n%s", args, code, out)
		}
		if strings.Contains(out, "first") {
			t.Errorf("%v: --last was ignored, whole transcript printed:\n%s", args, out)
		}
	}
}

// `prune <id> --older-than 30d` must reach the mutual-exclusion check.
//
// urfave/cli v2 drops flags after a positional, and parseSessionArgs originally
// knew only --force and --last, so this reported `unknown flag "--older-than"`
// — wrong twice: the flag exists, and the real complaint is that the two ways
// of choosing what to delete cannot be combined.
func TestPruneRejectsIdAndOlderThanInEitherOrder(t *testing.T) {
	cfg, _ := sessionsEnv(t)

	for _, args := range [][]string{
		{"prune", "an-id", "--older-than", "30d"},
		{"prune", "--older-than", "30d", "an-id"},
	} {
		out, code := runSessionsCmd(t, cfg, args...)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2\n%s", args, code, out)
		}
		if strings.Contains(out, "unknown flag") {
			t.Errorf("%v: reported an unknown flag for a flag that exists:\n%s", args, out)
		}
	}
}
