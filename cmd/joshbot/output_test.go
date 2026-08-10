package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/output"
)

// runReportCmd invokes one of the reporting commands the way the real app
// does — global --output flag, JSON error wrapper, exit codes intact — and
// returns its stdout and its error.
func runReportCmd(t *testing.T, action cli.ActionFunc, args ...string) (string, error) {
	t.Helper()
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.PathFlag{Name: "config"},
			&cli.StringFlag{Name: "output", Value: string(output.Text)},
		},
		Action: withJSONErrors(action),
		Writer: io.Discard,
		// urfave/cli calls os.Exit on a cli.ExitCoder by default, which would
		// take the test binary down with it. The error is still returned.
		ExitErrHandler: func(*cli.Context, error) {},
	}
	var err error
	out := captureStdout(t, func() {
		err = app.Run(append([]string{"joshbot"}, args...))
	})
	return out, err
}

// exitCodeOf reports the process exit status an error would produce.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var coder cli.ExitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return codeForError(err)
}

// A typo in --output must be distinguishable from a command that ran and
// reported a problem, or a script cannot tell "I invoked you wrong" from
// "your config is broken".
func TestOutputFlag_InvalidValueIsAValidationError(t *testing.T) {
	_, err := runReportCmd(t, runPreflight, "--config", workingConfig(t), "--output", "yaml")
	if err == nil {
		t.Fatal("an unknown --output value was accepted")
	}
	if got := exitCodeOf(t, err); got != exitValidation {
		t.Errorf("exit code = %d, want %d (exitValidation)", got, exitValidation)
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("error does not name the rejected value: %v", err)
	}
}

// --output text must be indistinguishable from passing no flag at all: the
// flag exists for scripts, and every existing human invocation omits it.
func TestOutputFlag_TextMatchesNoFlag(t *testing.T) {
	cfg := workingConfig(t)
	bare, err1 := runReportCmd(t, runPreflight, "--config", cfg)
	explicit, err2 := runReportCmd(t, runPreflight, "--config", cfg, "--output", "text")
	if err1 != nil || err2 != nil {
		t.Fatalf("preflight failed: %v / %v", err1, err2)
	}
	if bare != explicit {
		t.Errorf("--output text differs from no flag:\n%s\n---\n%s", bare, explicit)
	}
}

func TestPreflightJSON(t *testing.T) {
	out, err := runReportCmd(t, runPreflight, "--config", workingConfig(t), "--output", "json")
	if err != nil {
		t.Fatalf("preflight on a working config: %v\n%s", err, out)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("preflight --output json is not valid JSON: %v\n%s", err, out)
	}
	if doc["schema_version"] != float64(output.SchemaVersion) {
		t.Errorf("schema_version = %v, want %d", doc["schema_version"], output.SchemaVersion)
	}
	if doc["ok"] != true {
		t.Errorf("ok = %v, want true:\n%s", doc["ok"], out)
	}
	// The human framing has no business in a machine document.
	if strings.Contains(out, "OK — joshbot would start.") {
		t.Errorf("JSON output carries the text rendering:\n%s", out)
	}
}

// The exit status is the whole point of preflight in a script. JSON mode must
// not turn a failing config into a successful invocation just because the
// document was written successfully.
func TestPreflightJSON_FailingConfigStillExitsNonZero(t *testing.T) {
	path := writePreflightConfig(t, `{"providers": {"openrouter": {"api_key": "`+preflightKey+`"}}}`)
	out, err := runReportCmd(t, runPreflight, "--config", path, "--output", "json")
	if err == nil {
		t.Fatalf("preflight exited 0 on a config that would not work:\n%s", out)
	}
	if got := exitCodeOf(t, err); got == 0 {
		t.Errorf("exit code = 0 on a failing config")
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if doc["ok"] != false {
		t.Errorf("ok = %v, want false:\n%s", doc["ok"], out)
	}
}

// These documents exist to be pasted into an issue or piped into a script. A
// configured credential must not survive either trip — in either format.
func TestPreflightJSON_NeverPrintsTheCredential(t *testing.T) {
	out, err := runReportCmd(t, runPreflight, "--config", workingConfig(t), "--output", "json")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if strings.Contains(out, preflightKey) {
		t.Errorf("preflight --output json printed the API key:\n%s", out)
	}
}

func TestStatusJSON_NeverPrintsTheCredential(t *testing.T) {
	out, err := runReportCmd(t, runStatus, "--config", workingConfig(t), "--output", "json")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, preflightKey) {
		t.Errorf("status --output json printed the API key:\n%s", out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("status --output json is not valid JSON: %v\n%s", err, out)
	}
	if doc["schema_version"] != float64(output.SchemaVersion) {
		t.Errorf("schema_version = %v", doc["schema_version"])
	}
}

// A JSON consumer asked for JSON on stdout; discovering *why* the command
// failed should not require attaching a second reader to stderr.
func TestWithJSONErrors_EmitsAnErrorDocument(t *testing.T) {
	failing := func(*cli.Context) error {
		return newExitError(exitAuth, "run joshbot auth", errors.New("not authenticated"))
	}
	out, err := runReportCmd(t, failing, "--output", "json")
	if err == nil {
		t.Fatal("failing action reported success")
	}
	if got := exitCodeOf(t, err); got != exitAuth {
		t.Errorf("exit code = %d, want %d", got, exitAuth)
	}

	var doc output.ErrorDoc
	if jsonErr := json.Unmarshal([]byte(out), &doc); jsonErr != nil {
		t.Fatalf("error output is not valid JSON: %v\n%s", jsonErr, out)
	}
	if doc.Error.Code != exitAuth {
		t.Errorf("error.code = %d, want %d", doc.Error.Code, exitAuth)
	}
	if !strings.Contains(doc.Error.Message, "not authenticated") {
		t.Errorf("error.message = %q", doc.Error.Message)
	}
}

// In text mode the wrapper is a no-op: urfave/cli prints the error itself, and
// a stray JSON document on stdout would corrupt human output.
func TestWithJSONErrors_TextModeEmitsNothing(t *testing.T) {
	failing := func(*cli.Context) error { return errors.New("boom") }
	out, err := runReportCmd(t, failing)
	if err == nil {
		t.Fatal("failing action reported success")
	}
	if out != "" {
		t.Errorf("text mode wrote to stdout:\n%s", out)
	}
}

// The JSON documents deliberately bypass the redacting writer (it corrupts
// encoded JSON), so every path they carry has to be stripped as the document is
// built. A raw path leaks the account name into something meant to be pasted
// into an issue, and these documents exist to be pasted into issues.
func TestStatusJSON_DoesNotLeakTheHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		t.Skip("no usable home directory to check against")
	}
	// A workspace under the home directory is the default in a real install;
	// the other tests here use a temp dir, which cannot catch this.
	cfg := writePreflightConfig(t, `{
	  "providers": {"openrouter": {"enabled": true, "api_key": "`+preflightKey+`"}},
	  "agents": {"defaults": {"workspace": "`+filepath.Join(home, ".joshbot-status-leak-test")+`"}},
	  "models_config": {
	    "agent": {"model": "openrouter/x"},
	    "models": [{"name": "openrouter/x", "model": "openrouter/x", "api_key": "`+preflightKey+`"}]
	  }
	}`)

	out, err := runReportCmd(t, runStatus, "--config", cfg, "--output", "json")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, home) {
		t.Errorf("status --output json printed the home directory %q:\n%s", home, out)
	}
	if !strings.Contains(out, "~/.joshbot-status-leak-test") {
		t.Errorf("status --output json did not report the workspace at all:\n%s", out)
	}
}
