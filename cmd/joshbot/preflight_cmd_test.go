package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/output"
	"github.com/bigknoxy/joshbot/internal/redact"
)

// writePreflightConfig writes body as a config file and returns its path.
func writePreflightConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// runPreflightWith invokes the command and returns its stdout and its error.
func runPreflightWith(t *testing.T, configPath string) (string, error) {
	t.Helper()
	app := &cli.App{
		Flags:  []cli.Flag{&cli.PathFlag{Name: "config"}},
		Action: runPreflight,
		Writer: io.Discard,
		// urfave/cli calls os.Exit on a cli.ExitCoder by default, which would
		// take the test binary down with it. The error is still returned.
		ExitErrHandler: func(*cli.Context, error) {},
	}
	var err error
	out := captureStdout(t, func() {
		err = app.Run([]string{"joshbot", "--config", configPath})
	})
	return out, err
}

const preflightKey = "sk-preflight0123456789abcdefghij"

func workingConfig(t *testing.T) string {
	t.Helper()
	return writePreflightConfig(t, `{
	  "providers": {"openrouter": {"enabled": true, "api_key": "`+preflightKey+`"}},
	  "models_config": {
	    "agent": {"model": "openrouter/anthropic/claude-sonnet-4"},
	    "models": [{"name": "openrouter/anthropic/claude-sonnet-4",
	                "model": "openrouter/anthropic/claude-sonnet-4",
	                "api_key": "`+preflightKey+`"}]
	  }
	}`)
}

// The exit status is the whole point of a preflight in a script: an agentic
// caller that has to grep the text to learn whether the config works would
// rather have run the agent.
func TestPreflightCmd_ExitStatus(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		// wantText is a phrase the operator needs to act on.
		wantText string
	}{
		{
			name:     "working config",
			body:     "",
			wantText: "OK — joshbot would start.",
		},
		{
			// The single most common joshbot misconfiguration: omitting
			// "enabled" leaves a provider that looks configured and does
			// nothing.
			name:     "provider not enabled",
			body:     `{"providers": {"openrouter": {"api_key": "` + preflightKey + `"}}}`,
			wantErr:  true,
			wantText: string(config.ProblemNotEnabled),
		},
		{
			name:     "no credential",
			body:     `{"providers": {"openrouter": {"enabled": true}}}`,
			wantErr:  true,
			wantText: string(config.ProblemNoCredential),
		},
		{
			name: "active model not defined",
			body: `{"models_config": {"agent": {"model": "openrouter/missing"},
			        "models": [{"name": "openrouter/x", "model": "openrouter/x", "api_key": "k"}]}}`,
			wantErr:  true,
			wantText: string(config.ProblemUnresolvable),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := workingConfig(t)
			if tc.body != "" {
				path = writePreflightConfig(t, tc.body)
			}

			out, err := runPreflightWith(t, path)
			if tc.wantErr && err == nil {
				t.Errorf("preflight exited 0 on a config that would not work:\n%s", out)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("preflight failed on a working config: %v\n%s", err, out)
			}
			if !strings.Contains(out, tc.wantText) {
				t.Errorf("output does not mention %q:\n%s", tc.wantText, out)
			}
		})
	}
}

// Preflight exists to be pasted into an issue, so the credential it reports on
// must never be in the report.
func TestPreflightCmd_NeverPrintsTheCredential(t *testing.T) {
	out, err := runPreflightWith(t, workingConfig(t))
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if strings.Contains(out, preflightKey) {
		t.Errorf("preflight printed the API key:\n%s", out)
	}
	// And still answers the question the operator asked.
	if !strings.Contains(out, "credential source=") {
		t.Errorf("preflight does not report where the credential came from:\n%s", out)
	}
}

// Every line this command prints goes through internal/redact, which reads
// "<secret-name><sep><value>" as an assignment anywhere it appears — including
// in ordinary prose. Two problem classes end in the word "credential", so the
// obvious "<problem>: <detail>" rendering had the redactor blank the diagnosis.
func TestPreflightEntryLines_SurviveRedaction(t *testing.T) {
	for _, problem := range []config.PreflightProblem{
		config.ProblemNoDefault,
		config.ProblemNotEnabled,
		config.ProblemNoCredential,
		config.ProblemUnresolvable,
	} {
		e := config.PreflightEntry{
			Name:             "openrouter/x",
			Role:             "active",
			Provider:         "openrouter",
			ModelID:          "x",
			CredentialSource: config.CredentialMissing,
			Problem:          problem,
			Detail:           `model "openrouter/x" (provider "openrouter") has no credential; set api_key`,
		}
		var buf bytes.Buffer
		output.RenderPreflightText(&buf, output.Preflight{
			SchemaVersion:   output.SchemaVersion,
			PreflightReport: config.PreflightReport{Entries: []config.PreflightEntry{e}},
		})
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if got := redact.String(line); got != line {
				t.Errorf("redaction rewrote the %s line:\n got  %s\n want %s", problem, got, line)
			}
		}
	}
}
