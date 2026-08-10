package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// approverFunc adapts a function to the Approver interface.
type approverFunc func(context.Context, ApprovalRequest) (Decision, error)

func (f approverFunc) Approve(ctx context.Context, req ApprovalRequest) (Decision, error) {
	return f(ctx, req)
}

func gatedShell(t *testing.T, mode ApprovalMode) *ShellTool {
	t.Helper()
	// An empty allowlist would become the read-only default on a platform with
	// no sandbox, which would refuse these commands before the gate ever ran.
	tool := NewShellTool(5*time.Second, t.TempDir(), false, "echo")
	tool.SetApproval(mode)
	return tool
}

// The default is off: turning a gate on changes what an existing install can
// do, and a silent change of that kind is how an upgrade breaks a cron job.
func TestShellApproval_OffByDefault(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false, "echo")
	res := tool.Execute(context.Background(), map[string]any{"command": "echo hi"})
	if res.Error != nil {
		t.Fatalf("ungated shell refused a command: %v", res.Error)
	}
}

// The reason this issue exists: cron and heartbeat start turns with nobody
// watching. Those requests carry no approver, and the gate must refuse them
// *immediately* — a gate that blocks turns the heartbeat scanner into a hung
// goroutine, and one that opens is not a gate.
func TestShellApproval_UnattendedTurnIsDeniedWithoutBlocking(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)

	done := make(chan ToolResult, 1)
	go func() {
		done <- tool.Execute(context.Background(), map[string]any{"command": "echo hi"})
	}()

	select {
	case res := <-done:
		if res.Error == nil {
			t.Fatal("a turn with no approver was allowed to run a command")
		}
		if !errors.Is(res.Error, ErrDenied) {
			t.Errorf("denial is not reported as ErrDenied: %v", res.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a turn with no approver blocked instead of being denied")
	}
}

// A gate that opens because nobody answered in time is not a gate. The
// approver here is the shape of a real one — it waits on the context — and
// the shell tool must read that as a denial.
func TestShellApproval_TimeoutIsDenied(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)
	blocked := approverFunc(func(ctx context.Context, _ ApprovalRequest) (Decision, error) {
		<-ctx.Done()
		return Deny, ctx.Err()
	})

	ctx, cancel := context.WithTimeout(WithApprover(context.Background(), blocked), 50*time.Millisecond)
	defer cancel()

	res := tool.Execute(ctx, map[string]any{"command": "echo hi"})
	if res.Error == nil {
		t.Fatal("a prompt that timed out was treated as approval")
	}
	if !errors.Is(res.Error, ErrDenied) {
		t.Errorf("timeout denial is not reported as ErrDenied: %v", res.Error)
	}
}

// Cancellation is the other way a prompt goes unanswered — the user hits
// Ctrl-C while the question is on screen.
func TestShellApproval_CancellationIsDenied(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)
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

	if res := tool.Execute(ctx, map[string]any{"command": "echo hi"}); res.Error == nil {
		t.Fatal("a cancelled prompt was treated as approval")
	}
}

// An approver that returns Approve *and* an error is not a yes. This is the
// path a buggy or half-written approver takes, and the safe reading of a
// contradictory answer is the denial.
func TestShellApproval_ApproveWithErrorIsDenied(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)
	confused := approverFunc(func(context.Context, ApprovalRequest) (Decision, error) {
		return Approve, errors.New("the terminal went away")
	})

	ctx := WithApprover(context.Background(), confused)
	if res := tool.Execute(ctx, map[string]any{"command": "echo hi"}); res.Error == nil {
		t.Fatal("an approval that came with an error was honoured")
	}
}

func TestShellApproval_ApprovedCommandRuns(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)
	ctx := WithApprover(context.Background(), approverFunc(
		func(context.Context, ApprovalRequest) (Decision, error) { return Approve, nil }))

	res := tool.Execute(ctx, map[string]any{"command": "echo approved"})
	if res.Error != nil {
		t.Fatalf("approved command did not run: %v", res.Error)
	}
	if !strings.Contains(res.Output, "approved") {
		t.Errorf("approved command produced no output: %q", res.Output)
	}
}

// The operator has to see what they are approving. The binary alone says
// nothing — `rm -rf ./build` and `rm -rf /` are the same first word.
func TestShellApproval_RequestCarriesTheWholeCommand(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)
	const cmd = `echo one two three --flag "with spaces"`

	var got ApprovalRequest
	ctx := WithApprover(context.Background(), approverFunc(
		func(_ context.Context, req ApprovalRequest) (Decision, error) {
			got = req
			return Deny, nil
		}))
	tool.Execute(ctx, map[string]any{"command": cmd})

	if got.Command != cmd {
		t.Errorf("the approval prompt would not show the real command:\n got  %q\n want %q", got.Command, cmd)
	}
	if got.Tool != "shell" {
		t.Errorf("request does not name the tool: %q", got.Tool)
	}
	if got.WorkingDir == "" {
		t.Error("request does not say where the command would run")
	}
}

// async=true must not be a way around the gate.
func TestShellApproval_AsyncPathIsGatedToo(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)

	results := make(chan AsyncResult, 1)
	res := tool.ExecuteAsync(context.Background(), map[string]any{"command": "echo hi"},
		func(r AsyncResult) { results <- r })
	if res.Error == nil {
		t.Fatal("the async path ran a command with no approver")
	}
	select {
	case r := <-results:
		if r.Error == nil {
			t.Error("async callback reported success for a denied command")
		}
	case <-time.After(time.Second):
		t.Error("async denial never reached the callback")
	}
}

// The gate runs after the deny list, not before. Prompting for a command that
// was going to be refused anyway trains the operator to approve on reflex.
func TestShellApproval_DeniedCommandsNeverPrompt(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false, "rm")
	tool.SetApproval(ApprovalInteractive)

	prompted := false
	ctx := WithApprover(context.Background(), approverFunc(
		func(context.Context, ApprovalRequest) (Decision, error) {
			prompted = true
			return Approve, nil
		}))
	res := tool.Execute(ctx, map[string]any{"command": "rm -rf /"})

	if res.Error == nil {
		t.Fatal("a deny-listed command ran")
	}
	if prompted {
		t.Error("a command the deny list refuses anyway produced an approval prompt")
	}
}

// Telegram runs Process concurrently, so an approver on the Agent struct would
// hand one conversation's decision to another's command. Carrying it on the
// context is what prevents that, and this pins it.
func TestShellApproval_ConcurrentRequestsDoNotCrossDeliver(t *testing.T) {
	tool := gatedShell(t, ApprovalInteractive)

	seen := map[string][]string{}
	var mu sync.Mutex
	approverFor := func(name string) Approver {
		return approverFunc(func(_ context.Context, req ApprovalRequest) (Decision, error) {
			mu.Lock()
			seen[name] = append(seen[name], req.Command)
			mu.Unlock()
			return Approve, nil
		})
	}

	var wg sync.WaitGroup
	for _, name := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			ctx := WithApprover(context.Background(), approverFor(name))
			tool.Execute(ctx, map[string]any{"command": "echo " + name})
		}(name)
	}
	wg.Wait()

	for _, name := range []string{"alice", "bob"} {
		if want := []string{"echo " + name}; len(seen[name]) != 1 || seen[name][0] != want[0] {
			t.Errorf("%s's approver saw %v, want %v", name, seen[name], want)
		}
	}
}

// A nil approver must not be a way to switch the gate off.
func TestWithApproverNilStillDenies(t *testing.T) {
	ctx := WithApprover(context.Background(), nil)
	if d, _ := ApproverFromContext(ctx).Approve(ctx, ApprovalRequest{}); d != Deny {
		t.Fatal("WithApprover(nil) produced an approver that says yes")
	}
}

func TestApproverFromContext_DefaultsToDenyAll(t *testing.T) {
	d, err := ApproverFromContext(context.Background()).Approve(context.Background(), ApprovalRequest{})
	if d != Deny {
		t.Error("a context with no approver did not default to deny")
	}
	if !errors.Is(err, ErrDenied) {
		t.Errorf("default denial is not ErrDenied: %v", err)
	}
	// A nil context is a caller bug, not an open gate.
	//nolint:staticcheck // deliberately passing a nil context
	if d, _ := ApproverFromContext(nil).Approve(context.Background(), ApprovalRequest{}); d != Deny {
		t.Error("a nil context produced an approving approver")
	}
}

// Deny is the zero value so that every path which fails to produce a real
// answer lands there without anyone remembering to.
func TestDecisionZeroValueIsDeny(t *testing.T) {
	var d Decision
	if d != Deny {
		t.Fatal("the zero Decision is not Deny")
	}
}

func TestParseApprovalMode(t *testing.T) {
	cases := []struct {
		in   string
		want ApprovalMode
		ok   bool
	}{
		{"", ApprovalOff, true},
		{"off", ApprovalOff, true},
		{" Interactive ", ApprovalInteractive, true},
		{"always", ApprovalAlways, true},
		{"ALWAYS", ApprovalAlways, true},
		// The trap the startup error exists for: a typo that silently meant
		// "off" would leave the operator believing commands were gated.
		{"interactve", ApprovalOff, false},
		{"yes", ApprovalOff, false},
	}
	for _, tc := range cases {
		got, ok := ParseApprovalMode(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseApprovalMode(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// The only behavioural difference between the two on modes.
func TestApprovalMode_RemembersSession(t *testing.T) {
	if !ApprovalInteractive.RemembersSession() {
		t.Error("interactive should allow a remembered session approval")
	}
	if ApprovalAlways.RemembersSession() {
		t.Error(`"always" must ask every time; a remembered answer defeats it`)
	}
	if ApprovalOff.RemembersSession() {
		t.Error("an off gate has nothing to remember")
	}
}
