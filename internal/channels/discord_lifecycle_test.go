package channels

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
)

// TestDiscordStartWithoutATokenIsRestartable pins the restart contract. Start
// claims the run via beginRun and must hand it back via abortRun when it bails
// out: leaving running latched makes every later Start report "already
// running", so an operator who fixes their token still has a dead bot. The
// stop signal that beginRun allocated must be closed too, and a *later* Start
// must get a fresh one — reusing the closed channel would make consumeOutbound
// return immediately and every Send abort its retries while the channel looked
// perfectly started.
func TestDiscordStartWithoutATokenIsRestartable(t *testing.T) {
	d := NewDiscordChannel(bus.NewMessageBus(), &config.DiscordConfig{Enabled: true, AllowFrom: []string{"111"}})

	err := d.Start(context.Background())
	if err == nil {
		t.Fatal("Start with no token should have failed")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error should name the token, got %v", err)
	}

	d.mu.RLock()
	running, closed, first := d.running, d.stopClosed, d.stopCh
	d.mu.RUnlock()
	if running {
		t.Fatal("a failed Start left the channel latched as running; a later Start can never succeed")
	}
	if !closed {
		t.Fatal("abortRun left the run's stop signal open; anything already reading it is stranded")
	}
	select {
	case <-first:
	default:
		t.Fatal("stopClosed was set without actually closing the channel")
	}

	// beginRun must allocate a *fresh* stop signal for the next cycle rather
	// than handing out the closed one.
	if err := d.beginRun(); err != nil {
		t.Fatalf("beginRun after an aborted run: %v", err)
	}
	d.mu.RLock()
	second := d.stopCh
	d.mu.RUnlock()
	if second == first {
		t.Fatal("beginRun reused the closed stop channel; the new run is dead on arrival")
	}
	select {
	case <-second:
		t.Fatal("the new run's stop channel is already closed")
	default:
	}

	// Stop must close it exactly once — a second close panics the process.
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestDiscordStartRefusesASecondRun — two runs would open two gateway sessions
// on one token and both would consume from the single outbound channel.
func TestDiscordStartRefusesASecondRun(t *testing.T) {
	d, _ := newTestDiscordChannel(t, []string{"111"})
	if err := d.beginRun(); err != nil {
		t.Fatalf("beginRun: %v", err)
	}
	if err := d.Start(context.Background()); err == nil {
		t.Fatal("Start while already running should have been refused")
	}
	// Refusing must not have torn down the run that was already claimed.
	d.mu.RLock()
	running := d.running
	d.mu.RUnlock()
	if !running {
		t.Fatal("a refused second Start cleared the first run's latch")
	}
}

// TestDiscordConsumeOutboundRoutesByChannel pins the routing rule. The bus has
// one outbound channel that every channel implementation reads competitively,
// so a consumer that took messages not addressed to it would swallow another
// channel's reply with no error anywhere.
func TestDiscordConsumeOutboundRoutesByChannel(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})
	d.mu.Lock()
	d.running = true
	d.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.consumeOutbound(ctx)
		close(done)
	}()

	for _, m := range []bus.OutboundMessage{
		{Channel: "telegram", ChannelID: "c1", Content: "not mine"},
		{Channel: "discord", ChannelID: "c1", Content: "mine"},
		{Channel: "all", ChannelID: "c1", Content: "broadcast"},
	} {
		d.bus.OutboundChan() <- m
	}

	deadline := time.After(2 * time.Second)
	for {
		got := fake.messages()
		if len(got) >= 2 {
			if got[0] != "mine" || got[1] != "broadcast" {
				t.Fatalf("delivered = %v, want [mine broadcast]", got)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("consumeOutbound delivered %v, want the discord and all messages", got)
		case <-time.After(10 * time.Millisecond):
		}
	}

	// A message for another channel must never be delivered, even late.
	time.Sleep(50 * time.Millisecond)
	for _, m := range fake.messages() {
		if m == "not mine" {
			t.Fatal("the discord consumer stole a message addressed to telegram")
		}
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeOutbound did not return after Stop; the consumer leaks across restarts")
	}
}

// consumeOutbound must also honour the caller's context, not only Stop — Start
// is handed the process context and a cancelled one has to unwind the goroutine.
func TestDiscordConsumeOutboundReturnsOnContextCancel(t *testing.T) {
	d, _ := newTestDiscordChannel(t, []string{"111"})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		d.consumeOutbound(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeOutbound ignored context cancellation")
	}
}

// TestDiscordClearTypingOnlyEvictsItsOwnKeepAlive is the ownership rule. A
// keep-alive that hits its max duration clears the map entry; if it cleared the
// entry unconditionally it would evict a *newer* keep-alive started for the same
// channel by the next turn, and that turn would show no typing indicator at all
// while its goroutine kept ticking with nothing able to stop it.
func TestDiscordClearTypingOnlyEvictsItsOwnKeepAlive(t *testing.T) {
	d, _ := newTestDiscordChannel(t, []string{"111"})

	older := make(chan struct{})
	newer := make(chan struct{})
	d.mu.Lock()
	d.typingStop["c1"] = newer
	d.mu.Unlock()

	d.clearTyping("c1", older)

	d.mu.RLock()
	cur, ok := d.typingStop["c1"]
	d.mu.RUnlock()
	if !ok || cur != newer {
		t.Fatal("an expiring keep-alive evicted the newer one for the same channel")
	}

	// Its own entry it must remove.
	d.clearTyping("c1", newer)
	d.mu.RLock()
	_, ok = d.typingStop["c1"]
	d.mu.RUnlock()
	if ok {
		t.Fatal("clearTyping did not remove the entry it owns")
	}
}

// TestDiscordTypingKeepAliveExpires — Discord clears the indicator after ~10s,
// so the keep-alive re-sends; it must also give up at typingMaxDuration instead
// of typing forever at a user who will never get an answer.
func TestDiscordTypingKeepAliveExpires(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})
	d.mu.Lock()
	d.typingInterval = 5 * time.Millisecond
	d.typingMaxDuration = 60 * time.Millisecond
	d.mu.Unlock()

	d.startTyping("c1")
	// A second call for the same channel is a no-op, not a second goroutine.
	d.startTyping("c1")

	deadline := time.After(2 * time.Second)
	for {
		d.mu.RLock()
		_, still := d.typingStop["c1"]
		d.mu.RUnlock()
		if !still {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the typing keep-alive never expired; it types forever")
		case <-time.After(5 * time.Millisecond):
		}
	}

	fake.mu.Lock()
	ticks := fake.typing
	fake.mu.Unlock()
	if ticks < 2 {
		t.Fatalf("keep-alive sent %d typing indicators; it is not keeping anything alive", ticks)
	}

	// stopTyping on a channel that is no longer typing must not panic on a
	// double close.
	d.stopTyping("c1")
	d.stopTyping("")
}

// startTyping with no session must not start a goroutine that will nil-panic on
// its first tick — the channel is used before Start in tests and during startup.
func TestDiscordStartTypingWithoutASession(t *testing.T) {
	d := NewDiscordChannel(bus.NewMessageBus(), &config.DiscordConfig{Enabled: true, Token: "tok"})
	d.startTyping("c1")
	d.startTyping("")

	d.mu.RLock()
	n := len(d.typingStop)
	d.mu.RUnlock()
	if n != 0 {
		t.Fatalf("startTyping registered %d keep-alives with no session to type on", n)
	}
	if d.sender() != nil {
		t.Fatal("sender() invented a session")
	}
}

// reply is best effort and must swallow a send failure rather than propagate
// it: it is used to tell the user the bus is full, and failing there must not
// take down the handler.
func TestDiscordReplySurvivesASendFailure(t *testing.T) {
	d := NewDiscordChannel(bus.NewMessageBus(), &config.DiscordConfig{Enabled: true, Token: "tok"})
	d.reply("c1", "the bus is full") // no session at all
}

// handleCommand forwards every registered command to the agent and answers
// nothing locally; the unknown-command fallback lists the same table.
func TestDiscordHandleCommand(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})

	unknown := unknownDiscordCommandText("/nope")
	for _, c := range discordCommands {
		if !strings.Contains(unknown, "/"+c.Name) {
			t.Errorf("/%s is a registered command but is missing from the unknown-command listing", c.Name)
		}
	}

	d.handleCommand("111", "josh", "c1", "/new")
	select {
	case got := <-d.bus.InboundChannel():
		if got.Content != "/new" {
			t.Errorf("Content = %q, want /new", got.Content)
		}
		if got.Metadata["is_command"] != true {
			t.Errorf("/new must be tagged as a command, metadata = %+v", got.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("/new never reached the agent; the session is never reset")
	}
	if got := fake.messages(); len(got) != 0 {
		t.Fatalf("/new must not be acknowledged locally (the agent's reply is the ack): %v", got)
	}

	// An unrecognised command falls through silently here — dispatch is what
	// answers it — and must not send anything or reach the bus.
	d.handleCommand("111", "josh", "c1", "/nope")
	if got := fake.messages(); len(got) != 0 {
		t.Fatalf("an unknown command was answered by handleCommand: %v", got)
	}
	select {
	case got := <-d.bus.InboundChannel():
		t.Fatalf("an unknown command reached the agent: %+v", got)
	default:
	}
}

// A full bus must be reported to the user rather than dropped: /new silently
// doing nothing looks like the reset worked.
func TestDiscordHandleNewReportsAFullBus(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})
	for d.bus.Send(bus.InboundMessage{Content: "filler", Channel: "discord"}) {
	}

	d.handleCommand("111", "josh", "c1", "/new")
	got := fake.messages()
	if len(got) != 1 || !strings.Contains(strings.ToLower(got[0]), "sorry") {
		t.Fatalf("a dropped /new was not reported to the user: %v", got)
	}
}
