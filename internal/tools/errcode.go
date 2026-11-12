package tools

import (
	"errors"
	"io/fs"
)

// ToolErrorCode is a stable, machine-readable classification of why a tool
// call failed. It exists because ToolResult.Error is prose written for the
// model: its wording may change at any time, so a script or a caller that
// needs to branch on *why* a tool failed — denied versus timed out versus not
// found — has nothing to match on. Codes are the interface; the prose is not.
//
// The contract: a code is attached where this package knows the failure
// class, and every other failure carries the empty code rather than a wrong
// guess. New codes may be added; an existing code never changes meaning.
// The human-readable text of an error is unaffected — a code rides alongside
// it, never replaces it. The current list is documented in docs/TOOLS.md.
type ToolErrorCode string

const (
	// CodeDeny marks a refusal by policy or by a human: the shell deny list,
	// the shell allowlist, or an approval gate that answered no — including
	// the automatic refusal an unattended turn gets from DenyAll.
	CodeDeny ToolErrorCode = "deny"
	// CodeApprovalTimeout marks an approval prompt that expired unanswered:
	// nobody said no, the wait ran out. Distinct from CodeDeny because the
	// remedy differs — a denial is final, a timeout means the same command
	// may simply not have reached a human in time.
	CodeApprovalTimeout ToolErrorCode = "approval_timeout"
	// CodeTimeout marks work cut off by a deadline, such as a shell command
	// that ran past its timeout budget.
	CodeTimeout ToolErrorCode = "timeout"
	// CodeNotFound marks a named thing that does not exist: a file or
	// directory handed to the filesystem tool, a working directory a shell
	// command was pointed at, or a tool name missing from the registry.
	CodeNotFound ToolErrorCode = "not_found"
)

// codedError attaches a ToolErrorCode to an error while leaving its text and
// its wrapped chain exactly as they were, so errors.Is and errors.As keep
// working across the wrapper (existing callers match ErrDenied through it)
// and fmt's %v prints the same prose as before.
type codedError struct {
	code ToolErrorCode
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }

func (e *codedError) Unwrap() error { return e.err }

// WithCode tags err with a machine-readable code. An empty code returns err
// unchanged — a call site that cannot classify the failure stays a plain
// error instead of gaining an empty wrapper — and nil stays nil.
func WithCode(err error, code ToolErrorCode) error {
	if err == nil || code == "" {
		return err
	}
	return &codedError{code: code, err: err}
}

// ErrorCode returns the code carried by err, or "" when the error is
// unclassified. It reads through any %w wrapping, so a code survives the
// layers above the tool — Registry.Execute's "tool execution failed" wrapper
// among them — without those layers knowing codes exist.
func ErrorCode(err error) ToolErrorCode {
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code
	}
	return ""
}

// notFoundCode maps err to CodeNotFound when it reports a missing file or
// directory, and to "" otherwise — EACCES or EISDIR is not "not found" and
// must not be reported as one. The "" result makes WithCode a no-op, so call
// sites stay one line and unclassified errors stay plain.
func notFoundCode(err error) ToolErrorCode {
	if errors.Is(err, fs.ErrNotExist) {
		return CodeNotFound
	}
	return ""
}
