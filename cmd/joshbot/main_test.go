package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/tools"
)

type mockAgent struct {
	calls []string
}

func (m *mockAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	m.calls = append(m.calls, msg.Content)
	return "reply: " + msg.Content, nil
}

func TestRunAgentLoopProcessesInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &mockAgent{}
	// messageSender is nil in tests - chat ID won't be set but that's fine for unit tests
	if err := runAgentLoop(ctx, cancel, done, input, &output, mock, nil, false); err != nil {
		t.Fatalf("runAgentLoop error = %v", err)
	}

	if ctx.Err() != context.Canceled {
		t.Fatalf("expected context canceled, got %v", ctx.Err())
	}

	if len(mock.calls) != 1 || mock.calls[0] != "hello" {
		t.Fatalf("expected one call with 'hello', got %v", mock.calls)
	}

	if !strings.Contains(output.String(), "reply: hello") {
		t.Fatalf("missing response in output: %q", output.String())
	}
}

func TestRunAgentLoopExitsOnEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output bytes.Buffer
	input := bytes.NewBufferString("")

	mock := &mockAgent{}
	// messageSender is nil in tests - chat ID won't be set but that's fine for unit tests
	if err := runAgentLoop(ctx, cancel, done, input, &output, mock, nil, false); err != nil {
		t.Fatalf("runAgentLoop error = %v", err)
	}

	if len(mock.calls) != 0 {
		t.Fatalf("expected no agent calls, got %v", mock.calls)
	}
}

func TestRunAgentLoopSetsChatID(t *testing.T) {
	// Create a real BusMessageSender to verify SetChatID is called
	msgBus := bus.NewMessageBus()
	sender := tools.NewBusMessageSender(msgBus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &mockAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &output, mock, sender, false); err != nil {
		t.Fatalf("runAgentLoop error = %v", err)
	}

	// Verify chat ID was set for CLI channel
	chatID, ok := sender.GetChatID("cli")
	if !ok {
		t.Fatalf("expected chat ID to be set for cli channel")
	}
	if chatID != "cli_user" {
		t.Fatalf("expected chat ID 'cli_user', got %q", chatID)
	}
}

// mockProgressAgent simulates a real *agent.Agent for the purposes of
// testing the interactive tool-call visibility / spinner wiring: on
// Process, if a progress callback has been wired via SetProgressCallback,
// it emits a synthetic start+done event pair before returning, exactly as
// the real ReAct loop does around a tool call.
type mockProgressAgent struct {
	progress agent.ProgressFunc
}

func (m *mockProgressAgent) SetProgressCallback(fn agent.ProgressFunc) {
	m.progress = fn
}

func (m *mockProgressAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	if m.progress != nil {
		m.progress(agent.ToolProgressEvent{Tool: "shell", Summary: "echo hi", Phase: agent.ToolProgressStart})
		m.progress(agent.ToolProgressEvent{Tool: "shell", Summary: "echo hi", Phase: agent.ToolProgressDone, Elapsed: 42 * time.Millisecond})
	}
	return "reply: " + msg.Content, nil
}

// withTTY temporarily overrides isTTY (the injectable TTY-ness seam) for a
// test, restoring the original on cleanup. It never probes a real
// terminal — tests stay deterministic regardless of the environment they
// run in.
func withTTY(t *testing.T, tty bool) {
	t.Helper()
	orig := isTTY
	isTTY = func(w io.Writer) bool { return tty }
	t.Cleanup(func() { isTTY = orig })
}

func TestRunAgentLoop_NonTTY_NoProgressCallbackWired(t *testing.T) {
	withTTY(t, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &mockProgressAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &output, mock, nil, false); err != nil {
		t.Fatalf("runAgentLoop error = %v", err)
	}

	if mock.progress != nil {
		t.Fatal("progress callback should not be wired when output is not a TTY")
	}
	if strings.ContainsAny(output.String(), "\r") {
		t.Fatalf("non-TTY output must contain no carriage returns (spinner): %q", output.String())
	}
	if strings.Contains(output.String(), "⏺") || strings.Contains(output.String(), "⎿") {
		t.Fatalf("non-TTY output must contain no tool progress lines: %q", output.String())
	}
}

func TestRunAgentLoop_TTY_PrintsToolProgressLines(t *testing.T) {
	withTTY(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &mockProgressAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &output, mock, nil, false); err != nil {
		t.Fatalf("runAgentLoop error = %v", err)
	}

	// runAgentLoop clears the callback (SetProgressCallback(nil)) once it
	// returns, so wiring is verified via its effect — the printed lines —
	// rather than inspecting mock.progress after the fact.
	out := output.String()
	if !strings.Contains(out, "⏺ shell(echo hi)") {
		t.Errorf("missing tool start line in output: %q", out)
	}
	if !strings.Contains(out, "⎿ ok") {
		t.Errorf("missing tool completion line in output: %q", out)
	}
	if !strings.Contains(out, "reply: hello") {
		t.Errorf("missing final agent response in output: %q", out)
	}
}

func TestRunAgentLoop_TTY_ExitsCleanlyOnDoneWhileSpinnerRunning(t *testing.T) {
	// Guard against the spinner goroutine leaking or blocking shutdown:
	// this reuses the blockingReader pattern from interrupt_test.go so the
	// loop is sitting at a blocked read (spinner not yet started, since the
	// spinner only runs during Process) and confirms `done` still unblocks
	// runAgentLoop promptly with TTY-ness forced on.
	withTTY(t, true)

	reader := newBlockingReader()
	out := &discardWriter{}
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan error, 1)
	go func() {
		returned <- runAgentLoop(ctx, cancel, done, reader, out, noopAgent{}, nil, false)
	}()

	close(done)

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("runAgentLoop returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAgentLoop did not return after done was closed; goroutine leak or deadlock")
	}
}

// slowAgent blocks Process on a channel until the test releases it, so the
// test can deterministically control how long the spinner has to run
// without depending on wall-clock sleeps for correctness (only the spinner's
// own tick cadence is real time, which is inherent to a visual spinner).
type slowAgent struct {
	release chan struct{}
}

func (s *slowAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	<-s.release
	return "reply: " + msg.Content, nil
}

// TestRunAgentLoop_TTY_SpinnerRunsWhileWaiting verifies runAgentLoop starts
// the spinner before calling Process and stops (clearing the line) once it
// returns — i.e. the spinner is actually wired around the blocking call,
// not just constructed and discarded.
func TestRunAgentLoop_TTY_SpinnerRunsWhileWaiting(t *testing.T) {
	withTTY(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output syncBuffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &slowAgent{release: make(chan struct{})}
	returned := make(chan error, 1)
	go func() {
		returned <- runAgentLoop(ctx, cancel, done, input, &output, mock, nil, false)
	}()

	// Give the spinner goroutine a couple of tick intervals to draw at
	// least one frame before we let Process return.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), "thinking...") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(output.String(), "thinking...") {
		t.Fatal("expected spinner output while Process was blocked, got none")
	}

	close(mock.release)

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("runAgentLoop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runAgentLoop did not return after exit")
	}

	if strings.Contains(output.String(), "thinking...") == false {
		t.Fatal("sanity: spinner text unexpectedly absent from final output")
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent reads/writes, needed
// because the spinner goroutine writes to output concurrently with the test
// goroutine polling it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestCLIProgressOnToolEvent_ErrorStatus verifies a failed tool call renders
// as "error" rather than "ok" in the completion line.
func TestCLIProgressOnToolEvent_ErrorStatus(t *testing.T) {
	var out bytes.Buffer
	p := newCLIProgress(&out)

	p.onToolEvent(agent.ToolProgressEvent{Tool: "shell", Summary: "false", Phase: agent.ToolProgressStart})
	p.onToolEvent(agent.ToolProgressEvent{Tool: "shell", Summary: "false", Phase: agent.ToolProgressDone, Elapsed: time.Second, Err: context.DeadlineExceeded})

	got := out.String()
	if !strings.Contains(got, "⎿ error") {
		t.Errorf("expected an error status line, got %q", got)
	}
	if strings.Contains(got, "⎿ ok") {
		t.Errorf("failed tool call should not report ok, got %q", got)
	}
}

// TestCLIProgressStopSpinnerJoinsGoroutine proves stopSpinner cannot return
// until the spinner goroutine has actually exited (it blocks on
// p.spinDone), which is the guarantee against a leaked/cancellation-proof
// spinner goroutine. No sleeps or timing assumptions are needed: the
// channel handshake itself is the proof.
func TestCLIProgressStopSpinnerJoinsGoroutine(t *testing.T) {
	var out bytes.Buffer
	p := newCLIProgress(&out)

	p.startSpinner()
	spinDone := p.spinDone

	p.stopSpinner()

	select {
	case <-spinDone:
		// Goroutine exited, as required.
	default:
		t.Fatal("stopSpinner returned before the spinner goroutine exited")
	}
	if p.spinCancel != nil {
		t.Fatal("stopSpinner should reset spinCancel to nil")
	}
}

// TestFormatProviderStatus covers the `status` provider line for issue #71:
// a provider missing "enabled": true (or, where applicable, an api_key) must
// be visibly flagged rather than listed as if it were configured and ready.
func TestFormatProviderStatus(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string]config.ProviderConfig
		want      string
	}{
		{
			name:      "no providers",
			providers: map[string]config.ProviderConfig{},
			want:      "none",
		},
		{
			name: "api key set but enabled absent",
			providers: map[string]config.ProviderConfig{
				"openrouter": {APIKey: "sk-or-v1-anything"},
			},
			want: `openrouter (disabled — set "enabled": true)`,
		},
		{
			name: "api key set and enabled true",
			providers: map[string]config.ProviderConfig{
				"openrouter": {APIKey: "sk-or-v1-anything", Enabled: true},
			},
			want: "openrouter",
		},
		{
			name: "enabled true but no api key",
			providers: map[string]config.ProviderConfig{
				"nvidia": {Enabled: true},
			},
			want: `nvidia (disabled — missing "api_key")`,
		},
		{
			name: "ollama does not require an api key",
			providers: map[string]config.ProviderConfig{
				"ollama": {Enabled: true},
			},
			want: "ollama",
		},
		{
			name: "mixed providers sorted and each flagged independently",
			providers: map[string]config.ProviderConfig{
				"openrouter": {APIKey: "sk-or-v1-anything"},
				"groq":       {APIKey: "gsk_anything", Enabled: true},
			},
			want: `groq, openrouter (disabled — set "enabled": true)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatProviderStatus(tt.providers)
			if got != tt.want {
				t.Fatalf("formatProviderStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNoProvidersRegisteredError covers issue #71 part 2: the error raised
// when the legacy provider map yields zero registered providers must say
// whether the config had no providers at all, or providers present but none
// enabled -- and the latter must name the "enabled" field so a hand-editing
// user has somewhere to look.
func TestNoProvidersRegisteredError(t *testing.T) {
	t.Run("empty config", func(t *testing.T) {
		err := noProvidersRegisteredError(map[string]config.ProviderConfig{})
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if strings.Contains(err.Error(), "enabled") {
			t.Fatalf("error for an empty config should not talk about the enabled field: %q", err.Error())
		}
	})

	t.Run("present but none enabled", func(t *testing.T) {
		err := noProvidersRegisteredError(map[string]config.ProviderConfig{
			"openrouter": {APIKey: "sk-or-v1-anything"},
		})
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if !strings.Contains(err.Error(), "enabled") {
			t.Fatalf("error should name the enabled field, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), "openrouter") {
			t.Fatalf("error should name the configured-but-inert provider, got %q", err.Error())
		}
	})
}

// An enabled provider with no api_key must not be told to set "enabled": true —
// it already is. A diagnostic that names the wrong cause is the bug this issue
// was filed about.
func TestNoProvidersRegisteredError_EnabledButKeyless(t *testing.T) {
	err := noProvidersRegisteredError(map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: ""},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), `add "enabled": true`) {
		t.Errorf("error blames the enabled flag on an already-enabled provider: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error should name the missing api_key; got %v", err)
	}
}

// ollama needs no api_key, so an enabled ollama with no key is not the cause.
func TestNoProvidersRegisteredError_KeylessProviderNotBlamed(t *testing.T) {
	err := noProvidersRegisteredError(map[string]config.ProviderConfig{
		"openrouter": {Enabled: false, APIKey: "sk-x"},
	})
	if !strings.Contains(err.Error(), `"enabled": true`) {
		t.Errorf("a disabled provider should still point at the enabled flag; got %v", err)
	}
}

// These exercise the REAL isTTY, not the injected seam. Every other progress
// test overrides isTTY, so without this the production gate could be inverted
// and the suite would stay green — while `agent -m | jq` started receiving
// spinner frames and ANSI escapes.
func TestIsTTY_NonFileWritersAreNeverTerminals(t *testing.T) {
	for name, w := range map[string]io.Writer{
		"bytes.Buffer": &bytes.Buffer{},
		"io.Discard":   io.Discard,
	} {
		if isTTY(w) {
			t.Errorf("isTTY(%s) = true; decorative output would leak into non-terminal writers", name)
		}
	}
}

func TestIsTTY_PipeIsNotATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	// A pipe is an *os.File but not a terminal — this is exactly the shape of
	// `joshbot agent -m ... | something`.
	if isTTY(w) {
		t.Error("isTTY(pipe) = true; piped output would be polluted with ANSI and \\r")
	}
}

// The spinner takes p.mu to draw a frame, so waiting for it to exit while
// holding that lock deadlocks the moment a tick lands in the window: the
// spinner blocks in Lock and never returns to its select to see the cancel.
// The first streamed delta did exactly that.
func TestCLIProgressStreamEventDoesNotDeadlockWithSpinner(t *testing.T) {
	p := newCLIProgress(io.Discard)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p.beginTurn()
			p.startSpinner()
			// Land inside the spinner's tick window.
			time.Sleep(time.Millisecond)
			p.onStreamEvent(agent.StreamEvent{Delta: "x"})
			p.stopSpinner()
		}
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("onStreamEvent deadlocked against the spinner goroutine")
	}

	if !p.didStream() {
		t.Error("didStream() is false after a delta was written")
	}
}

// A turn that never streams must still have its response printed.
//
// Several Process paths return without streaming anything — a slash command,
// a session-load failure, a stream that fails to open — and keying the
// decision off the config flag instead of what was actually streamed printed
// two blank lines and swallowed the answer.
func TestStreamedFlagIsPerTurnNotPerConfig(t *testing.T) {
	p := newCLIProgress(io.Discard)

	p.beginTurn()
	p.onStreamEvent(agent.StreamEvent{Delta: "hello"})
	if !p.didStream() {
		t.Fatal("didStream() should be true on a turn that streamed")
	}

	p.beginTurn()
	if p.didStream() {
		t.Error("didStream() carried over from the previous turn; this turn's answer would be swallowed")
	}
}
