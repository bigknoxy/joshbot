package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Approval is the gate that asks a human before a shell command runs.
//
// It is deliberately separate from the progress sink (agent.WithSink), which
// is fire-and-forget: a progress callback that never returns costs nothing,
// while an approval that never returns hangs the turn. The two also have
// opposite failure modes — a dropped progress event is cosmetic, a dropped
// approval must be a denial.
//
// The whole design follows from one property: joshbot runs turns nobody is
// watching. The heartbeat scanner and the cron scheduler start turns with no
// human attached, so a gate that blocks would hang a background goroutine and
// a gate that auto-approves on timeout would not be a gate. Both are resolved
// the same way — an unattended turn carries no Approver, ApproverFromContext
// hands back DenyAll, and the command is refused immediately.

// Decision is the answer to an approval request.
type Decision int

const (
	// Deny is the zero value on purpose: every path that fails to produce a
	// real answer — no approver, a timeout, a cancelled context, an error
	// from the approver — lands here without anyone having to remember to.
	Deny Decision = iota
	// Approve lets the command run.
	Approve
)

func (d Decision) String() string {
	if d == Approve {
		return "approve"
	}
	return "deny"
}

// ApprovalRequest describes what is about to run. The command is carried whole
// and unabridged: an operator approving `rm -rf ./build` must be shown the
// arguments, since the binary alone is not the dangerous part.
type ApprovalRequest struct {
	// Tool is the tool asking. Only "shell" today, but the interface is not
	// shell-specific and the prompt should say which tool it is speaking for.
	Tool string
	// Command is the exact command line, never truncated.
	Command string
	// WorkingDir is where it would run.
	WorkingDir string
}

// Approver decides whether a command may run.
type Approver interface {
	// Approve blocks until a decision or until ctx expires. It must never
	// return Approve on timeout or on ctx cancellation — a gate that opens
	// when nobody answered is not a gate.
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ErrDenied is returned to the model when a command was refused. It is a
// sentinel so a caller can distinguish "the human said no" from "the command
// failed", and the text is written for the model: it should retry with a
// different approach, not re-run the same command hoping for a different
// answer.
var ErrDenied = errors.New("command not approved by the user")

// denyAll refuses everything without blocking.
type denyAll struct{}

func (denyAll) Approve(context.Context, ApprovalRequest) (Decision, error) {
	return Deny, fmt.Errorf("%w: no approver is attached to this request "+
		"(a scheduled or background turn has no human to ask)", ErrDenied)
}

// DenyAll is the approver used when none was installed. It is what makes the
// gate fail closed for cron, heartbeat and any other unattended turn.
func DenyAll() Approver { return denyAll{} }

// approverKey is an unexported context key.
type approverKey struct{}

// WithApprover attaches an approver to the request context. Passing nil
// removes any approver, which means DenyAll — there is no way to use this to
// disable the gate, only to close it.
func WithApprover(ctx context.Context, a Approver) context.Context {
	if a == nil {
		return context.WithValue(ctx, approverKey{}, nil)
	}
	return context.WithValue(ctx, approverKey{}, a)
}

// ApproverFromContext returns the request's approver, or DenyAll when there is
// none. It never returns nil, so no caller can forget the nil check and get an
// open gate out of it.
func ApproverFromContext(ctx context.Context) Approver {
	if ctx == nil {
		return DenyAll()
	}
	a, _ := ctx.Value(approverKey{}).(Approver)
	if a == nil {
		return DenyAll()
	}
	return a
}

// ApprovalMode selects when the gate applies.
type ApprovalMode string

const (
	// ApprovalOff runs commands without asking. The default: turning the gate
	// on changes what an existing install can do unattended, so it is opt-in.
	ApprovalOff ApprovalMode = "off"
	// ApprovalInteractive asks before every command, and lets the approver
	// remember a "yes to everything" answer for the rest of the session.
	ApprovalInteractive ApprovalMode = "interactive"
	// ApprovalAlways asks before every command and ignores any remembered
	// answer — every single command is a fresh decision.
	ApprovalAlways ApprovalMode = "always"
)

// ParseApprovalMode maps a config string to a mode. An unrecognised value
// returns ok=false rather than defaulting: a typo'd "interactve" that silently
// meant "off" would leave an operator believing commands were gated when every
// one of them ran unasked. Mirrors ParseSandboxMode, and cmd/joshbot turns
// ok=false into a startup error the same way.
func ParseApprovalMode(s string) (ApprovalMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "false", "none", "disabled":
		return ApprovalOff, true
	case "interactive", "on", "true":
		return ApprovalInteractive, true
	case "always":
		return ApprovalAlways, true
	default:
		return ApprovalOff, false
	}
}

// RemembersSession reports whether the mode permits an approver to reuse an
// earlier "approve everything" answer. This is the only behavioural difference
// between interactive and always.
func (m ApprovalMode) RemembersSession() bool { return m == ApprovalInteractive }
