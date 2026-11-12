package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The codes exist so a caller can branch on why a tool failed without
// parsing prose. Everything here pins both halves of that contract: the code
// is what it says, and the human-readable text is byte-for-byte what these
// paths produced before codes existed.

func TestErrorCode_UnclassifiedErrorReadsAsEmpty(t *testing.T) {
	if got := ErrorCode(errors.New("boom")); got != "" {
		t.Errorf("ErrorCode(plain error) = %q, want \"\"", got)
	}
	if got := ErrorCode(nil); got != "" {
		t.Errorf("ErrorCode(nil) = %q, want \"\"", got)
	}
	if got := ErrorCode(WithCode(nil, CodeDeny)); got != "" {
		t.Errorf("WithCode(nil, …) must stay nil, got %q", got)
	}
}

func TestWithCode_EmptyCodeReturnsErrUnchanged(t *testing.T) {
	err := errors.New("boom")
	if got := WithCode(err, ""); got != err {
		t.Errorf("WithCode(err, \"\") must return the error itself, got %v", got)
	}
}

func TestErrorCode_SurvivesWrapping(t *testing.T) {
	sentinel := errors.New("underlying")
	coded := WithCode(sentinel, CodeDeny)

	// Registry.Execute wraps with %w; the code must read through it.
	wrapped := fmt.Errorf("tool execution failed: %w", coded)
	if got := ErrorCode(wrapped); got != CodeDeny {
		t.Errorf("ErrorCode through %%w wrapping = %q, want %q", got, CodeDeny)
	}
	if !errors.Is(wrapped, sentinel) {
		t.Error("coded error broke the errors.Is chain")
	}
}

func TestWithCode_TextUnchanged(t *testing.T) {
	const msg = "something failed"
	coded := WithCode(errors.New(msg), CodeTimeout)
	if coded.Error() != msg {
		t.Errorf("coded text = %q, want the prose untouched %q", coded.Error(), msg)
	}
}

// --- deny: the shell screening paths ---

func TestErrCode_ShellDenyListRefusal(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false)
	res := tool.Execute(context.Background(), map[string]any{"command": "rm -rf /"})
	if res.Error == nil {
		t.Fatal("deny-listed command ran")
	}
	if got := ErrorCode(res.Error); got != CodeDeny {
		t.Errorf("denial code = %q, want %q", got, CodeDeny)
	}
	// Pinned verbatim: the prose is not an interface and may change, but a
	// change must be a deliberate edit here, not a side effect of coding it.
	const want = "command denied: potentially dangerous pattern detected (recursive delete of /)"
	if res.Error.Error() != want {
		t.Errorf("deny text changed:\n got  %q\n want %q", res.Error.Error(), want)
	}
}

func TestErrCode_ShellAllowlistRefusal(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false, "echo")
	res := tool.Execute(context.Background(), map[string]any{"command": "ls"})
	if res.Error == nil {
		t.Fatal("non-allowlisted command ran")
	}
	if got := ErrorCode(res.Error); got != CodeDeny {
		t.Errorf("allowlist refusal code = %q, want %q", got, CodeDeny)
	}
}

func TestErrCode_ShellAsyncDenyCarriesCode(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false)

	results := make(chan AsyncResult, 1)
	res := tool.ExecuteAsync(context.Background(), map[string]any{"command": "rm -rf /"},
		func(r AsyncResult) { results <- r })
	if got := ErrorCode(res.Error); got != CodeDeny {
		t.Errorf("async deny code = %q, want %q", got, CodeDeny)
	}
	select {
	case r := <-results:
		if got := ErrorCode(r.Error); got != CodeDeny {
			t.Errorf("async callback deny code = %q, want %q", got, CodeDeny)
		}
	case <-time.After(time.Second):
		t.Error("async denial never reached the callback")
	}
}

// --- deny and approval_timeout: the approval gates ---

func TestErrCode_ApprovalDenials(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)

	t.Run("human said no", func(t *testing.T) {
		ctx := WithApprover(context.Background(), approverFunc(
			func(context.Context, ApprovalRequest) (Decision, error) { return Deny, nil }))
		res := tool.Execute(ctx, map[string]any{"command": "echo hi"})
		if got := ErrorCode(res.Error); got != CodeDeny {
			t.Errorf("refusal code = %q, want %q", got, CodeDeny)
		}
		const want = "command not approved by the user: run it yourself, or ask the user to approve it"
		if res.Error.Error() != want {
			t.Errorf("refusal text changed:\n got  %q\n want %q", res.Error.Error(), want)
		}
	})

	t.Run("unattended turn", func(t *testing.T) {
		res := tool.Execute(context.Background(), map[string]any{"command": "echo hi"})
		if got := ErrorCode(res.Error); got != CodeDeny {
			t.Errorf("unattended refusal code = %q, want %q", got, CodeDeny)
		}
		if !errors.Is(res.Error, ErrDenied) {
			t.Errorf("unattended refusal lost ErrDenied: %v", res.Error)
		}
	})

	t.Run("prompt expired", func(t *testing.T) {
		blocked := approverFunc(func(ctx context.Context, _ ApprovalRequest) (Decision, error) {
			<-ctx.Done()
			return Deny, ctx.Err()
		})
		ctx, cancel := context.WithTimeout(WithApprover(context.Background(), blocked), 50*time.Millisecond)
		defer cancel()

		res := tool.Execute(ctx, map[string]any{"command": "echo hi"})
		if got := ErrorCode(res.Error); got != CodeApprovalTimeout {
			t.Errorf("expired prompt code = %q, want %q", got, CodeApprovalTimeout)
		}
		// The code is additive: the sentinel existing callers match is still
		// in the chain, and the prose still names the cause.
		if !errors.Is(res.Error, ErrDenied) {
			t.Errorf("expired prompt lost ErrDenied: %v", res.Error)
		}
		const want = "command not approved by the user: context deadline exceeded"
		if res.Error.Error() != want {
			t.Errorf("expired prompt text changed:\n got  %q\n want %q", res.Error.Error(), want)
		}
	})

	t.Run("cancelled prompt is a denial, not a timeout", func(t *testing.T) {
		blocked := approverFunc(func(ctx context.Context, _ ApprovalRequest) (Decision, error) {
			<-ctx.Done()
			return Deny, ctx.Err()
		})
		ctx, cancel := context.WithCancel(WithApprover(context.Background(), blocked))
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		defer cancel()

		res := tool.Execute(ctx, map[string]any{"command": "echo hi"})
		if got := ErrorCode(res.Error); got != CodeDeny {
			t.Errorf("cancelled prompt code = %q, want %q — nobody timed out, the wait was ended", got, CodeDeny)
		}
	})
}

func TestErrCode_SendFileApprovalDenial(t *testing.T) {
	tool, _, ws := sendFileFixture(t)
	tool.SetApproval(ApprovalAlways)
	writeWS(t, ws, "secret.txt", []byte("contents"))

	ctx := WithApprover(sendFileCtx(), approverFunc(func(context.Context, ApprovalRequest) (Decision, error) {
		return Deny, nil
	}))
	res := tool.Execute(ctx, map[string]any{"path": "secret.txt"})
	if got := ErrorCode(res.Error); got != CodeDeny {
		t.Errorf("send_file refusal code = %q, want %q", got, CodeDeny)
	}
}

// --- timeout: a shell command past its budget ---

func TestErrCode_ShellCommandTimeout(t *testing.T) {
	tool := NewShellTool(1*time.Second, t.TempDir(), false)
	// sleep 2, not sleep 30: the deadline kill hits only sh — dash forks the
	// command — and the orphaned sleep holds the stdout pipe until it exits,
	// so the error surfaces when the child finishes, not at the deadline.
	// That sequencing is pre-existing; the test pins the code, not the timing.
	res := tool.Execute(context.Background(), map[string]any{"command": "sleep 2"})
	if res.Error == nil {
		t.Fatal("a command past its timeout was allowed to finish")
	}
	if got := ErrorCode(res.Error); got != CodeTimeout {
		t.Errorf("timeout code = %q, want %q", got, CodeTimeout)
	}
	if !strings.HasPrefix(res.Error.Error(), "command timed out after ") {
		t.Errorf("timeout text changed: %q", res.Error.Error())
	}
}

// --- not_found: missing paths and unknown tools ---

func TestErrCode_FilesystemNotFound(t *testing.T) {
	tool := NewFilesystemTool(t.TempDir(), false)

	t.Run("read_file", func(t *testing.T) {
		res := tool.Execute(context.Background(), map[string]any{"operation": "read_file", "path": "missing.txt"})
		if got := ErrorCode(res.Error); got != CodeNotFound {
			t.Errorf("read_file missing code = %q, want %q", got, CodeNotFound)
		}
		if !strings.HasPrefix(res.Error.Error(), "failed to read file: ") {
			t.Errorf("read_file text changed: %q", res.Error.Error())
		}
	})

	t.Run("edit_file", func(t *testing.T) {
		res := tool.Execute(context.Background(), map[string]any{
			"operation": "edit_file", "path": "missing.txt", "search": "a", "replace": "b"})
		if got := ErrorCode(res.Error); got != CodeNotFound {
			t.Errorf("edit_file missing code = %q, want %q", got, CodeNotFound)
		}
	})

	t.Run("list_dir", func(t *testing.T) {
		res := tool.Execute(context.Background(), map[string]any{"operation": "list_dir", "path": "missing-dir"})
		if got := ErrorCode(res.Error); got != CodeNotFound {
			t.Errorf("list_dir missing code = %q, want %q", got, CodeNotFound)
		}
		const wantPrefix = "directory does not exist: "
		if !strings.HasPrefix(res.Error.Error(), wantPrefix) {
			t.Errorf("list_dir text changed: %q", res.Error.Error())
		}
	})

	t.Run("existing but unreadable kind is not not_found", func(t *testing.T) {
		// A directory exists — reading it fails with EISDIR, which is a
		// different fact from a missing file and must not read as not_found.
		res := tool.Execute(context.Background(), map[string]any{"operation": "read_file", "path": "."})
		if res.Error == nil {
			t.Fatal("reading a directory was allowed")
		}
		if got := ErrorCode(res.Error); got != "" {
			t.Errorf("read of a directory carried code %q, want \"\"", got)
		}
	})
}

func TestErrCode_ShellWorkingDirNotFound(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false)
	res := tool.Execute(context.Background(), map[string]any{
		"command": "echo hi", "working_dir": "no-such-dir"})
	if got := ErrorCode(res.Error); got != CodeNotFound {
		t.Errorf("missing working dir code = %q, want %q", got, CodeNotFound)
	}
}

func TestErrCode_RegistryToolNotFound(t *testing.T) {
	reg := NewRegistry()

	if _, err := reg.Execute(context.Background(), "no-such-tool", nil); ErrorCode(err) != CodeNotFound {
		t.Errorf("Execute code = %q, want %q", ErrorCode(err), CodeNotFound)
	}

	res, _ := reg.ExecuteWithContext(context.Background(), "no-such-tool", nil, "", "", nil)
	if got := ErrorCode(res.Error); got != CodeNotFound {
		t.Errorf("ExecuteWithContext code = %q, want %q", got, CodeNotFound)
	}
}
