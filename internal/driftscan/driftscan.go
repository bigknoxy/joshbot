// Package driftscan implements the Tier 1 "drift sentinel": a propose-only
// subagent task that reads joshbot's own source and config, compares it
// against README.md, docs/INSTALL.md, site/*.html, AGENTS.md/CLAUDE.md and
// the bundled SKILL.md files, and reports where the two have drifted apart.
//
// It is deliberately read-mostly. The subagent's only write is the report
// itself (see WriteReport); this package does not expose a path to
// config.json, skills.trust, mcp.trust or any other governed file.
//
// That confinement is enforced in cmd/joshbot, not here: this package has no
// access to a tools.Registry and cannot itself gate what the subagent may
// call. cmd/joshbot's docs_cmd.go wires the subagent's tool-calling loop
// through a scoped executor (scopedDocsExecutor / driftScanAllowedTools)
// restricted to read-only file access plus shell — never joshbot_config,
// send_file, cron, the message tools, or the filesystem tool's write/edit
// operations. Wiring this package's prompt against the raw, unrestricted
// registry an interactive chat turn gets would hand the subagent
// config.Save and arbitrary outbound sends, contradicting the read-mostly
// design stated here.
//
// The one non-negotiable design constraint, carried over verbatim from the
// panel review that approved this feature: every claim in the report MUST be
// backed by a verification command the subagent actually ran (go build,
// wc -l, go test -cover, etc., via its ordinary shell access) — never a
// guess dressed up as a fact. The prompt (BuildPrompt) asks the model to
// follow that rule itself, but the model's own claim that it verified
// something is not trusted: FilterUnverified enforces it in Go, after the
// subagent returns, by dropping any item with no evidence_path. Both layers
// exist on purpose; only the second one is load-bearing.
package driftscan

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/subagent"
)

// DriftItem is one claimed piece of documentation drift, matching the
// required output schema:
//
//	{drift_items: [{doc_path, claim, current_text, proposed_text, evidence_path}]}
type DriftItem struct {
	// DocPath is the documentation file the claim is about, e.g. "README.md".
	DocPath string `json:"doc_path"`
	// Claim is a short statement of what is stale or wrong.
	Claim string `json:"claim"`
	// CurrentText is the (or a representative) passage as it stands today.
	CurrentText string `json:"current_text"`
	// ProposedText is the suggested replacement. This package never applies
	// it — the scanner proposes, an operator or a later PR disposes.
	ProposedText string `json:"proposed_text"`
	// EvidencePath names the command actually run, or the file actually
	// read, to verify this specific claim (e.g. "go build ./cmd/joshbot",
	// "wc -l internal/agent/*.go"). This is the fail-closed enforcement
	// point: FilterUnverified drops any item where this is empty, and a
	// vague restatement of the claim here does not count as evidence — it is
	// meant to name a reproducible command, not describe one.
	EvidencePath string `json:"evidence_path"`
}

// Report is a subagent's raw parsed output. Unlike the items returned by
// FilterUnverified, a Report's DriftItems have not yet been screened for
// evidence and must never be written to a report file directly.
type Report struct {
	DriftItems []DriftItem `json:"drift_items"`
}

// OutputSchema returns the subagent.OutputSchema for a drift scan.
//
// It only pins the top-level shape: subagent.OutputSchema validates flat
// keys and their types, not the structure of array elements, so it cannot by
// itself require that every item carry a non-empty evidence_path. That
// per-item rule is FilterUnverified's job, enforced in Go after the run
// completes — never left to the schema validator or to the model's own
// prompt-following.
func OutputSchema() *subagent.OutputSchema {
	return &subagent.OutputSchema{
		Required: []string{"drift_items"},
		Types:    map[string]string{"drift_items": "array"},
	}
}

// FilterUnverified drops any item whose EvidencePath is empty or
// whitespace-only. This is the actual enforcement point for the fail-closed
// evidence rule (requirement D.2): the prompt also tells the model to omit a
// claim it cannot verify, but that instruction is advisory, and a model that
// ignores it — or hallucinates a plausible-looking evidence_path — must not
// be able to put an unverified claim in front of an operator. Only a
// non-empty EvidencePath is checked here; this package cannot itself confirm
// the named command was really run and really succeeded, so the prompt's
// instruction to only fill in evidence_path after a successful verification
// command is the other half of the contract this filter cannot see.
func FilterUnverified(items []DriftItem) []DriftItem {
	out := make([]DriftItem, 0, len(items))
	for _, it := range items {
		if strings.TrimSpace(it.EvidencePath) == "" {
			continue
		}
		out = append(out, it)
	}
	return out
}

// stripFence mirrors subagent's own unexported helper of the same name: a
// model commonly wraps its JSON answer in a ```json fence even when
// instructed not to, and rejecting the output for that alone would waste a
// repair round-trip on formatting rather than content. subagent.Runner
// already tolerates this when validating against OutputSchema, but the raw
// SubResult.Output handed back to the caller is not itself de-fenced, so
// ParseReport repeats the same trim here.
func stripFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
}

// ParseReport parses a subagent's final answer into a Report. The returned
// items are raw model output and have not been screened by FilterUnverified.
func ParseReport(output string) (Report, error) {
	var r Report
	if err := json.Unmarshal([]byte(stripFence(output)), &r); err != nil {
		return Report{}, fmt.Errorf("drift scan output is not a valid report: %w", err)
	}
	return r, nil
}

// ReportFileName returns the report's file name for the given date, e.g.
// "doc-drift-2026-09-09.md".
func ReportFileName(date time.Time) string {
	return fmt.Sprintf("doc-drift-%s.md", date.Format("2006-01-02"))
}

// ReportRelPath returns the report's path relative to the workspace root.
// This is the only path this package ever writes to (see WriteReport), and
// it is always written through the caller's workspace-contained filesystem
// write — never a raw os.WriteFile.
func ReportRelPath(date time.Time) string {
	return "reports/" + ReportFileName(date)
}

// RenderMarkdown renders already-filtered drift items as the report body.
// An empty list still produces a well-formed report: "nothing verified" is a
// real, useful answer on a healthy checkout, not a missing or blank file.
func RenderMarkdown(items []DriftItem, date time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Documentation drift report — %s\n\n", date.Format("2006-01-02"))

	if len(items) == 0 {
		b.WriteString("No verified drift found.\n\n" +
			"Every claim the scanner could not back with a successfully-executed " +
			"verification command was omitted rather than guessed — see the " +
			"fail-closed evidence rule documented in AGENTS.md and CLAUDE.md. An " +
			"empty report means nothing survived verification, which can also mean " +
			"the scanner's shell access itself was unavailable this run; it is not " +
			"on its own proof that the docs are current.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "%d item(s) below. Each is backed by a verification command the "+
		"scanner actually ran during this scan; nothing here is a guess.\n\n", len(items))

	sorted := append([]DriftItem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].DocPath < sorted[j].DocPath })

	for i, it := range sorted {
		fmt.Fprintf(&b, "## %d. %s\n\n", i+1, it.DocPath)
		fmt.Fprintf(&b, "**Claim:** %s\n\n", it.Claim)
		if it.CurrentText != "" {
			fmt.Fprintf(&b, "**Current text:**\n```\n%s\n```\n\n", it.CurrentText)
		}
		if it.ProposedText != "" {
			fmt.Fprintf(&b, "**Proposed text:**\n```\n%s\n```\n\n", it.ProposedText)
		}
		fmt.Fprintf(&b, "**Evidence:** `%s`\n\n", it.EvidencePath)
	}

	return b.String()
}

// FileWriter is the minimal capability WriteReport needs to persist a
// report: write content to a path relative to the workspace root. It is a
// plain func type, not an interface, so this package does not need to
// import internal/tools — the real wiring (cmd/joshbot's docs_cmd.go) adapts
// tools.Registry's "filesystem" tool (write_file operation, which is
// workspace-contained via internal/tools/openat.go) to this shape.
type FileWriter func(ctx context.Context, relPath, content string) error

// WriteReport renders items and writes them through write at
// ReportRelPath(date), returning the path written. items must already be
// the output of FilterUnverified — WriteReport does not re-filter, so
// passing a raw Report's items here would defeat the whole point of the
// fail-closed rule.
func WriteReport(ctx context.Context, write FileWriter, items []DriftItem, date time.Time) (string, error) {
	relPath := ReportRelPath(date)
	if err := write(ctx, relPath, RenderMarkdown(items, date)); err != nil {
		return "", err
	}
	return relPath, nil
}

// docsCheckPromptHeader is the fixed portion of the subagent's task prompt.
// It names every document this scan compares against and states the
// fail-closed evidence rule explicitly, even though FilterUnverified is the
// rule's actual enforcement — a model told the rule up front wastes fewer of
// its own bounded iterations on claims that would be discarded anyway.
const docsCheckPromptHeader = `You are joshbot's documentation drift sentinel, running as a bounded, read-mostly subagent.

Task: compare joshbot's own source code and configuration against its documentation, and report every place they have drifted apart. Check against:
- README.md
- docs/INSTALL.md
- site/index.html and site/architecture.html
- AGENTS.md and CLAUDE.md
- every bundled internal/skills/bundled/*/SKILL.md

You have shell access. Use it. A claim is only as good as the command that produced it — read the relevant source file, or run a command (go build, go vet, wc -l, go test -cover, grep for a config key's struct tag, etc.) to check a specific documented fact, then compare its real output against what the documentation says.

THE ONE RULE THAT MATTERS: for every item you report, evidence_path must name the actual command you ran or the actual file you read to verify that specific claim — not a description of what you believe is true, and not a command you did not actually execute. If a verification command fails, or you cannot obtain real evidence for a claim, you MUST omit that claim entirely. Do not guess. Do not report a plausible-sounding claim with no evidence behind it. An empty or sparse report because little could be verified this run is the correct, honest output — never fill space with unverified claims.

Respond with a single JSON object: {"drift_items": [{"doc_path": ..., "claim": ..., "current_text": ..., "proposed_text": ..., "evidence_path": ...}]}. An empty drift_items array is a valid and often correct answer.`

// BuildPrompt assembles the subagent's task prompt. sourceSummary is an
// optional operator-supplied hint about the checkout (branch, commit, a
// note about what changed recently) and may be empty.
func BuildPrompt(sourceSummary string) string {
	var b strings.Builder
	b.WriteString(docsCheckPromptHeader)
	if strings.TrimSpace(sourceSummary) != "" {
		fmt.Fprintf(&b, "\n\nContext about this checkout: %s\n", sourceSummary)
	}
	return b.String()
}
