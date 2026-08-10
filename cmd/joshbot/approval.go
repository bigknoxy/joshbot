package main

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/bigknoxy/joshbot/internal/tools"
)

// shellApprovalMode is the resolved tools.shell_approval setting, read by the
// interactive loop when it decides whether to install an approver. It is a
// package var for the same reason isTTY is: runAgentLoop already takes eight
// parameters, and every one added has to be threaded through a dozen tests
// that do not care about it.
var shellApprovalMode = tools.ApprovalOff

// cliApprover asks the operator at the terminal.
//
// It is only ever constructed when output is a real TTY. A gate installed on a
// pipe would prompt into nowhere and then block until the turn's context
// expired, which is strictly worse than the no-approver default of denying
// immediately and saying why.
type cliApprover struct {
	out io.Writer
	in  io.Reader
	// raw is true when the terminal is in raw mode, in which case a single
	// keypress arrives without an Enter and there is no trailing newline to
	// discard.
	raw bool
	// remember allows a "yes to everything" answer to stand for the rest of
	// the session. False under "always", which is the whole difference
	// between the two modes.
	remember bool

	// mu serialises prompts. Two concurrent turns each printing a question to
	// the same terminal produce interleaved text and an answer credited to
	// whichever prompt happened to be reading — so one at a time, and
	// approvedAll is read under the same lock.
	mu          sync.Mutex
	approvedAll bool
}

func newCLIApprover(out io.Writer, in io.Reader, raw bool, mode tools.ApprovalMode) *cliApprover {
	return &cliApprover{out: out, in: in, raw: raw, remember: mode.RemembersSession()}
}

// Approve implements tools.Approver.
//
// It never returns Approve without a human keystroke: a cancelled or expired
// context returns Deny, and so does a closed stdin. The read runs on its own
// goroutine so that ctx expiry is observable at all — a bare Read on a
// terminal blocks until a key is pressed and would ignore the deadline
// entirely. That goroutine outlives the call when the context expires first;
// it is one blocked read per abandoned prompt, and the alternative (closing
// stdin to unblock it) would take the session's input with it.
func (a *cliApprover) Approve(ctx context.Context, req tools.ApprovalRequest) (tools.Decision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.remember && a.approvedAll {
		return tools.Approve, nil
	}

	// The whole command, never elided: the arguments are the dangerous part,
	// and an operator shown "git ..." has not been asked anything useful.
	fmt.Fprintf(a.out, "\n⚠️  %s wants to run:\n    %s\n", req.Tool, req.Command)
	if req.WorkingDir != "" {
		fmt.Fprintf(a.out, "    in %s\n", req.WorkingDir)
	}
	if a.remember {
		fmt.Fprint(a.out, "Allow? [y]es / [n]o / [a]ll for this session: ")
	} else {
		fmt.Fprint(a.out, "Allow? [y]es / [n]o: ")
	}

	answers := make(chan byte, 1)
	go func() { answers <- a.readAnswer() }()

	select {
	case <-ctx.Done():
		fmt.Fprintln(a.out, "\n(no answer — denied)")
		return tools.Deny, fmt.Errorf("approval was not answered: %w", ctx.Err())
	case c := <-answers:
		switch c {
		case 'y':
			fmt.Fprintln(a.out, "y")
			return tools.Approve, nil
		case 'a':
			if a.remember {
				fmt.Fprintln(a.out, "a — approving every command for this session")
				a.approvedAll = true
				return tools.Approve, nil
			}
			fmt.Fprintln(a.out, "\nsession approval is not available under shell_approval=\"always\"")
			return tools.Deny, nil
		default:
			// Anything else is a no, including EOF. A gate that reads a
			// stray keystroke as consent is not a gate.
			fmt.Fprintln(a.out, "n")
			return tools.Deny, nil
		}
	}
}

// readAnswer returns the first meaningful keystroke, lowercased. It returns 0
// on EOF, which Approve treats as a denial.
func (a *cliApprover) readAnswer() byte {
	buf := make([]byte, 1)
	for {
		n, err := a.in.Read(buf)
		if err != nil || n == 0 {
			return 0
		}
		c := buf[0]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			// Leading whitespace only: a bare Enter is not an answer.
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if !a.raw {
			// Cooked input arrives a line at a time, so the rest of the line
			// — including the newline — is still queued. Left there, it would
			// be read as an empty prompt line on the next turn.
			a.drainLine()
		}
		return c
	}
}

// drainLine consumes the remainder of the current input line.
func (a *cliApprover) drainLine() {
	buf := make([]byte, 1)
	for {
		n, err := a.in.Read(buf)
		if err != nil || n == 0 || buf[0] == '\n' {
			return
		}
	}
}
