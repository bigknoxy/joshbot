package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/urfave/cli/v2"
)

// sinkAgent is a test double for *agent.Agent that drives the per-request
// sinks (progress + usage) the same way the real ReAct loop does: it reads
// them off the context and emits a synthetic tool call plus two usage
// deltas. It lets us exercise runAgentJSON's capture/accumulation without a
// live provider.
type sinkAgent struct {
	reply string
	err   error
}

func (a *sinkAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	if p := agent.ProgressFromContext(ctx); p != nil {
		p(agent.ToolProgressEvent{Tool: "shell", Summary: "echo hi", Phase: agent.ToolProgressStart})
		p(agent.ToolProgressEvent{Tool: "shell", Summary: "echo hi", Phase: agent.ToolProgressDone, Elapsed: 20 * time.Millisecond})
	}
	if u := agent.UsageFromContext(ctx); u != nil {
		u(providers.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
		u(providers.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	}
	return a.reply, nil
}

// --- #144: --output-format json ---

func TestRunAgentJSON_SingleDocument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	m := &sinkAgent{reply: "the answer"}

	err := runAgentJSON(context.Background(), m, "hi", "json", "", &stdout, &stderr, nil)
	if err != nil {
		t.Fatalf("runAgentJSON: %v", err)
	}

	// Exactly one JSON document on stdout.
	trimmed := strings.TrimSpace(stdout.String())
	if strings.Count(trimmed, "\n") != 0 {
		t.Fatalf("expected a single line on stdout, got:\n%s", trimmed)
	}
	var res jsonResult
	if err := json.Unmarshal([]byte(trimmed), &res); err != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", err, trimmed)
	}
	if res.Type != "result" {
		t.Errorf("type = %q, want result", res.Type)
	}
	if res.SessionID == "" {
		t.Errorf("session_id empty")
	}
	if res.Result != "the answer" {
		t.Errorf("result = %q", res.Result)
	}
	if res.Usage.TotalTokens != 20 || res.Usage.PromptTokens != 13 || res.Usage.CompletionTokens != 7 {
		t.Errorf("usage not accumulated: %+v", res.Usage)
	}
	if res.CostUSD != nil {
		t.Errorf("cost_usd should be null, got %v", *res.CostUSD)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Tool != "shell" {
		t.Errorf("tool_calls = %+v", res.ToolCalls)
	}
}

func TestRunAgentJSON_DefaultSessionID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	m := &sinkAgent{reply: "ok"}
	if err := runAgentJSON(context.Background(), m, "hi", "json", "", &stdout, &stderr, nil); err != nil {
		t.Fatal(err)
	}
	var res jsonResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "cli:cli_user" {
		t.Errorf("session_id = %q, want cli:cli_user", res.SessionID)
	}
}

// --- #144: stream-json emits one valid JSON object per line ---

func TestRunAgentJSON_StreamNDJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	m := &sinkAgent{reply: "done"}
	if err := runAgentJSON(context.Background(), m, "hi", "stream-json", "", &stdout, &stderr, nil); err != nil {
		t.Fatalf("runAgentJSON: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected >=3 NDJSON lines (start, done, result), got %d:\n%s", len(lines), stdout.String())
	}
	var types []string
	for i, ln := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(ln), &obj); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%q", i, err, ln)
		}
		types = append(types, obj["type"].(string))
	}
	// First events are tool lifecycle, final line is the result.
	if types[0] != "tool_start" || types[1] != "tool_done" {
		t.Errorf("unexpected leading event types: %v", types)
	}
	if types[len(types)-1] != "result" {
		t.Errorf("final line type = %q, want result", types[len(types)-1])
	}
}

// --- #144: stdout carries data only ---

func TestRunAgentJSON_StdoutDataOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	m := &sinkAgent{reply: "clean"}
	if err := runAgentJSON(context.Background(), m, "hi", "stream-json", "", &stdout, &stderr, nil); err != nil {
		t.Fatal(err)
	}
	for i, ln := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if !json.Valid([]byte(ln)) {
			t.Errorf("non-JSON content on stdout at line %d: %q", i, ln)
		}
	}
}

// --- #144/#148: error path writes JSON error to stderr, keeps stdout empty ---

func TestRunAgentJSON_ErrorToStderr(t *testing.T) {
	jsonErrorEmitted = false
	t.Cleanup(func() { jsonErrorEmitted = false })

	var stdout, stderr bytes.Buffer
	m := &sinkAgent{err: errors.New("boom")}
	err := runAgentJSON(context.Background(), m, "hi", "json", "", &stdout, &stderr, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if codeForError(err) != exitGeneral {
		t.Errorf("exit code = %d, want %d", codeForError(err), exitGeneral)
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("stdout must be empty on error, got %q", stdout.String())
	}
	if !jsonErrorEmitted {
		t.Errorf("jsonErrorEmitted not set")
	}
	var doc jsonErrorDoc
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("stderr not valid JSON error: %v\n%s", err, stderr.String())
	}
	if doc.Type != "error" || doc.Code != exitGeneral || !strings.Contains(doc.Error, "boom") {
		t.Errorf("bad error doc: %+v", doc)
	}
}

// --- #149: resume threads a specific session ---

func TestRunAgentJSON_ResumeSessionID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	m := &sinkAgent{reply: "ok"}
	if err := runAgentJSON(context.Background(), m, "hi", "json", "cli:thread-42", &stdout, &stderr, nil); err != nil {
		t.Fatal(err)
	}
	var res jsonResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "cli:thread-42" {
		t.Errorf("session_id = %q, want cli:thread-42", res.SessionID)
	}
}

func TestHeadlessSession(t *testing.T) {
	cases := []struct {
		resume, ch, sn string
	}{
		{"", "cli", "cli_user"},
		{"cli:abc", "cli", "abc"},
		{"telegram:12345", "telegram", "12345"},
		{"bareid", "cli", "bareid"},
		{":weird", "cli", "weird"},
	}
	for _, c := range cases {
		ch, sn := headlessSession(c.resume)
		if ch != c.ch || sn != c.sn {
			t.Errorf("headlessSession(%q) = (%q,%q), want (%q,%q)", c.resume, ch, sn, c.ch, c.sn)
		}
	}
}

// --- #148: exit-code mapping ---

func TestCodeForError(t *testing.T) {
	if codeForError(nil) != exitOK {
		t.Errorf("nil -> %d", codeForError(nil))
	}
	if codeForError(errors.New("plain")) != exitGeneral {
		t.Errorf("plain -> %d, want %d", codeForError(errors.New("plain")), exitGeneral)
	}
	e := newExitError(exitAuth, "log in", errors.New("no creds"))
	if codeForError(e) != exitAuth {
		t.Errorf("exitError -> %d, want %d", codeForError(e), exitAuth)
	}
	// Wrapped in the chain is still found.
	wrapped := fmt.Errorf("context: %w", e)
	if codeForError(wrapped) != exitAuth {
		t.Errorf("wrapped exitError -> %d, want %d", codeForError(wrapped), exitAuth)
	}
	if remediationForError(e) != "log in" {
		t.Errorf("remediation = %q", remediationForError(e))
	}
}

func TestParseLogLevel(t *testing.T) {
	for _, s := range []string{"debug", "info", "warn", "warning", "error", ""} {
		if _, err := parseLogLevel(s); err != nil {
			t.Errorf("parseLogLevel(%q) unexpected error: %v", s, err)
		}
	}
	if _, err := parseLogLevel("nonsense"); err == nil {
		t.Errorf("parseLogLevel(nonsense) should error")
	}
}

// --- #144/#148: runAgent flag validation (no config/provider needed) ---

func agentCtx(t *testing.T, flags map[string]string) *cli.Context {
	t.Helper()
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.String("output-format", "text", "")
	fs.String("message", "", "")
	fs.String("resume", "", "")
	fs.String("model", "", "")
	fs.String("config", "", "")
	for k, v := range flags {
		if err := fs.Set(k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	return cli.NewContext(cli.NewApp(), fs, nil)
}

func TestRunAgent_InvalidOutputFormat(t *testing.T) {
	err := runAgent(agentCtx(t, map[string]string{"output-format": "yaml", "message": "hi"}))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if codeForError(err) != exitValidation {
		t.Errorf("exit code = %d, want %d", codeForError(err), exitValidation)
	}
}

func TestRunAgent_JSONRequiresMessage(t *testing.T) {
	err := runAgent(agentCtx(t, map[string]string{"output-format": "json"}))
	if err == nil {
		t.Fatal("expected validation error for missing message")
	}
	if codeForError(err) != exitValidation {
		t.Errorf("exit code = %d, want %d", codeForError(err), exitValidation)
	}
}
