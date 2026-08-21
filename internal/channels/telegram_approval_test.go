package channels

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/tools"
)

func approvalTestChannel(t *testing.T) (*TelegramChannel, *ShellApprovalCoordinator, *fakeEditor) {
	t.Helper()
	tg := newTestTelegramChannel()
	ed := &fakeEditor{}
	tg.mu.Lock()
	tg.editor = ed
	tg.mu.Unlock()
	c, err := tg.NewShellApprovalCoordinator(tools.ApprovalInteractive)
	if err != nil {
		t.Fatalf("NewShellApprovalCoordinator: %v", err)
	}
	return tg, c, ed
}

// press simulates the router delivering a decoded button press. It goes
// through DecodeCallback so the payloads the keyboard encodes and the ones
// the handler reads cannot drift.
func press(t *testing.T, c *ShellApprovalCoordinator, chatID int64, action, payload string) {
	t.Helper()
	if err := c.handlePress(context.Background(), CallbackPress{
		Action: CallbackAction{Namespace: ApprovalCallbackNamespace, Action: action, Payload: payload},
		ChatID: chatID,
	}); err != nil {
		t.Fatalf("handlePress: %v", err)
	}
}

// approveAsync starts an Approve call and returns its result channel plus the
// pending id it registered (read from the coordinator once the prompt is out).
func approveAsync(t *testing.T, c *ShellApprovalCoordinator, chatID int64, req tools.ApprovalRequest) (<-chan tools.Decision, string) {
	t.Helper()
	a := c.ApproverFor("42")
	if chatID != 42 {
		a = &telegramApprover{c: c, chatID: chatID}
	}
	out := make(chan tools.Decision, 1)
	go func() {
		d, _ := a.Approve(context.Background(), req)
		out <- d
	}()
	// Wait for the prompt to be registered.
	deadline := time.After(2 * time.Second)
	for {
		c.mu.Lock()
		var id string
		for k := range c.pending {
			id = k
		}
		c.mu.Unlock()
		if id != "" {
			return out, id
		}
		select {
		case <-deadline:
			t.Fatal("prompt never registered")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestTelegramApproval_AllowPress(t *testing.T) {
	_, c, ed := approvalTestChannel(t)

	out, id := approveAsync(t, c, 42, tools.ApprovalRequest{Tool: "shell", Command: "rm -rf ./build", WorkingDir: "/ws"})
	press(t, c, 42, "allow", id)

	if d := <-out; d != tools.Approve {
		t.Fatalf("decision = %v, want approve", d)
	}

	calls := ed.snapshot()
	if len(calls) < 2 {
		t.Fatalf("want a send and a settle edit, got %d calls", len(calls))
	}
	// The prompt carries the exact command, unabridged, and no parse mode.
	if !strings.Contains(calls[0].text, "rm -rf ./build") || !strings.Contains(calls[0].text, "/ws") {
		t.Errorf("prompt must show the whole command and dir, got %q", calls[0].text)
	}
	if calls[0].mode != "" {
		t.Errorf("prompt must be plain text, got mode %q", calls[0].mode)
	}
	last := calls[len(calls)-1]
	if !last.edit || !strings.Contains(last.text, "✅") {
		t.Errorf("settled prompt must be edited with the outcome, got %+v", last)
	}
}

func TestTelegramApproval_DenyPressAndUnknownAction(t *testing.T) {
	for _, action := range []string{"deny", "banana"} {
		t.Run(action, func(t *testing.T) {
			_, c, _ := approvalTestChannel(t)
			out, id := approveAsync(t, c, 42, tools.ApprovalRequest{Tool: "shell", Command: "id"})
			press(t, c, 42, action, id)
			if d := <-out; d != tools.Deny {
				t.Fatalf("action %q: decision = %v, want deny", action, d)
			}
		})
	}
}

// The turn's context bounds the wait: no press means deny, with the prompt
// visibly settled so the dead keyboard is not left looking live.
func TestTelegramApproval_TimeoutDenies(t *testing.T) {
	_, c, ed := approvalTestChannel(t)
	a := c.ApproverFor("42")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	d, err := a.Approve(ctx, tools.ApprovalRequest{Tool: "shell", Command: "id"})
	if d != tools.Deny || err == nil {
		t.Fatalf("timeout must deny with an error, got %v, %v", d, err)
	}
	calls := ed.snapshot()
	last := calls[len(calls)-1]
	if !last.edit || !strings.Contains(last.text, "denied") {
		t.Errorf("expired prompt must be settled, got %+v", last)
	}
	// The pending table must not leak the abandoned prompt.
	c.mu.Lock()
	n := len(c.pending)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("pending table leaks %d entries", n)
	}
}

// "Allow all" is remembered only under interactive mode, and the next gated
// command passes without a new prompt.
func TestTelegramApproval_AllowAllRemembersForSession(t *testing.T) {
	_, c, ed := approvalTestChannel(t)

	out, id := approveAsync(t, c, 42, tools.ApprovalRequest{Tool: "shell", Command: "id"})
	press(t, c, 42, "all", id)
	if d := <-out; d != tools.Approve {
		t.Fatalf("decision = %v, want approve", d)
	}

	before := len(ed.snapshot())
	a := c.ApproverFor("42")
	d, err := a.Approve(context.Background(), tools.ApprovalRequest{Tool: "shell", Command: "ls"})
	if d != tools.Approve || err != nil {
		t.Fatalf("remembered approval: got %v, %v", d, err)
	}
	if len(ed.snapshot()) != before {
		t.Error("a remembered approval must not send a new prompt")
	}
}

// Under "always" every command is a fresh decision: no session button on the
// keyboard, and even a forged "all" press is not remembered.
func TestTelegramApproval_AlwaysModeDoesNotRemember(t *testing.T) {
	tg := newTestTelegramChannel()
	ed := &fakeEditor{}
	tg.mu.Lock()
	tg.editor = ed
	tg.mu.Unlock()
	c, err := tg.NewShellApprovalCoordinator(tools.ApprovalAlways)
	if err != nil {
		t.Fatalf("NewShellApprovalCoordinator: %v", err)
	}

	out, id := approveAsync(t, c, 42, tools.ApprovalRequest{Tool: "shell", Command: "id"})
	press(t, c, 42, "all", id)
	if d := <-out; d != tools.Approve {
		t.Fatalf("decision = %v, want approve", d)
	}
	c.mu.Lock()
	remembered := c.approvedAll
	c.mu.Unlock()
	if remembered {
		t.Error("always mode must not remember an all answer")
	}
}

// A press from a different chat than the prompt was sent to is not an answer:
// the request settles as denied.
func TestTelegramApproval_PressFromOtherChatDenies(t *testing.T) {
	_, c, _ := approvalTestChannel(t)
	out, id := approveAsync(t, c, 42, tools.ApprovalRequest{Tool: "shell", Command: "id"})
	press(t, c, 99, "allow", id)
	if d := <-out; d != tools.Deny {
		t.Fatalf("cross-chat press must deny, got %v", d)
	}
}

// A stale or replayed press (already settled, or never existed) is ignored
// without error.
func TestTelegramApproval_StalePressIsIgnored(t *testing.T) {
	_, c, _ := approvalTestChannel(t)
	press(t, c, 42, "allow", "no-such-id")
}

// A send failure denies rather than hangs — the prompt never reached a human.
func TestTelegramApproval_SendFailureDenies(t *testing.T) {
	_, c, ed := approvalTestChannel(t)
	ed.mu.Lock()
	ed.sendErrs = []error{errors.New("chat not found")}
	ed.mu.Unlock()

	a := c.ApproverFor("42")
	d, err := a.Approve(context.Background(), tools.ApprovalRequest{Tool: "shell", Command: "id"})
	if d != tools.Deny || !errors.Is(err, tools.ErrDenied) {
		t.Fatalf("send failure must deny with ErrDenied, got %v, %v", d, err)
	}
}

// A non-numeric chat id yields no approver at all, which keeps the caller on
// the fail-closed no-approver path.
func TestTelegramApproval_ApproverForRejectsBadID(t *testing.T) {
	_, c, _ := approvalTestChannel(t)
	if a := c.ApproverFor("not-a-chat"); a != nil {
		t.Fatal("a garbage chat id must yield no approver")
	}
}

// The keyboard's callback data must round-trip through the real encoder and
// decoder — the namespace is claimed via RegisterCallback, so a second
// coordinator on the same channel must be refused.
func TestTelegramApproval_NamespaceIsClaimedOnce(t *testing.T) {
	tg, _, _ := approvalTestChannel(t)
	if _, err := tg.NewShellApprovalCoordinator(tools.ApprovalInteractive); err == nil {
		t.Fatal("second coordinator must be refused: duplicate namespace")
	}
}
