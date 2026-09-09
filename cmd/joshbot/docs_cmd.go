package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/driftscan"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/redact"
	"github.com/bigknoxy/joshbot/internal/subagent"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// `joshbot docs check` runs the Tier 1 "drift sentinel" (internal/driftscan)
// on demand: a bounded, read-mostly subagent compares joshbot's own source
// and config against its documentation and writes a propose-only report.
//
// This is deliberately a standalone CLI command, not a tool the model can
// call mid-chat (the design explicitly scopes it that way) — every run gets
// its own bounded timeout, distinct from agents.defaults.timeout, the same
// way extractAndCreateSkill's background skill extraction gets its own
// skillExtractionTimeout rather than inheriting whatever the interactive
// turn's budget happens to be.
//
// Cron wiring (running this on a schedule via tools.WithCronService) is
// explicitly out of scope for this change; it is a documented follow-up, not
// a gap in what shipped here.

// docsOut is the redacted writer runDocsCheck prints through, mirroring
// tuningOut and sessionsOut — overridable in tests.
var docsOut = func() io.Writer { return redact.Writer(os.Stdout) }

// docsCheckTimeout bounds the whole drift-scan subagent run: it reads a
// meaningful slice of the source tree and runs several verification
// commands (go build, wc -l, go test -cover, ...), so it is deliberately
// more generous than agents.defaults.timeout's 120s default, but still
// finite. It is NOT agents.defaults.timeout — this command is not a chat
// turn, and inheriting that budget would tie an unrelated setting to a
// feature that has nothing to do with interactive latency.
const docsCheckTimeout = 5 * time.Minute

// docsCheckMaxIter raises the subagent's iteration budget above
// subagent.DefaultMaxIterations (20): a scan that reads several docs and
// runs several verification commands per claim needs more tool-call rounds
// than an ordinary focused subagent task.
const docsCheckMaxIter = 40

// driftScanAllowedTools is the allowlist of tool names the drift-scan
// subagent's own tool-calling loop may call. internal/driftscan's package
// doc claims the subagent "is never given a path to config.json,
// skills.trust, mcp.trust or any other governed file" — but that confinement
// only ever applied to the report-writing closure in runDocsCheck (write,
// below). Wiring the subagent with the raw toolExecutorAdapter (the same,
// unrestricted tools.Registry an interactive chat turn gets) handed it
// joshbot_config (config.Save on operations "set"/"switch_model"), send_file,
// cron and the message tools too — none of which a read-mostly,
// propose-only doc scan has any legitimate use for, and all of which
// contradict the documented contract. subagent.ToolExecutor has no
// allowlist/denylist of its own, so scopedDocsExecutor below is what
// actually enforces it.
//
// Shell stays in the allowlist on purpose: internal/driftscan's own prompt
// tells the model "You have shell access. Use it" — every reported item
// must be backed by a verification command (go build, wc -l, go test
// -cover, ...) run through it. This allowlist does not, and cannot,
// additionally confine what a permitted shell command touches; an operator
// who wants that confinement sets tools.shell_sandbox="workspace" the same
// as for any other shell access. The filesystem tool's generic dispatcher
// (name "filesystem", which accepts operation: "write_file"/"edit_file") is
// deliberately excluded — only the read-only per-operation aliases are
// allowed, so there is no write path into the subagent's own tool loop at
// all; the only write this command performs is the report itself, via the
// `write` closure in runDocsCheck, which calls the registry directly and
// never goes through the subagent.
var driftScanAllowedTools = map[string]bool{
	"read_file": true,
	"list_dir":  true,
	"glob":      true,
	"grep":      true,
	"shell":     true,
}

// scopedDocsExecutor wraps a subagent.ToolExecutor and restricts it to
// driftScanAllowedTools, both in the schemas offered to the model
// (GetSchemas) and, as the actual enforcement point, in ExecuteWithContext —
// so a model that calls a disallowed tool name anyway (hallucinated, or
// recalled from a bundled skill's description) is refused rather than
// served.
type scopedDocsExecutor struct {
	inner subagent.ToolExecutor
}

func (s *scopedDocsExecutor) GetSchemas() []providers.Tool {
	all := s.inner.GetSchemas()
	filtered := make([]providers.Tool, 0, len(all))
	for _, t := range all {
		if driftScanAllowedTools[t.Function.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func (s *scopedDocsExecutor) ExecuteWithContext(ctx context.Context, name string, args map[string]any, channel, channelID string, callback func(subagent.AsyncResult)) (subagent.ToolResult, bool) {
	if !driftScanAllowedTools[name] {
		return subagent.ToolResult{Error: fmt.Errorf("tool %q is not available to the drift-scan subagent (read-mostly by design; see driftScanAllowedTools)", name)}, false
	}
	return s.inner.ExecuteWithContext(ctx, name, args, channel, channelID, callback)
}

// docsCheckRunner is the minimal capability performDocsCheck needs from a
// subagent.Runner. Narrowing it to an interface (rather than depending on
// *subagent.Runner directly) is what lets the fail-closed evidence rule be
// tested end-to-end with a scripted answer, with no real provider or shell
// access required — see docs_cmd_test.go's scriptedDocsRunner.
type docsCheckRunner interface {
	Run(ctx context.Context, prompt string, cfg subagent.Config) (*subagent.SubResult, error)
}

// docsCommand builds the `joshbot docs` command group.
func docsCommand() *cli.Command {
	return &cli.Command{
		Name:  "docs",
		Usage: "Documentation drift tooling",
		Subcommands: []*cli.Command{
			{
				Name:  "check",
				Usage: "Scan the repo for documentation drift against the current source and config",
				Description: "Runs a bounded, read-mostly subagent (internal/driftscan) that compares\n" +
					"joshbot's own source and config against README.md, docs/INSTALL.md,\n" +
					"site/*.html, AGENTS.md/CLAUDE.md and the bundled SKILL.md files.\n\n" +
					"Every reported item must be backed by a verification command the\n" +
					"subagent actually ran (go build, wc -l, go test -cover, ...); a claim\n" +
					"with no evidence is silently omitted rather than guessed — this is\n" +
					"enforced in Go after the scan runs, not left to the model's own\n" +
					"judgement. The only file this writes is the report itself, under\n" +
					"workspace/reports/doc-drift-<date>.md.",
				Action: runDocsCheck,
			},
		},
	}
}

func runDocsCheck(c *cli.Context) error {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}

	_, provider, _, _, toolsRegistry, _, err := setupComponents(cfg)
	defer closeMCPServers()
	defer stopBackgroundServices()
	if err != nil {
		return err
	}

	agentModel := cfg.Agents.Defaults.Model
	if cfg.UseModelsConfig() {
		agentModel = cfg.ModelsConfig.Agent.Model
	}

	runner := subagent.NewRunner(provider, agentModel,
		subagent.WithTools(&scopedDocsExecutor{inner: &toolExecutorAdapter{registry: toolsRegistry}}),
		subagent.WithTimeout(docsCheckTimeout),
	)

	ctx, cancel := context.WithTimeout(context.Background(), docsCheckTimeout)
	defer cancel()

	// Verification commands go through the shell tool like any other tool
	// call. tools.shell_approval defaults to "off" (no gate at all), so the
	// common case needs nothing further; when an operator has turned it on,
	// this command is being run by a human at a terminal for exactly this
	// purpose, so it installs the same interactive approver runAgentLoop
	// installs rather than let every verification command be denied
	// immediately (the fail-closed default for an unattended caller) and
	// silently produce an empty "no drift found" report that looks like a
	// clean bill of health rather than "nothing could be verified".
	if (shellApprovalMode != tools.ApprovalOff || sendFileApprovalMode != tools.ApprovalOff) && isTTY(os.Stdout) {
		approver := newCLIApprover(os.Stdout, os.Stdin, false, combinedApprovalMode(shellApprovalMode, sendFileApprovalMode))
		ctx = tools.WithApprover(ctx, approver)
	}

	write := func(ctx context.Context, relPath, content string) error {
		res, _ := toolsRegistry.ExecuteWithContext(ctx, "filesystem", map[string]any{
			"operation": "write_file",
			"path":      relPath,
			"content":   content,
		}, "cli", "", nil)
		return res.Error
	}

	relPath, n, err := performDocsCheck(ctx, runner, write, "")
	if err != nil {
		return fmt.Errorf("docs check: %w", err)
	}

	out := docsOut()
	fmt.Fprintf(out, "Wrote %s (%d verified item(s))\n", relPath, n)
	if n == 0 {
		fmt.Fprintln(out, "No drift survived verification this run. This can mean the docs are")
		fmt.Fprintln(out, "current, or that nothing could be verified (e.g. shell access was")
		fmt.Fprintln(out, "unavailable) — read the report for context, not just the item count.")
	}
	return nil
}

// performDocsCheck runs the drift-scan prompt through runner, parses its
// answer, applies the fail-closed evidence filter, and writes the surviving
// items through write. It returns the path written (relative to the
// workspace root) and the number of items that survived filtering.
//
// This is the whole thing runDocsCheck delegates to, so it stays testable
// with a scripted runner and a temp-directory writer — no real provider,
// subagent shell access, or config.Config required.
func performDocsCheck(ctx context.Context, runner docsCheckRunner, write driftscan.FileWriter, sourceSummary string) (relPath string, verified int, err error) {
	prompt := driftscan.BuildPrompt(sourceSummary)

	res, err := runner.Run(ctx, prompt, subagent.Config{
		Role:         subagent.RoleLeaf,
		OutputSchema: driftscan.OutputSchema(),
		Timeout:      docsCheckTimeout,
		MaxIter:      docsCheckMaxIter,
	})
	if err != nil {
		return "", 0, fmt.Errorf("drift scan run failed: %w", err)
	}

	report, perr := driftscan.ParseReport(res.Output)
	if perr != nil {
		return "", 0, fmt.Errorf("drift scan produced unparseable output: %w", perr)
	}

	filtered := driftscan.FilterUnverified(report.DriftItems)

	relPath, werr := driftscan.WriteReport(ctx, write, filtered, time.Now())
	if werr != nil {
		return "", 0, fmt.Errorf("failed to write drift report: %w", werr)
	}

	return relPath, len(filtered), nil
}
