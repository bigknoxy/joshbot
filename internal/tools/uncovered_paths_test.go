package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/mcp"
)

// --- mcpTool ---------------------------------------------------------------

// TestMCPToolSchemaIsServedRaw pins that an MCP tool advertises its schema
// through RawSchema and NOT through Parameters. Returning the schema as a
// flattened []Parameter would drop nested object properties, so a non-nil
// Parameters() here is a regression even though it would look harmless.
func TestMCPToolSchemaIsServedRaw(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"a":{"type":"object"}}}`)
	tool := &mcpTool{
		toolName: "search",
		fullName: mcpNamespacePrefix + "srv__search",
		desc:     "a description",
		schema:   schema,
		timeout:  time.Second,
	}

	if got := tool.Name(); got != mcpNamespacePrefix+"srv__search" {
		t.Errorf("Name() = %q, want the namespaced name", got)
	}
	if got := tool.Description(); got != "a description" {
		t.Errorf("Description() = %q", got)
	}
	if params := tool.Parameters(); params != nil {
		t.Errorf("Parameters() = %v, want nil so GenerateSchema is bypassed", params)
	}
	var _ rawSchemaProvider = tool
	if string(tool.RawSchema()) != string(schema) {
		t.Errorf("RawSchema() = %s, want the server schema verbatim", tool.RawSchema())
	}
}

// TestMCPToolExecuteReportsTransportFailure pins that a call against a server
// that was never connected comes back as a ToolResult error rather than a
// panic or an empty successful output. An unconnected client returning
// "success" would hand the model a silent no-op.
func TestMCPToolExecuteReportsTransportFailure(t *testing.T) {
	client := mcp.NewClient(mcp.Server{Name: "srv", Command: "/nonexistent"})
	tool := &mcpTool{
		client:   client,
		toolName: "search",
		fullName: mcpNamespacePrefix + "srv__search",
		timeout:  time.Second,
	}

	// ctx deliberately not a context.Context: Execute must fall back to
	// Background rather than panicking on the type assertion.
	res := tool.Execute("not a context", map[string]any{"q": "x"})
	if res.Error == nil {
		t.Fatalf("Execute on an unconnected client returned no error (output=%q)", res.Output)
	}
	if res.Output != "" {
		t.Errorf("Output = %q, want empty on error", res.Output)
	}
}

// TestTruncateMCPOutput pins the truncation convention, including that the
// default cap applies when maxChars is unset (0) — treating 0 as "no output
// allowed" would blank every MCP result.
func TestTruncateMCPOutput(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		maxChars int
		want     string
		wantNote bool
	}{
		{name: "under the cap is verbatim", out: "hello", maxChars: 10, want: "hello"},
		{name: "exactly at the cap is verbatim", out: "hello", maxChars: 5, want: "hello"},
		{name: "over the cap is cut and annotated", out: "hello", maxChars: 3, want: "hel", wantNote: true},
		{name: "zero means the default cap, not zero", out: "hello", maxChars: 0, want: "hello"},
		{name: "negative means the default cap", out: "hello", maxChars: -1, want: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMCPOutput(tt.out, tt.maxChars)
			if !strings.HasPrefix(got, tt.want) {
				t.Errorf("got %q, want prefix %q", got, tt.want)
			}
			if hasNote := strings.Contains(got, "truncated"); hasNote != tt.wantNote {
				t.Errorf("truncation note = %v, want %v (got %q)", hasNote, tt.wantNote, got)
			}
		})
	}
}

// TestMCPToolDefaultCapTruncates proves the default cap is a real cap and not
// effectively infinite.
func TestMCPToolDefaultCapTruncates(t *testing.T) {
	long := strings.Repeat("x", mcpMaxOutputChars+100)
	got := truncateMCPOutput(long, 0)
	if len(got) <= mcpMaxOutputChars {
		t.Errorf("len = %d, want > cap only by the truncation note", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("output over the default cap was not truncated")
	}
}

// --- cron tool description -------------------------------------------------

// TestCronToolDescriptionRulesOutCronExpressions pins that the description the
// model reads says durations and disclaims cron expressions. internal/cron only
// parses delay:/every: durations, so a description that said "cron expression"
// would produce confidently wrong schedules with no error at the tool boundary.
func TestCronToolDescriptionRulesOutCronExpressions(t *testing.T) {
	tool := NewCronTool(nil, "")
	desc := tool.Description()
	if !strings.Contains(desc, "not cron expressions") {
		t.Errorf("Description() = %q, want it to disclaim cron expressions", desc)
	}
	for _, unit := range []string{"30m", "2h", "1d"} {
		if !strings.Contains(desc, unit) {
			t.Errorf("Description() = %q, want a %s duration example", desc, unit)
		}
	}
	if tool.Name() != "cron" {
		t.Errorf("Name() = %q, want cron", tool.Name())
	}
}

// TestNewCronToolDefaultsChannel pins the empty-channel fallback: an empty
// default channel would address deliveries to nowhere.
func TestNewCronToolDefaultsChannel(t *testing.T) {
	if got := NewCronTool(nil, "").defaultChannel; got != "cli" {
		t.Errorf("defaultChannel = %q, want cli", got)
	}
	if got := NewCronTool(nil, "telegram").defaultChannel; got != "telegram" {
		t.Errorf("defaultChannel = %q, want the supplied channel", got)
	}
}

// --- ShellTool.ExecuteAsync refusal paths ----------------------------------

// TestExecuteAsyncRefusalsAlsoReachTheCallback pins every pre-execution refusal
// on the async path. Two things matter and both have failed before: the refusal
// must be returned AND delivered to the callback (a caller awaiting the
// callback would otherwise hang forever), and async=true must not be a way past
// the deny list, the workspace bound or the approval gate.
func TestExecuteAsyncRefusalsAlsoReachTheCallback(t *testing.T) {
	ws := t.TempDir()

	tests := []struct {
		name     string
		approval ApprovalMode
		ctx      context.Context
		args     map[string]any
		wantErr  string
	}{
		{
			name:    "missing command",
			ctx:     context.Background(),
			args:    map[string]any{},
			wantErr: "command is required",
		},
		{
			name:    "deny list applies to the async path",
			ctx:     context.Background(),
			args:    map[string]any{"command": "rm -rf /"},
			wantErr: "command denied",
		},
		{
			name:    "absolute working_dir outside the workspace",
			ctx:     context.Background(),
			args:    map[string]any{"command": "echo hi", "working_dir": "/etc"},
			wantErr: "working directory outside workspace",
		},
		{
			name:    "relative working_dir escaping the workspace",
			ctx:     context.Background(),
			args:    map[string]any{"command": "echo hi", "working_dir": "../.."},
			wantErr: "working directory outside workspace",
		},
		{
			name:     "approval gate fails closed with no approver",
			approval: ApprovalAlways,
			ctx:      context.Background(),
			args:     map[string]any{"command": "echo hi"},
			wantErr:  "not approved",
		},
		{
			name:     "approval gate fails closed on a nil context",
			approval: ApprovalAlways,
			ctx:      nil,
			args:     map[string]any{"command": "echo hi"},
			wantErr:  "not approved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewShellTool(2*time.Second, ws, true)
			tool.SetApproval(tt.approval)

			var got []AsyncResult
			res := tool.ExecuteAsync(tt.ctx, tt.args, func(r AsyncResult) {
				got = append(got, r)
			})

			if res.Error == nil {
				t.Fatalf("ExecuteAsync returned no error, want %q", tt.wantErr)
			}
			if !strings.Contains(res.Error.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", res.Error, tt.wantErr)
			}
			// The refusal must be synchronous: exactly one callback, already
			// delivered by the time ExecuteAsync returns.
			if len(got) != 1 {
				t.Fatalf("callback invoked %d times, want exactly 1 before return", len(got))
			}
			if got[0].Error == nil || !strings.Contains(got[0].Error.Error(), tt.wantErr) {
				t.Errorf("callback error = %v, want it to contain %q", got[0].Error, tt.wantErr)
			}
			if got[0].Output != "" {
				t.Errorf("callback output = %q, want empty on refusal", got[0].Output)
			}
		})
	}
}

// TestExecuteAsyncApprovedCommandRuns pins the positive half of the gate: with
// an approver attached the async path actually executes, so the fail-closed
// tests above are not passing merely because async is broken.
func TestExecuteAsyncApprovedCommandRuns(t *testing.T) {
	ws := t.TempDir()
	tool := NewShellTool(5*time.Second, ws, true)
	tool.SetApproval(ApprovalAlways)

	ctx := WithApprover(context.Background(), approverFunc(
		func(context.Context, ApprovalRequest) (Decision, error) { return Approve, nil },
	))

	done := make(chan AsyncResult, 1)
	res := tool.ExecuteAsync(ctx, map[string]any{"command": "echo async-ok"}, func(r AsyncResult) {
		done <- r
	})
	if res.Error != nil {
		t.Fatalf("ExecuteAsync returned error: %v", res.Error)
	}

	select {
	case r := <-done:
		if r.Error != nil {
			t.Fatalf("async result error: %v", r.Error)
		}
		if !strings.Contains(r.Output, "async-ok") {
			t.Errorf("output = %q, want it to contain async-ok", r.Output)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("async command never completed")
	}
}
