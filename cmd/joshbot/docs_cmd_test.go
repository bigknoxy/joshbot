package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/driftscan"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/subagent"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// TestDocsCommandIsWiredIntoCommandsTable pins that `joshbot docs check` is a
// real subcommand, the way TestCLICommandNamesAllHaveHandlers pins the
// slash-command table for every channel.
func TestDocsCommandIsWiredIntoCommandsTable(t *testing.T) {
	app := newApp()
	var docs *cli.Command
	for _, c := range app.Commands {
		if c.Name == "docs" {
			docs = c
			break
		}
	}
	if docs == nil {
		t.Fatal("newApp() has no \"docs\" command")
	}
	found := false
	for _, s := range docs.Subcommands {
		if s.Name == "check" {
			found = true
		}
	}
	if !found {
		t.Fatalf("docs command has no \"check\" subcommand, got: %+v", docs.Subcommands)
	}
}

// scriptedDocsRunner is a fixed-answer docsCheckRunner: it never calls a
// real provider or executes a real tool, so these tests exercise
// performDocsCheck's own logic (parsing, fail-closed filtering, report
// writing) without needing a live LLM or shell access wired up.
type scriptedDocsRunner struct {
	output string
	err    error
}

func (r *scriptedDocsRunner) Run(ctx context.Context, prompt string, cfg subagent.Config) (*subagent.SubResult, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &subagent.SubResult{Output: r.output}, nil
}

// testWorkspaceWriter builds a driftscan.FileWriter backed by the real
// openat-contained filesystem tool (internal/tools), rooted at a temp
// directory, mirroring exactly how docs_cmd.go wires the real one through
// tools.Registry — so these tests exercise the actual containment path, not
// a stand-in that could silently diverge from it.
func testWorkspaceWriter(t *testing.T, workspace string) driftscan.FileWriter {
	t.Helper()
	fsTool := tools.NewFilesystemTool(workspace, true)
	return func(ctx context.Context, relPath, content string) error {
		res := fsTool.Execute(ctx, map[string]any{
			"operation": "write_file",
			"path":      relPath,
			"content":   content,
		})
		return res.Error
	}
}

// TestDocsCheckOmitsUnverifiedClaimEndToEnd is the primary integration-level
// fail-closed falsifier (requirement D.2): a drift item the model reported
// with no evidence_path — standing in for a verification command that
// failed or was never actually run — must never appear in the written
// report, while a properly-evidenced item alongside it survives untouched.
func TestDocsCheckOmitsUnverifiedClaimEndToEnd(t *testing.T) {
	ws := t.TempDir()

	scripted := &scriptedDocsRunner{output: `{"drift_items": [
		{"doc_path": "README.md", "claim": "binary size is stale", "current_text": "19MB", "proposed_text": "21MB", "evidence_path": "ls -la joshbot"},
		{"doc_path": "AGENTS.md", "claim": "unverifiable guess about LOC", "current_text": "", "proposed_text": "", "evidence_path": ""}
	]}`}

	relPath, n, err := performDocsCheck(context.Background(), scripted, testWorkspaceWriter(t, ws), "")
	if err != nil {
		t.Fatalf("performDocsCheck: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 surviving item, got %d", n)
	}

	data, rerr := os.ReadFile(filepath.Join(ws, relPath))
	if rerr != nil {
		t.Fatalf("reading written report: %v", rerr)
	}
	report := string(data)

	if !strings.Contains(report, "binary size is stale") {
		t.Fatalf("verified claim missing from report:\n%s", report)
	}
	if strings.Contains(report, "unverifiable guess about LOC") {
		t.Fatalf("unverified claim leaked into the report:\n%s", report)
	}
}

// TestDocsCheckReportPathAndFormat pins the exact report location and that
// its content is well-formed markdown naming the evidence for a surviving
// claim.
func TestDocsCheckReportPathAndFormat(t *testing.T) {
	ws := t.TempDir()

	scripted := &scriptedDocsRunner{output: `{"drift_items": [
		{"doc_path": "docs/INSTALL.md", "claim": "install command changed", "current_text": "old", "proposed_text": "new", "evidence_path": "go build ./cmd/joshbot"}
	]}`}

	relPath, n, err := performDocsCheck(context.Background(), scripted, testWorkspaceWriter(t, ws), "")
	if err != nil {
		t.Fatalf("performDocsCheck: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 item, got %d", n)
	}
	if !strings.HasPrefix(relPath, "reports/doc-drift-") || !strings.HasSuffix(relPath, ".md") {
		t.Fatalf("unexpected report path: %s", relPath)
	}

	data, rerr := os.ReadFile(filepath.Join(ws, relPath))
	if rerr != nil {
		t.Fatalf("reading written report: %v", rerr)
	}
	report := string(data)
	if !strings.HasPrefix(report, "# Documentation drift report") {
		t.Fatalf("report missing expected heading:\n%s", report)
	}
	if !strings.Contains(report, "go build ./cmd/joshbot") {
		t.Fatalf("report missing evidence command:\n%s", report)
	}
}

// TestDocsCheckEmptyReportWhenNothingSurvives proves a report that omits
// every claim (because none had evidence) is still written and well-formed,
// not treated as a scan failure.
func TestDocsCheckEmptyReportWhenNothingSurvives(t *testing.T) {
	ws := t.TempDir()
	scripted := &scriptedDocsRunner{output: `{"drift_items": [
		{"doc_path": "README.md", "claim": "no evidence", "evidence_path": ""}
	]}`}

	relPath, n, err := performDocsCheck(context.Background(), scripted, testWorkspaceWriter(t, ws), "")
	if err != nil {
		t.Fatalf("performDocsCheck: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 surviving items, got %d", n)
	}
	data, rerr := os.ReadFile(filepath.Join(ws, relPath))
	if rerr != nil {
		t.Fatalf("reading written report: %v", rerr)
	}
	if !strings.Contains(string(data), "No verified drift found") {
		t.Fatalf("expected an explicit no-drift statement:\n%s", data)
	}
}

// TestDocsCheckSurfacesRunnerError proves a subagent failure is reported as
// an error rather than silently producing an empty "no drift" report — the
// two situations must be distinguishable to an operator.
func TestDocsCheckSurfacesRunnerError(t *testing.T) {
	ws := t.TempDir()
	scripted := &scriptedDocsRunner{err: context.DeadlineExceeded}

	if _, _, err := performDocsCheck(context.Background(), scripted, testWorkspaceWriter(t, ws), ""); err == nil {
		t.Fatal("expected an error when the subagent run itself fails")
	}
}

// TestDocsCheckSurfacesUnparseableOutput proves malformed subagent output is
// reported as an error, never coerced into a silent empty report.
func TestDocsCheckSurfacesUnparseableOutput(t *testing.T) {
	ws := t.TempDir()
	scripted := &scriptedDocsRunner{output: "not json at all"}

	if _, _, err := performDocsCheck(context.Background(), scripted, testWorkspaceWriter(t, ws), ""); err == nil {
		t.Fatal("expected an error for unparseable subagent output")
	}
}

// fakeToolExecutor is a minimal subagent.ToolExecutor stand-in that reports
// a fixed schema list and records which tool name it was asked to execute,
// so scopedDocsExecutor's filtering can be exercised without a real
// tools.Registry.
type fakeToolExecutor struct {
	schemas []providers.Tool
	called  string
}

func (f *fakeToolExecutor) GetSchemas() []providers.Tool { return f.schemas }

func (f *fakeToolExecutor) ExecuteWithContext(ctx context.Context, name string, args map[string]any, channel, channelID string, callback func(subagent.AsyncResult)) (subagent.ToolResult, bool) {
	f.called = name
	return subagent.ToolResult{Output: "ok"}, false
}

func toolSchema(name string) providers.Tool {
	return providers.Tool{Type: "function", Function: providers.FunctionDefinition{Name: name}}
}

// TestScopedDocsExecutorFiltersSchemas proves the drift-scan subagent is
// never even offered a governed-file mutator like joshbot_config or
// send_file — closing the gap where the package doc's "never given a path
// to config.json..." claim was true only of the report-writing closure, not
// of the subagent's own tool-calling loop wired with the raw, unrestricted
// registry.
func TestScopedDocsExecutorFiltersSchemas(t *testing.T) {
	inner := &fakeToolExecutor{schemas: []providers.Tool{
		toolSchema("read_file"),
		toolSchema("list_dir"),
		toolSchema("glob"),
		toolSchema("grep"),
		toolSchema("shell"),
		toolSchema("filesystem"),
		toolSchema("write_file"),
		toolSchema("edit_file"),
		toolSchema("joshbot_config"),
		toolSchema("send_file"),
		toolSchema("cron"),
		toolSchema("message"),
	}}
	scoped := &scopedDocsExecutor{inner: inner}

	got := map[string]bool{}
	for _, s := range scoped.GetSchemas() {
		got[s.Function.Name] = true
	}

	for name := range driftScanAllowedTools {
		if !got[name] {
			t.Errorf("allowed tool %q missing from filtered schema list", name)
		}
	}
	for name := range got {
		if !driftScanAllowedTools[name] {
			t.Errorf("disallowed tool %q leaked into the filtered schema list", name)
		}
	}
	if got["joshbot_config"] {
		t.Error("joshbot_config must never be offered to the drift-scan subagent")
	}
}

// TestScopedDocsExecutorRefusesDisallowedCalls proves the enforcement point
// is ExecuteWithContext, not just the schema list: a model that calls
// joshbot_config anyway (hallucinated, or recalled from training) must be
// refused rather than served, and an allowed tool must still pass through.
func TestScopedDocsExecutorRefusesDisallowedCalls(t *testing.T) {
	inner := &fakeToolExecutor{}
	scoped := &scopedDocsExecutor{inner: inner}

	res, _ := scoped.ExecuteWithContext(context.Background(), "joshbot_config", map[string]any{"operation": "set"}, "cli", "", nil)
	if res.Error == nil {
		t.Fatal("expected an error refusing joshbot_config, got nil")
	}
	if inner.called != "" {
		t.Fatalf("joshbot_config must never reach the underlying executor, but it was called with %q", inner.called)
	}

	res, _ = scoped.ExecuteWithContext(context.Background(), "read_file", map[string]any{"path": "README.md"}, "cli", "", nil)
	if res.Error != nil {
		t.Fatalf("expected read_file to pass through, got error: %v", res.Error)
	}
	if inner.called != "read_file" {
		t.Fatalf("expected the underlying executor to be called with read_file, got %q", inner.called)
	}
}
