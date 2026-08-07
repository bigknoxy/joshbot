package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
)

// fixedReplyAgent returns a caller-supplied reply verbatim, so a golden test
// can control the exact response bytes.
type fixedReplyAgent struct{ reply string }

func (a fixedReplyAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	return a.reply, nil
}

// Golden-output regression tests (issue #154).
//
// The interactive CLI (runAgentLoop) and the non-interactive text path
// (runAgentSingleMessage) are the product surface for BOTH the human and the
// agentic audiences: a script shelling out to `joshbot agent -m` parses
// stdout directly. A refactor that leaked a spinner frame, a "\r", or an ANSI
// clear code into non-TTY output would silently corrupt every such consumer.
// These tests pin the exact bytes so that regression cannot pass unnoticed.

// controlBytes reports whether s contains any of the decorative control
// sequences the CLI must never emit to a non-terminal: carriage returns, the
// ANSI ESC introducer, or the tool-progress frame glyphs.
func containsDecoration(s string) bool {
	return strings.ContainsAny(s, "\r\x1b") ||
		strings.Contains(s, "⏺") ||
		strings.Contains(s, "⎿") ||
		strings.Contains(s, "thinking...")
}

// TestRunAgentSingleMessage_GoldenStdout pins the exact stdout of the
// non-interactive `agent -m` text path. It must be the answer text and a
// single trailing newline — nothing else. This is the invariant an agentic
// consumer depends on.
func TestRunAgentSingleMessage_GoldenStdout(t *testing.T) {
	var out bytes.Buffer
	mock := fixedReplyAgent{reply: "the answer"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runAgentSingleMessage(ctx, mock, "hi", "", &out, nil); err != nil {
		t.Fatalf("runAgentSingleMessage: %v", err)
	}

	const want = "the answer\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want exactly %q", out.String(), want)
	}
}

// TestRunAgentSingleMessage_TrimsAndStaysClean proves that a reply padded
// with leading/trailing whitespace and blank lines is normalised to a single
// clean line with no decoration leaking in.
func TestRunAgentSingleMessage_TrimsAndStaysClean(t *testing.T) {
	var out bytes.Buffer
	mock := fixedReplyAgent{reply: "\n\n  padded reply  \n\n"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runAgentSingleMessage(ctx, mock, "hi", "", &out, nil); err != nil {
		t.Fatalf("runAgentSingleMessage: %v", err)
	}

	const want = "padded reply\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want exactly %q", out.String(), want)
	}
	if containsDecoration(out.String()) {
		t.Fatalf("stdout contains decorative control bytes: %q", out.String())
	}
}

// TestRunAgentLoop_NonTTY_GoldenExactOutput pins the full byte stream of the
// interactive loop when output is not a terminal. Even though the mock agent
// emits tool-progress events on every Process call, isTTY=false means the
// progress sink is never wired and the spinner never runs, so the output is
// deterministic and free of any decoration.
func TestRunAgentLoop_NonTTY_GoldenExactOutput(t *testing.T) {
	withTTY(t, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var out bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	// mockProgressAgent would emit ⏺/⎿ frames IF a sink were wired — the
	// point of this test is that at isTTY=false none of that reaches stdout.
	mock := &mockProgressAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &out, mock, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	const want = "joshbot agent mode. Type 'exit' to quit.\n" +
		"> \n" +
		"reply: hello\n\n" +
		"> "
	if out.String() != want {
		t.Fatalf("non-TTY golden mismatch:\n got: %q\nwant: %q", out.String(), want)
	}
	if containsDecoration(out.String()) {
		t.Fatalf("non-TTY output leaked decoration: %q", out.String())
	}
}

// TestRunAgentLoop_TTY_EmitsFramesAroundAnswer is the companion to the
// non-TTY golden test: with isTTY forced true, the ⏺ start frame and ⎿
// completion frame must bracket the answer, and each must be preceded by the
// clearLine sequence so it overwrites any spinner residue. The spinner's own
// timing is nondeterministic, so this asserts on the frame structure rather
// than a byte-exact match.
func TestRunAgentLoop_TTY_EmitsFramesAroundAnswer(t *testing.T) {
	withTTY(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var out bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &mockProgressAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &out, mock, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	got := out.String()
	startIdx := strings.Index(got, "⏺ shell(echo hi)")
	doneIdx := strings.Index(got, "⎿ ok")
	answerIdx := strings.Index(got, "reply: hello")
	if startIdx < 0 {
		t.Fatalf("missing ⏺ start frame: %q", got)
	}
	if doneIdx < 0 {
		t.Fatalf("missing ⎿ completion frame: %q", got)
	}
	if answerIdx < 0 {
		t.Fatalf("missing answer: %q", got)
	}
	// Ordering: start frame, then done frame, then the final answer.
	if !(startIdx < doneIdx && doneIdx < answerIdx) {
		t.Fatalf("frames out of order: start=%d done=%d answer=%d in %q", startIdx, doneIdx, answerIdx, got)
	}
	// Each frame is preceded by clearLine (\r\033[K) so it overwrites the
	// in-place spinner rather than appending to it.
	if !strings.Contains(got, clearLine+"⏺ shell(echo hi)") {
		t.Errorf("⏺ frame not preceded by clearLine in %q", got)
	}
}

// TestRunAgentLoop_NonTTY_ErrorPathStaysClean verifies that even the error
// branch of the loop emits no decoration to a non-terminal.
func TestRunAgentLoop_NonTTY_ErrorPathStaysClean(t *testing.T) {
	withTTY(t, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var out bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &errAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &out, mock, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Error:") {
		t.Fatalf("expected an error line, got %q", got)
	}
	if containsDecoration(got) {
		t.Fatalf("error path leaked decoration to non-TTY: %q", got)
	}
}

// errAgent always fails Process, to exercise the loop's error branch.
type errAgent struct{}

func (errAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	return "", context.DeadlineExceeded
}

// Ensure the progress event shape used by the mocks matches the real type so
// this file fails to compile if the ProgressFunc signature drifts.
var _ agent.ProgressFunc = func(agent.ToolProgressEvent) {}
