package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/incidents"
)

// incidentsEnv sets up an isolated home with a config and returns the config
// path. Mirrors tuningEnv: the CLI joins the incident log's path to the
// config's home, so an isolated HOME is what makes the command inspectable.
func incidentsEnv(t *testing.T) (configPath, home string) {
	t.Helper()

	home = t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".joshbot"), 0750); err != nil {
		t.Fatalf("mkdir home: %v", err)
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
	return configPath, home
}

func runIncidentsCmd(t *testing.T, configPath string, args ...string) (string, int) {
	t.Helper()

	app := &cli.App{
		Flags:                 []cli.Flag{&cli.PathFlag{Name: "config"}},
		Commands:              []*cli.Command{incidentsCommand()},
		Writer:                io.Discard,
		ErrWriter:             io.Discard,
		ExitErrHandler:        func(*cli.Context, error) {},
		CustomAppHelpTemplate: " ",
	}

	full := append([]string{"joshbot", "--config", configPath, "incidents"}, args...)

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

// TestIncidentsOnFreshInstallIsEmpty: no incidents.jsonl exists yet, so
// list must say so (not error) and exit 0 — a fresh install is a valid
// state, not a failure.
func TestIncidentsOnFreshInstallIsEmpty(t *testing.T) {
	cfg, _ := incidentsEnv(t)

	out, code := runIncidentsCmd(t, cfg, "list")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "No incidents recorded") {
		t.Fatalf("list on a fresh install should say so:\n%s", out)
	}
}

// TestIncidentsListShowsRecorded: incidents written directly to the JSONL
// are listed by `incidents list`, including the iteration/elapsed details
// and the (redacted) detail line.
func TestIncidentsListShowsRecorded(t *testing.T) {
	cfg, home := incidentsEnv(t)

	log, err := incidents.NewLog(filepath.Join(home, ".joshbot", incidents.FileName))
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	log.Record(incidents.Incident{
		Type:         incidents.TurnTimeout,
		Session:      "cli:user123",
		Channel:      "cli",
		Model:        "poolside/laguna-s-2.1",
		ElapsedNanos: int64(130 * time.Second),
		Iterations:   7,
		ToolCalls:    3,
		Detail:       "context deadline exceeded",
	})

	out, code := runIncidentsCmd(t, cfg, "list")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "turn_timeout") {
		t.Fatalf("list output missing the incident type:\n%s", out)
	}
	if !strings.Contains(out, "poolside/laguna-s-2.1") {
		t.Fatalf("list output missing the model:\n%s", out)
	}
	if !strings.Contains(out, "iters=7") || !strings.Contains(out, "tools=3") {
		t.Fatalf("list output missing the loop counters:\n%s", out)
	}
	if !strings.Contains(out, "elapsed=") {
		t.Fatalf("list output missing the elapsed time:\n%s", out)
	}
}

// TestIncidentsDetailIsRedactedOnOutput: a detail line carrying a credential
// must not reach the terminal through `incidents list`. The log redacts on
// Record, but the writer redacts again — defence in depth, the same reason
// every CLI output goes through redact.Writer.
func TestIncidentsDetailIsRedactedOnOutput(t *testing.T) {
	cfg, home := incidentsEnv(t)

	secret := "sk-ant-api03-verysecretvalue"
	log, err := incidents.NewLog(filepath.Join(home, ".joshbot", incidents.FileName))
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	log.Record(incidents.Incident{
		Type:   incidents.LLMFailure,
		Model:  "openrouter/x",
		Detail: "LLM call failed: Authorization: Bearer " + secret,
	})

	out, code := runIncidentsCmd(t, cfg, "list")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if strings.Contains(out, secret) {
		t.Fatal("incidents list printed the secret")
	}
	if !strings.Contains(out, "llm_failure") {
		t.Fatalf("list output missing the incident type:\n%s", out)
	}
}

// TestIncidentsSummaryCountsByType covers the trailing-window counts and the
// self-heal line, including the next-restart bump hint when one is pending.
func TestIncidentsSummaryCountsAndHealLine(t *testing.T) {
	cfg, home := incidentsEnv(t)

	// Two fresh turn timeouts inside the lookback window: the summary shows
	// both counts, and with heal_timeouts on, the pending bump.
	log, err := incidents.NewLog(filepath.Join(home, ".joshbot", incidents.FileName))
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	for i := 0; i < 2; i++ {
		log.Record(incidents.Incident{Type: incidents.TurnTimeout})
	}

	raw, _ := os.ReadFile(cfg)
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	generic["agents"].(map[string]any)["defaults"].(map[string]any)["heal_timeouts"] = "bump"
	data, _ := json.MarshalIndent(generic, "", "  ")
	if err := os.WriteFile(cfg, data, 0600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	out, code := runIncidentsCmd(t, cfg, "summary")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "turn_timeout:  2") {
		t.Fatalf("summary did not count the timeouts:\n%s", out)
	}
	if !strings.Contains(out, "bump on next restart") {
		t.Fatalf("summary did not report the pending heal bump:\n%s", out)
	}
}

// TestIncidentsClearRemovesHistory covers `incidents clear`: the file is
// removed (a truncating clear, deliberately not an audit trail) and a
// subsequent list reports empty.
func TestIncidentsClearRemovesHistory(t *testing.T) {
	cfg, home := incidentsEnv(t)

	path := filepath.Join(home, ".joshbot", incidents.FileName)
	log, err := incidents.NewLog(path)
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	log.Record(incidents.Incident{Type: incidents.LLMFailure, Detail: "boom"})

	out, code := runIncidentsCmd(t, cfg, "clear")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clear left the incidents file behind (err=%v)", err)
	}

	out, code = runIncidentsCmd(t, cfg, "list")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "No incidents recorded") {
		t.Fatalf("list after clear should be empty:\n%s", out)
	}
}

// TestIncidentsListRejectsANonPositiveLimit covers the positional-limit
// parse: a non-integer or zero limit is a usage error, not a panic.
func TestIncidentsListRejectsANonPositiveLimit(t *testing.T) {
	cfg, _ := incidentsEnv(t)

	for _, arg := range []string{"0", "-3", "abc"} {
		_, code := runIncidentsCmd(t, cfg, "list", arg)
		if code == 0 {
			t.Fatalf("list %q exited 0, want a usage failure", arg)
		}
	}
}
