package driftscan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/tools"
)

// TestFilterUnverifiedOmitsItemsWithEmptyEvidencePath pins the fail-closed
// evidence rule (requirement D.2): an item with no evidence_path must never
// survive, no matter how confident or well-formed the rest of it looks.
// subagent.OutputSchema cannot express this per-item constraint (it only
// validates flat top-level keys), so this Go-side filter is the actual
// enforcement point.
func TestFilterUnverifiedOmitsItemsWithEmptyEvidencePath(t *testing.T) {
	cases := []struct {
		name string
		item DriftItem
		keep bool
	}{
		{
			name: "has evidence path",
			item: DriftItem{DocPath: "README.md", Claim: "binary size", EvidencePath: "ls -la joshbot"},
			keep: true,
		},
		{
			name: "empty evidence path",
			item: DriftItem{DocPath: "README.md", Claim: "binary size", EvidencePath: ""},
			keep: false,
		},
		{
			name: "whitespace-only evidence path",
			item: DriftItem{DocPath: "README.md", Claim: "binary size", EvidencePath: "   "},
			keep: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := FilterUnverified([]DriftItem{tc.item})
			got := len(out) == 1
			if got != tc.keep {
				t.Fatalf("FilterUnverified(%+v): kept=%v, want keep=%v", tc.item, got, tc.keep)
			}
		})
	}
}

// TestFilterUnverifiedMixedList proves the filter works item-by-item, not
// all-or-nothing: a list carrying both a verified and an unverified claim
// must keep exactly the verified one.
func TestFilterUnverifiedMixedList(t *testing.T) {
	items := []DriftItem{
		{DocPath: "README.md", Claim: "a", EvidencePath: "go build ./..."},
		{DocPath: "AGENTS.md", Claim: "b", EvidencePath: ""},
		{DocPath: "docs/INSTALL.md", Claim: "c", EvidencePath: "wc -l foo.go"},
	}
	out := FilterUnverified(items)
	if len(out) != 2 {
		t.Fatalf("expected 2 surviving items, got %d: %+v", len(out), out)
	}
	for _, it := range out {
		if strings.TrimSpace(it.EvidencePath) == "" {
			t.Fatalf("filtered list still contains an item with no evidence: %+v", it)
		}
	}
}

// TestOutputSchemaRequiresDriftItems confirms the top-level schema rejects
// output missing the drift_items key — the shape-level half of the contract,
// separate from the per-item evidence rule FilterUnverified enforces.
func TestOutputSchemaRequiresDriftItems(t *testing.T) {
	schema := OutputSchema()

	if err := schema.Validate(`{"something_else": []}`); err == nil {
		t.Fatal("expected an error for output missing drift_items")
	}
	if err := schema.Validate(`{"drift_items": "not an array"}`); err == nil {
		t.Fatal("expected an error for drift_items with the wrong type")
	}
	if err := schema.Validate(`{"drift_items": []}`); err != nil {
		t.Fatalf("expected an empty drift_items array to validate, got: %v", err)
	}
	if err := schema.Validate(`not json at all`); err == nil {
		t.Fatal("expected an error for non-JSON output")
	}
}

// TestParseReportStripsCodeFence mirrors subagent's own tolerance for a
// model that wraps its JSON answer in a ```json fence despite being told
// not to.
func TestParseReportStripsCodeFence(t *testing.T) {
	raw := "```json\n{\"drift_items\": [{\"doc_path\": \"README.md\", \"claim\": \"x\", \"evidence_path\": \"wc -l x\"}]}\n```"
	report, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if len(report.DriftItems) != 1 || report.DriftItems[0].DocPath != "README.md" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestParseReportRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseReport("not json"); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

// TestScanWritesOnlyTheReportFile proves the scanner's one and only write is
// the report itself, per requirement D.3/D.5: it must never touch
// config.json, skills.trust, mcp.trust, or any other governed file. This
// exercises the real openat-contained filesystem tool (internal/tools),
// not a stand-in, so the containment guarantee is genuinely under test.
func TestScanWritesOnlyTheReportFile(t *testing.T) {
	ws := t.TempDir()

	// Seed the workspace with files that must survive untouched, standing in
	// for the governed files this scanner must never write to.
	seed := map[string]string{
		"config.json":       `{"providers":{}}`,
		"skills.trust":      "trust-data",
		"notes/keep-me.txt": "do not touch",
	}
	for rel, content := range seed {
		full := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("seed mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("seed write: %v", err)
		}
	}

	before := snapshotTree(t, ws)

	fsTool := tools.NewFilesystemTool(ws, true)
	write := func(ctx context.Context, relPath, content string) error {
		res, _ := (&registryAdapter{tool: fsTool}).ExecuteWithContext(ctx, "filesystem", map[string]any{
			"operation": "write_file",
			"path":      relPath,
			"content":   content,
		})
		return res.Error
	}

	date := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	items := []DriftItem{
		{DocPath: "README.md", Claim: "binary size", CurrentText: "19MB", ProposedText: "20MB", EvidencePath: "ls -la joshbot"},
	}
	relPath, err := WriteReport(context.Background(), write, items, date)
	if err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if relPath != "reports/doc-drift-2026-09-09.md" {
		t.Fatalf("unexpected report path: %s", relPath)
	}

	after := snapshotTree(t, ws)

	// Every seeded file must be byte-identical, and the only addition must be
	// the report itself.
	for rel, want := range seed {
		got, ok := after[rel]
		if !ok {
			t.Fatalf("seeded file %s disappeared", rel)
		}
		if got != want {
			t.Fatalf("seeded file %s was modified: got %q want %q", rel, got, want)
		}
	}

	added := map[string]bool{}
	for rel := range after {
		if _, existed := before[rel]; !existed {
			added[rel] = true
		}
	}
	if len(added) != 1 || !added[relPath] {
		t.Fatalf("expected exactly one new file (%s), got additions: %v", relPath, added)
	}
	if !strings.Contains(after[relPath], "binary size") {
		t.Fatalf("report content missing expected claim: %s", after[relPath])
	}
}

// registryAdapter is the tiny slice of tools.Registry's ExecuteWithContext
// shape this test needs, applied directly to a single FilesystemTool instead
// of standing up a whole Registry — kept local to the test so this package
// need not depend on Registry construction to prove containment.
type registryAdapter struct {
	tool *tools.FilesystemTool
}

func (r *registryAdapter) ExecuteWithContext(ctx context.Context, name string, args map[string]any) (tools.ToolResult, bool) {
	return r.tool.Execute(ctx, args), false
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// TestRenderMarkdownEmptyIsStillWellFormed proves "nothing verified" is a
// real, useful answer rather than a blank or missing file.
func TestRenderMarkdownEmptyIsStillWellFormed(t *testing.T) {
	date := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	md := RenderMarkdown(nil, date)
	if !strings.Contains(md, "2026-09-09") {
		t.Fatalf("expected date in report: %s", md)
	}
	if !strings.Contains(md, "No verified drift") {
		t.Fatalf("expected an explicit no-drift statement: %s", md)
	}
}

// TestRenderMarkdownSortsByDocPath keeps report ordering stable and
// deterministic regardless of the order the model emitted items in.
func TestRenderMarkdownSortsByDocPath(t *testing.T) {
	date := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	items := []DriftItem{
		{DocPath: "z.md", Claim: "z", EvidencePath: "cmd"},
		{DocPath: "a.md", Claim: "a", EvidencePath: "cmd"},
	}
	md := RenderMarkdown(items, date)
	aIdx := strings.Index(md, "a.md")
	zIdx := strings.Index(md, "z.md")
	if aIdx < 0 || zIdx < 0 || aIdx > zIdx {
		t.Fatalf("expected a.md before z.md in report:\n%s", md)
	}
}

func TestBuildPromptStatesFailClosedRule(t *testing.T) {
	p := BuildPrompt("")
	if !strings.Contains(strings.ToLower(p), "omit") {
		t.Fatalf("expected the prompt to instruct the model to omit unverifiable claims: %s", p)
	}
}

func TestBuildPromptIncludesSourceSummary(t *testing.T) {
	p := BuildPrompt("this checkout is a shallow clone at commit abc123")
	if !strings.Contains(p, "abc123") {
		t.Fatalf("expected source summary to be included: %s", p)
	}
}

func TestReportRelPathAndFileName(t *testing.T) {
	date := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if got := ReportFileName(date); got != "doc-drift-2026-01-02.md" {
		t.Fatalf("ReportFileName: got %s", got)
	}
	if got := ReportRelPath(date); got != "reports/doc-drift-2026-01-02.md" {
		t.Fatalf("ReportRelPath: got %s", got)
	}
}
