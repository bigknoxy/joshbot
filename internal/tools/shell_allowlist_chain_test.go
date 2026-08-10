package tools

import (
	"os"
	"path/filepath"
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
	banned := map[string]string{
		"find":    "-exec sh -c",
		"go":      "go run",
		"git":     "-c core.pager='sh -c …'",
		"rg":      "--pre PROGRAM",
		"sort":    "--compress-program=PROGRAM",
		"sh":      "is a shell",
		"bash":    "is a shell",
		"xargs":   "runs the command it is given",
		"env":     "runs the command it is given",
		"python3": "is an interpreter",
		"awk":     "system()",
		"sed":     "GNU sed e command",
		"perl":    "is an interpreter",
		"vi":      ":!command",
		"less":    "!command",
		"man":     "pages through $PAGER",
		"tar":     "--use-compress-program",
		"zip":     "-TT unzip command",
	}
	for name, why := range banned {
		for _, entry := range defaultUnsandboxedAllowlist {
			if entry == name {
				t.Errorf("%q executes an arbitrary second program (%s) and must not be in the default allowlist", name, why)
			}
		}
	}

	// Each remaining entry is asserted reachable only as itself: the specific
	// escape flags above must not be runnable through anything on the list.
	tool := NewShellTool(5*time.Second, t.TempDir(), true, defaultUnsandboxedAllowlist...)
	for name := range banned {
		cmd := name + " --version"
		if res := tool.Execute(nil, map[string]any{"command": cmd}); res.Error == nil {
			t.Errorf("%q is reachable under the default allowlist: %s", cmd, res.Output)
		}
	}
}

// Redirection was absent from commandSeparators, and the command still goes to
// `sh -c` unchanged. So with the default read-only allowlist in force,
// `echo PWNED > /outside/escaped.txt` passed the first-word check, passed the
// deny list, and the shell wrote the file — outside the workspace, as the user
// running joshbot, with no sandbox involved. `>>` against ~/.ssh/authorized_keys
// is the same call with two characters changed, and `<` is the read direction of
// it. The allowlist's entire premise is that the listed commands cannot affect
// state they were not handed; one redirection character undoes that.
func TestAllowListRejectsRedirection(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "escaped.txt")

	tool := NewShellTool(5*time.Second, ws, true, defaultUnsandboxedAllowlist...)

	for _, cmd := range []string{
		"echo PWNED > " + victim,
		"echo PWNED >> " + victim,
		"echo PWNED 2> " + victim,
		"echo PWNED &> " + victim,
		"cat < /etc/passwd",
		"cat <> " + victim,
	} {
		res := tool.Execute(nil, map[string]any{"command": cmd})
		if res.Error == nil {
			t.Errorf("redirection was permitted under an allowlist: %q -> %s", cmd, res.Output)
		}
		if _, err := os.Stat(victim); err == nil {
			t.Fatalf("command %q wrote outside the workspace: %s exists", cmd, victim)
		}
	}

	// The same boundary through the async entry point.
	res := tool.ExecuteAsync(nil, map[string]any{"command": "echo PWNED > " + victim},
		func(AsyncResult) {})
	if res.Error == nil {
		t.Errorf("async redirection was permitted: %s", res.Output)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Fatalf("async command wrote outside the workspace: %s exists", victim)
	}
}

// A carriage return and an ANSI-C quoted escape are both ways of writing a
// separator that a naive substring scan for "\n" or ";" never sees.
func TestAllowListRejectsSmuggledSeparators(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), true, "echo")
	for _, cmd := range []string{"echo hi\rid", "echo $'\\x3b' id"} {
		if res := tool.Execute(nil, map[string]any{"command": cmd}); res.Error == nil {
			t.Errorf("smuggled separator was permitted: %q -> %s", cmd, res.Output)
		}
	}
}

// `rg` and `sort` were on the default allowlist and both run a program named by
// a flag: ripgrep's --pre is invoked once per file, and sort's
// --compress-program is exec'd when the sort spills to disk. Neither was caught
// by the allowlist (the first word is allowed) or the deny list (no dangerous
// pattern), so on a platform with no OS sandbox — where this list is the only
// boundary — they were a direct route to running /bin/sh.
func TestDefaultAllowlistRejectsExecFlagBinaries(t *testing.T) {
	ws := t.TempDir()
	tool := NewShellTool(5*time.Second, ws, true, defaultUnsandboxedAllowlist...)

	escapes := []string{
		"rg --pre /bin/sh --pre-glob '*' pattern .",
		"rg --pre=/bin/sh pattern .",
		"sort --compress-program=/bin/sh /etc/passwd",
		"sort --compress-program /bin/sh /etc/passwd",
	}
	for _, cmd := range escapes {
		res := tool.Execute(nil, map[string]any{"command": cmd})
		if res.Error == nil {
			t.Errorf("exec-flag escape was permitted: %q -> %s", cmd, res.Output)
			continue
		}
		if !strings.Contains(res.Error.Error(), "not in allowlist") {
			t.Errorf("expected %q to be refused as not allowlisted, got %v", cmd, res.Error)
		}
	}
}
