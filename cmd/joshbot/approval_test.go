package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/tools"
)

func req() tools.ApprovalRequest {
	return tools.ApprovalRequest{
		Tool:       "shell",
		Command:    `rm -rf ./build --and "more args"`,
		WorkingDir: "/tmp/ws",
	}
}

func answering(t *testing.T, keys string, mode tools.ApprovalMode) (*cliApprover, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	// raw=false: the reader is a pipe, not a terminal, so answers arrive
	// line-at-a-time and the trailing newline has to be drained.
	return newCLIApprover(out, strings.NewReader(keys), false, mode), out
}

func TestCLIApprover_YesApproves(t *testing.T) {
	a, _ := answering(t, "y\n", tools.ApprovalInteractive)
	d, err := a.Approve(context.Background(), req())
	if d != tools.Approve || err != nil {
		t.Fatalf("y was not read as approval: %v %v", d, err)
	}
}

func TestCLIApprover_NoDenies(t *testing.T) {
	a, _ := answering(t, "n\n", tools.ApprovalInteractive)
	if d, _ := a.Approve(context.Background(), req()); d != tools.Deny {
		t.Fatal("n was read as approval")
	}
}

// Anything that is not an explicit yes is a no. A gate that reads a stray
// keystroke as consent is not a gate.
func TestCLIApprover_UnrecognisedKeyDenies(t *testing.T) {
	for _, key := range []string{"q\n", "1\n", "\n\n\nq\n"} {
		a, _ := answering(t, key, tools.ApprovalInteractive)
		if d, _ := a.Approve(context.Background(), req()); d != tools.Deny {
			t.Errorf("%q was read as approval", key)
		}
	}
}

// A closed stdin — the pipe case, or the user hitting Ctrl-D — is a denial,
// never a default yes.
func TestCLIApprover_EOFDenies(t *testing.T) {
	a, _ := answering(t, "", tools.ApprovalInteractive)
	if d, _ := a.Approve(context.Background(), req()); d != tools.Deny {
		t.Fatal("EOF on stdin was read as approval")
	}
}

// The core property of the whole issue: nobody answered, so the answer is no.
// A bare Read on a terminal blocks forever, which is why the read runs on its
// own goroutine — without that the deadline would be ignored entirely.
func TestCLIApprover_TimeoutDenies(t *testing.T) {
	// A reader that never produces anything and never reaches EOF: a terminal
	// with nobody at it.
	a := newCLIApprover(&bytes.Buffer{}, newBlockingReader(), false, tools.ApprovalInteractive)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan tools.Decision, 1)
	go func() {
		d, _ := a.Approve(ctx, req())
		done <- d
	}()

	select {
	case d := <-done:
		if d != tools.Deny {
			t.Fatal("an unanswered prompt was treated as approval")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the approver blocked past its context deadline")
	}
}

func TestCLIApprover_CancellationDenies(t *testing.T) {
	a := newCLIApprover(&bytes.Buffer{}, newBlockingReader(), false, tools.ApprovalInteractive)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	if d, err := a.Approve(ctx, req()); d != tools.Deny || err == nil {
		t.Fatalf("Ctrl-C during a prompt was not a denial: %v %v", d, err)
	}
}

// The operator has to be shown the arguments — the binary alone is not the
// dangerous part of `rm -rf ./build`.
func TestCLIApprover_PromptShowsWholeCommand(t *testing.T) {
	a, out := answering(t, "n\n", tools.ApprovalInteractive)
	a.Approve(context.Background(), req())

	if !strings.Contains(out.String(), req().Command) {
		t.Errorf("the prompt did not show the command being approved:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "/tmp/ws") {
		t.Errorf("the prompt did not say where the command would run:\n%s", out.String())
	}
}

// "all for this session" is the difference between the two on modes.
func TestCLIApprover_InteractiveRemembersAll(t *testing.T) {
	a, out := answering(t, "a\n", tools.ApprovalInteractive)
	if d, _ := a.Approve(context.Background(), req()); d != tools.Approve {
		t.Fatal("a was not read as approval")
	}
	if !strings.Contains(out.String(), "[a]ll for this session") {
		t.Errorf("interactive mode did not offer the session option:\n%s", out.String())
	}
	// stdin is exhausted now, so a second prompt could only be answered from
	// the remembered decision.
	if d, _ := a.Approve(context.Background(), req()); d != tools.Approve {
		t.Fatal("interactive mode forgot the session-wide approval")
	}
}

// "always" means always. A remembered answer would silently turn it back into
// interactive, which is the whole reason the mode exists.
func TestCLIApprover_AlwaysNeverRemembers(t *testing.T) {
	a, out := answering(t, "a\ny\n", tools.ApprovalAlways)

	if d, _ := a.Approve(context.Background(), req()); d != tools.Deny {
		t.Error(`"a" was honoured under shell_approval="always"`)
	}
	if strings.Contains(out.String(), "[a]ll for this session") {
		t.Errorf(`"always" offered a session-wide approval:\n%s`, out.String())
	}
	if d, _ := a.Approve(context.Background(), req()); d != tools.Approve {
		t.Error("the following y was not read")
	}
	// Third prompt, stdin exhausted: nothing may be remembered.
	if d, _ := a.Approve(context.Background(), req()); d != tools.Deny {
		t.Error(`"always" remembered an earlier approval`)
	}
}

// Two turns prompting at once would otherwise interleave their questions on
// one terminal and credit an answer to whichever prompt happened to be
// reading. The mutex is what prevents that; this pins it under -race.
func TestCLIApprover_ConcurrentPromptsSerialise(t *testing.T) {
	a, _ := answering(t, "yyyy\n", tools.ApprovalInteractive)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Approve(context.Background(), req())
		}()
	}
	wg.Wait()
}
