package channels

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

func TestCLIChannel_HandleCommand(t *testing.T) {
	ctx := context.Background()

	t.Run("quit returns ErrQuit", func(t *testing.T) {
		c := NewCLIChannel(bus.NewMessageBus())
		if err := c.handleCommand(ctx, "/quit"); !errors.Is(err, ErrQuit) {
			t.Errorf("expected ErrQuit, got %v", err)
		}
	})

	t.Run("exit returns ErrQuit", func(t *testing.T) {
		c := NewCLIChannel(bus.NewMessageBus())
		if err := c.handleCommand(ctx, "/exit"); !errors.Is(err, ErrQuit) {
			t.Errorf("expected ErrQuit, got %v", err)
		}
	})

	t.Run("commands are case-insensitive", func(t *testing.T) {
		c := NewCLIChannel(bus.NewMessageBus())
		if err := c.handleCommand(ctx, "/QUIT"); !errors.Is(err, ErrQuit) {
			t.Errorf("expected /QUIT to be recognised, got %v", err)
		}
	})

	t.Run("help prints and continues", func(t *testing.T) {
		c := NewCLIChannel(bus.NewMessageBus())
		if err := c.handleCommand(ctx, "/help"); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("clear prints and continues", func(t *testing.T) {
		c := NewCLIChannel(bus.NewMessageBus())
		if err := c.handleCommand(ctx, "/clear"); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("history prints and continues", func(t *testing.T) {
		c := NewCLIChannel(bus.NewMessageBus())
		c.addToHistory("earlier input")
		if err := c.handleCommand(ctx, "/history"); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("unknown command is reported and continues", func(t *testing.T) {
		c := NewCLIChannel(bus.NewMessageBus())
		if err := c.handleCommand(ctx, "/nope"); err != nil {
			t.Errorf("expected an unknown command to be non-fatal, got %v", err)
		}
	})
}

func TestCLIChannel_HandleCommandNewClearsHistoryAndForwards(t *testing.T) {
	mb := bus.NewMessageBus()
	c := NewCLIChannel(mb)

	c.addToHistory("one")
	c.addToHistory("two")

	if err := c.handleCommand(context.Background(), "/new"); err != nil {
		t.Fatalf("handleCommand(/new) = %v", err)
	}

	if len(c.inputHistory) != 0 {
		t.Errorf("expected history to be cleared, got %v", c.inputHistory)
	}
	if c.historyPos != 0 {
		t.Errorf("expected historyPos to reset to 0, got %d", c.historyPos)
	}

	select {
	case msg := <-mb.InboundChannel():
		if msg.Content != "/new" {
			t.Errorf("expected /new to be forwarded to the bus, got %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Error("expected /new to be forwarded to the bus")
	}
}

func TestCLIChannel_SendToBus(t *testing.T) {
	mb := bus.NewMessageBus()
	c := NewCLIChannel(mb)

	if err := c.sendToBus(context.Background(), "hello agent"); err != nil {
		t.Fatalf("sendToBus returned %v", err)
	}

	select {
	case msg := <-mb.InboundChannel():
		if msg.Content != "hello agent" {
			t.Errorf("Content = %q, want %q", msg.Content, "hello agent")
		}
		if msg.SenderID != "cli_user" {
			t.Errorf("SenderID = %q, want cli_user", msg.SenderID)
		}
		if msg.Channel != "cli" {
			t.Errorf("Channel = %q, want cli", msg.Channel)
		}
		if msg.Metadata["username"] != "user" {
			t.Errorf("Metadata[username] = %v, want user", msg.Metadata["username"])
		}
	case <-time.After(time.Second):
		t.Error("expected the message to reach the bus")
	}
}

func TestCLIChannel_ProcessInput(t *testing.T) {
	mb := bus.NewMessageBus()
	c := NewCLIChannel(mb)
	ctx := context.Background()

	t.Run("blank input is ignored", func(t *testing.T) {
		if err := c.processInput(ctx, "   "); err != nil {
			t.Errorf("expected blank input to be a no-op, got %v", err)
		}
		if len(c.inputHistory) != 0 {
			t.Errorf("blank input should not be recorded, got %v", c.inputHistory)
		}
	})

	t.Run("slash input is routed to handleCommand", func(t *testing.T) {
		if err := c.processInput(ctx, "/quit"); !errors.Is(err, ErrQuit) {
			t.Errorf("expected /quit to route to handleCommand, got %v", err)
		}
	})

	t.Run("plain input is recorded and sent", func(t *testing.T) {
		if err := c.processInput(ctx, "  what is up  "); err != nil {
			t.Fatalf("processInput returned %v", err)
		}
		last := c.inputHistory[len(c.inputHistory)-1]
		if last != "what is up" {
			t.Errorf("expected the trimmed input to be recorded, got %q", last)
		}
		select {
		case msg := <-mb.InboundChannel():
			if msg.Content != "what is up" {
				t.Errorf("Content = %q, want %q", msg.Content, "what is up")
			}
		case <-time.After(time.Second):
			t.Error("expected the message to reach the bus")
		}
	})
}

func TestCLIChannel_SendIsNoOpWhenNotRunning(t *testing.T) {
	c := NewCLIChannel(bus.NewMessageBus())
	// running is false, so Send should return without printing.
	if err := c.Send(bus.OutboundMessage{Channel: "cli", Content: "ignored"}); err != nil {
		t.Errorf("expected Send to be a no-op, got %v", err)
	}
}

func TestCLIChannel_ConsumeOutboundStopsOnContextCancel(t *testing.T) {
	c := NewCLIChannel(bus.NewMessageBus())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		c.consumeOutbound(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeOutbound did not return after the context was cancelled")
	}
}

func TestCLIChannel_ConsumeOutboundStopsOnStopCh(t *testing.T) {
	c := NewCLIChannel(bus.NewMessageBus())

	done := make(chan struct{})
	go func() {
		c.consumeOutbound(context.Background())
		close(done)
	}()

	close(c.stopCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeOutbound did not return after stopCh was closed")
	}
}

// The consumer must print messages addressed to "cli" or "all" and ignore
// those for other channels. Asserting only that the goroutine exits would
// pass even if the filter were removed entirely.
func TestCLIChannel_ConsumeOutboundFiltersByChannel(t *testing.T) {
	mb := bus.NewMessageBus()
	c := NewCLIChannel(mb)

	c.mu.Lock()
	c.running = true
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := captureStdout(t, func() {
		done := make(chan struct{})
		go func() {
			c.consumeOutbound(ctx)
			close(done)
		}()

		mb.OutboundChan() <- bus.OutboundMessage{Channel: "cli", Content: "MARKER_CLI"}
		mb.OutboundChan() <- bus.OutboundMessage{Channel: "telegram", Content: "MARKER_TELEGRAM"}
		mb.OutboundChan() <- bus.OutboundMessage{Channel: "all", Content: "MARKER_ALL"}

		// Give the consumer a chance to drain before shutting it down.
		deadline := time.After(2 * time.Second)
		for {
			if _, outbound := mb.QueueLength(); outbound == 0 {
				break
			}
			select {
			case <-deadline:
				t.Error("outbound queue did not drain")
				cancel()
				<-done
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		time.Sleep(50 * time.Millisecond)

		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("consumeOutbound did not return after the context was cancelled")
		}
	})

	if !strings.Contains(out, "MARKER_CLI") {
		t.Error("expected a message addressed to cli to be printed")
	}
	if !strings.Contains(out, "MARKER_ALL") {
		t.Error(`expected a message addressed to "all" to be printed`)
	}
	if strings.Contains(out, "MARKER_TELEGRAM") {
		t.Error("a message for another channel should not have been printed")
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns what
// was written. The CLI channel prints directly rather than through a writer,
// so this is the only seam available.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	collected := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		collected <- buf.String()
	}()

	fn()

	os.Stdout = orig
	_ = w.Close()
	out := <-collected
	_ = r.Close()
	return out
}

func TestCLIChannel_StopWhenNotRunning(t *testing.T) {
	c := NewCLIChannel(bus.NewMessageBus())
	if err := c.Stop(); err != nil {
		t.Errorf("Stop on a non-running channel should be a no-op, got %v", err)
	}
}

func TestGetTerminalWidth(t *testing.T) {
	// Under `go test` stdout is not a terminal, so this returns 0. The point
	// is that it degrades instead of failing.
	if w := getTerminalWidth(); w < 0 {
		t.Errorf("getTerminalWidth() = %d, want a non-negative width", w)
	}
}
