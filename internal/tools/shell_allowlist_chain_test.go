package tools

import (
	"strings"
	"testing"
	"time"
)

// The allowlist used to prefix-match only the first token and then hand the
// whole string to `sh -c`, so anything after a separator ran unscreened: an
// allowed first word was a licence to execute an arbitrary second program.
func TestAllowListRejectsChainedCommands(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false, "echo", "git", "ls", "wc")

	chained := []string{
		"echo hi; id",
		"git --version; sh -c 'echo pwned'",
		"echo hi && id",
		"echo hi | sh",
		"echo $(id)",
		"echo `id`",
		"echo hi &\nid",
	}

	for _, cmd := range chained {
		res := tool.Execute(nil, map[string]any{"command": cmd})
		if res.Error == nil {
			t.Errorf("chained command was permitted under an allowlist: %q -> %s", cmd, res.Output)
			continue
		}
		if !strings.Contains(res.Error.Error(), "chains a second command") {
			t.Errorf("expected a chaining refusal for %q, got %v", cmd, res.Error)
		}
	}
}

// ExecuteAsync screens identically — it is the same boundary reached by a
// different entry point.
func TestAllowListRejectsChainedCommandsAsync(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false, "echo")

	var cbErr error
	res := tool.ExecuteAsync(nil, map[string]any{"command": "echo hi; id"},
		func(r AsyncResult) { cbErr = r.Error })

	if res.Error == nil {
		t.Fatalf("async chained command was permitted: %s", res.Output)
	}
	if cbErr == nil {
		t.Fatalf("async refusal was not reported to the callback")
	}
}

// A plain allowlisted command still runs.
func TestAllowListPermitsPlainCommand(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false, "echo")
	res := tool.Execute(nil, map[string]any{"command": "echo hi"})
	if res.Error != nil {
		t.Fatalf("plain allowlisted command was refused: %v", res.Error)
	}
	if !strings.Contains(res.Output, "hi") {
		t.Fatalf("expected output to contain hi, got %q", res.Output)
	}
}

// The default allowlist for platforms with no OS sandbox is the sole boundary
// there, so it must contain nothing that launches a program of the caller's
// choosing: find (-exec), go (go run) and git (-c core.pager=…) all do.
func TestDefaultUnsandboxedAllowlistHasNoExecutors(t *testing.T) {
	for _, banned := range []string{"find", "go", "git", "sh", "bash", "xargs", "env", "python3"} {
		for _, entry := range defaultUnsandboxedAllowlist {
			if entry == banned {
				t.Errorf("%q executes an arbitrary second program and must not be in the default allowlist", banned)
			}
		}
	}
}
