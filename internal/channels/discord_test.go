package channels

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
)

// fakeDiscordSender records sends and typing calls and can be told to fail a
// configurable number of times before succeeding.
type fakeDiscordSender struct {
	mu        sync.Mutex
	sent      []string
	typing    int
	failFirst int
	failErr   error
}

func (f *fakeDiscordSender) ChannelMessageSend(channelID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFirst > 0 {
		f.failFirst--
		return nil, f.failErr
	}
	f.sent = append(f.sent, content)
	return &discordgo.Message{ID: "1", ChannelID: channelID, Content: content}, nil
}

func (f *fakeDiscordSender) ChannelTyping(channelID string, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing++
	return nil
}

func (f *fakeDiscordSender) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

func newTestDiscordChannel(t *testing.T, allow []string) (*DiscordChannel, *fakeDiscordSender) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	d := NewDiscordChannel(msgBus, &config.DiscordConfig{Token: "tok", AllowFrom: allow})
	fake := &fakeDiscordSender{}
	d.send = fake
	return d, fake
}

func TestDiscordChannel_New(t *testing.T) {
	d, _ := newTestDiscordChannel(t, []string{"111", "@Alice"})
	if d.Name() != "discord" {
		t.Fatalf("expected name discord, got %s", d.Name())
	}
	if len(d.allowIDs) != 1 || len(d.allowNames) != 1 {
		t.Fatalf("expected 1 ID and 1 name entry, got %d and %d", len(d.allowIDs), len(d.allowNames))
	}
}

func TestDiscordChannel_IsAllowed(t *testing.T) {
	d, _ := newTestDiscordChannel(t, []string{"123456", "Alice"})

	if !d.IsAllowed("123456", "someuser", "Some User") {
		t.Error("expected match by user ID")
	}
	if !d.IsAllowed("999", "alice", "Whoever") {
		t.Error("expected case-insensitive username match")
	}
	if !d.IsAllowed("999", "other", "alice") {
		t.Error("expected match by global name")
	}
	if d.IsAllowed("999", "unknown", "Unknown") {
		t.Error("expected unknown user to be rejected")
	}
}

func TestDiscordChannel_IsAllowed_EmptyDeniesEveryone(t *testing.T) {
	d, _ := newTestDiscordChannel(t, nil)
	if d.IsAllowed("123", "anyone", "Anyone") {
		t.Error("empty allowlist must deny everyone (fail-closed)")
	}
}

func TestDiscordChannel_DispatchRejectsUnauthorized(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})
	d.dispatch("222", "mallory", "Mallory", "chan1", "hello")

	msgs := fake.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "not authorized") {
		t.Fatalf("expected authorization rejection, got %v", msgs)
	}
	// Nothing should have reached the bus.
	if l, _ := d.bus.QueueLength(); l != 0 {
		t.Fatalf("expected no inbound bus messages, got %d", l)
	}
}

func TestDiscordChannel_DispatchAuthorizedReachesBus(t *testing.T) {
	d, _ := newTestDiscordChannel(t, []string{"111"})
	d.dispatch("111", "alice", "Alice", "chan1", "hello there")

	select {
	case msg := <-d.bus.InboundChannel():
		if msg.Channel != "discord" {
			t.Errorf("expected channel discord, got %s", msg.Channel)
		}
		if msg.SenderID != "discord_111" {
			t.Errorf("expected sender discord_111, got %s", msg.SenderID)
		}
		if msg.Content != "hello there" {
			t.Errorf("unexpected content %q", msg.Content)
		}
		if msg.Metadata["chat_id"] != "chan1" {
			t.Errorf("expected chat_id chan1, got %v", msg.Metadata["chat_id"])
		}
	default:
		t.Fatal("expected an inbound message on the bus")
	}
}

func TestDiscordChannel_UnknownCommandOnlyToAllowed(t *testing.T) {
	// Unauthorized users must never see the command list.
	d, fake := newTestDiscordChannel(t, []string{"111"})
	d.dispatch("222", "mallory", "Mallory", "chan1", "/bogus")
	if len(fake.messages()) != 0 {
		t.Fatalf("unauthorized user should get no command listing, got %v", fake.messages())
	}

	// Authorized user gets the listing.
	d.dispatch("111", "alice", "Alice", "chan1", "/bogus")
	msgs := fake.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Unknown command") || !strings.Contains(msgs[0], "/help") {
		t.Fatalf("expected unknown-command help, got %v", msgs)
	}
}

// /help is the agent's answer, identical on every channel: it is forwarded
// to the bus like any other command and nothing is sent locally.
func TestDiscordChannel_HelpCommand(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})
	d.dispatch("111", "alice", "Alice", "chan1", "/help")
	select {
	case msg := <-d.bus.InboundChannel():
		if msg.Content != "/help" || msg.Metadata["is_command"] != true {
			t.Errorf("/help reached the agent as %+v", msg)
		}
	default:
		t.Fatal("expected /help on the bus")
	}
	if msgs := fake.messages(); len(msgs) != 0 {
		t.Fatalf("a local help text would drift from the agent's: %v", msgs)
	}
}

// Every command the agent answers is forwarded from Discord too — /status,
// /model and the rest used to be "unknown" here while the CLI and Telegram
// accepted them.
func TestDiscordChannel_ForwardsEveryAgentCommand(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})
	for _, c := range discordCommands {
		d.dispatch("111", "alice", "Alice", "chan1", "/"+c.Name+" arg")
		select {
		case msg := <-d.bus.InboundChannel():
			if msg.Content != "/"+c.Name+" arg" || msg.Metadata["is_command"] != true {
				t.Errorf("/%s reached the agent as %+v", c.Name, msg)
			}
		default:
			t.Errorf("/%s did not reach the bus", c.Name)
		}
	}
	if msgs := fake.messages(); len(msgs) != 0 {
		t.Errorf("forwarded commands must not be answered locally: %v", msgs)
	}
}

func TestDiscordChannel_NewCommandReachesBus(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})
	d.dispatch("111", "alice", "Alice", "chan1", "/new")

	select {
	case msg := <-d.bus.InboundChannel():
		if msg.Content != "/new" {
			t.Errorf("expected /new content, got %q", msg.Content)
		}
		if ic, _ := msg.Metadata["is_command"].(bool); !ic {
			t.Error("expected is_command true")
		}
	default:
		t.Fatal("expected /new on the bus")
	}
	// No local acknowledgement: the agent's "Started a new conversation"
	// reply is the acknowledgement, and a local one made /new answer twice.
	if msgs := fake.messages(); len(msgs) != 0 {
		t.Fatalf("/new must not be acknowledged locally, got %v", msgs)
	}
}

func TestDiscordChannel_SendSplitsLongMessages(t *testing.T) {
	d, fake := newTestDiscordChannel(t, nil)
	long := strings.Repeat("a", DiscordMaxMessageLen*2+10)
	if err := d.Send(bus.OutboundMessage{Channel: "discord", ChannelID: "c1", Content: long}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	msgs := fake.messages()
	if len(msgs) < 2 {
		t.Fatalf("expected message to be split, got %d parts", len(msgs))
	}
	for i, m := range msgs {
		if len(m) > DiscordMaxMessageLen {
			t.Errorf("part %d exceeds Discord limit: %d bytes", i, len(m))
		}
	}
}

func TestDiscordChannel_SendRetriesTransient(t *testing.T) {
	d, fake := newTestDiscordChannel(t, nil)
	d.retryDelay = time.Millisecond
	fake.failFirst = 2
	fake.failErr = &net503{}

	if err := d.Send(bus.OutboundMessage{Channel: "discord", ChannelID: "c1", Content: "hi"}); err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if msgs := fake.messages(); len(msgs) != 1 || msgs[0] != "hi" {
		t.Fatalf("expected one successful send, got %v", msgs)
	}
}

func TestDiscordChannel_SendNoRecipient(t *testing.T) {
	d, _ := newTestDiscordChannel(t, nil)
	if err := d.Send(bus.OutboundMessage{Channel: "discord", Content: "hi"}); err == nil {
		t.Fatal("expected error when no recipient specified")
	}
}

func TestDiscordChannel_SendUsesMetadataChatID(t *testing.T) {
	d, fake := newTestDiscordChannel(t, nil)
	msg := bus.OutboundMessage{
		Channel:  "discord",
		Content:  "routed",
		Metadata: map[string]any{"chat_id": "meta-chan"},
	}
	if err := d.Send(msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if msgs := fake.messages(); len(msgs) != 1 {
		t.Fatalf("expected send via metadata chat_id, got %v", msgs)
	}
}

// net503 is a retryable-looking error (matches isRetryable's "connection").
type net503 struct{}

func (net503) Error() string { return "connection reset by peer" }

// TestDiscordChannel_IsAllowed_NameCannotSpoofID pins the partitioned
// allowlist. A Discord global display name is free-form and unvalidated, so a
// stranger can set theirs to the operator's snowflake; when every entry was
// matched against every field that was a full authentication bypass into an
// agent loop holding the shell tool.
func TestDiscordChannel_IsAllowed_NameCannotSpoofID(t *testing.T) {
	d, _ := newTestDiscordChannel(t, []string{"123456"})

	if d.IsAllowed("999", "attacker", "123456") {
		t.Error("global name matching an ID-shaped allowlist entry must not authenticate")
	}
	if d.IsAllowed("999", "123456", "Attacker") {
		t.Error("username matching an ID-shaped allowlist entry must not authenticate")
	}
	if !d.IsAllowed("123456", "attacker", "Attacker") {
		t.Error("the real user ID must still match")
	}
}

// TestDiscordChannel_IsAllowed_NameEntryDoesNotMatchID is the mirror case: a
// name entry may only ever satisfy a name field.
func TestDiscordChannel_IsAllowed_NameEntryDoesNotMatchID(t *testing.T) {
	d, _ := newTestDiscordChannel(t, []string{"alice"})

	if d.IsAllowed("alice", "bob", "Bob") {
		t.Error("a name entry must not be matched against the user ID field")
	}
	if !d.IsAllowed("999", "Alice", "Someone") {
		t.Error("expected case-insensitive username match")
	}
}

// discordRESTError builds a discordgo REST error with the given API code and
// HTTP status, matching the shape ChannelMessageSend returns.
func discordRESTError(code, status int) error {
	return &discordgo.RESTError{
		Response: &http.Response{StatusCode: status},
		Message:  &discordgo.APIErrorMessage{Code: code, Message: "denied"},
	}
}

// TestDiscordChannel_SendDoesNotRetryPermanent pins the Discord-specific
// classifier. Discord's permanent failures are REST error codes, which
// Telegram's string-matching classifier treats as retryable — burning the full
// backoff inside the single consumeOutbound goroutine for a send that can never
// succeed.
func TestDiscordChannel_SendDoesNotRetryPermanent(t *testing.T) {
	cases := []struct {
		name string
		code int
	}{
		{"cannot send to user", 50007},
		{"missing access", 50001},
		{"unknown channel", 10003},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, fake := newTestDiscordChannel(t, nil)
			d.retryDelay = time.Hour // any retry would hang the test
			fake.failFirst = 10
			fake.failErr = discordRESTError(tc.code, 403)

			done := make(chan error, 1)
			go func() {
				done <- d.Send(bus.OutboundMessage{Channel: "discord", ChannelID: "c1", Content: "hi"})
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected send to fail")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Send retried a permanent Discord failure")
			}
		})
	}
}

func TestIsDiscordRetryable(t *testing.T) {
	if isDiscordRetryable(discordRESTError(50007, 403)) {
		t.Error("50007 must be permanent")
	}
	if !isDiscordRetryable(discordRESTError(0, 429)) {
		t.Error("429 rate limit must be retryable")
	}
	if !isDiscordRetryable(discordRESTError(0, 500)) {
		t.Error("5xx must be retryable")
	}
	if !isDiscordRetryable(net503{}) {
		t.Error("connection errors must be retryable")
	}
	if isDiscordRetryable(nil) {
		t.Error("nil error is not retryable")
	}
}

// TestDiscordChannel_RestartReallocatesStopChannel is the regression test for a
// channel that could not be restarted: Stop closed stopCh and Start never
// replaced it, so the second run's consumeOutbound returned immediately and
// every Send aborted its retries — and the next Stop panicked on a double
// close. Start itself needs a live gateway, so the test drives beginRun, the
// method Start delegates the whole running/stop-channel handshake to; the
// reallocation lives there and nowhere else, so removing it turns this red.
func TestDiscordChannel_RestartReallocatesStopChannel(t *testing.T) {
	d, _ := newTestDiscordChannel(t, nil)

	if err := d.beginRun(); err != nil {
		t.Fatalf("first beginRun: %v", err)
	}
	first := d.stopChan()

	// A second beginRun while running must be refused, not silently reset the
	// stop channel out from under the live run.
	if err := d.beginRun(); err == nil {
		t.Fatal("beginRun while running should have failed")
	}
	if d.stopChan() != first {
		t.Fatal("a refused beginRun replaced the live run's stop channel")
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-first:
	default:
		t.Fatal("Stop did not close the stop channel")
	}

	// The second run must hand out a fresh, open channel.
	if err := d.beginRun(); err != nil {
		t.Fatalf("second beginRun: %v", err)
	}
	second := d.stopChan()
	if second == first {
		t.Fatal("restart reused the closed stop channel")
	}
	d.mu.Lock()
	closed := d.stopClosed
	d.mu.Unlock()
	if closed {
		t.Fatal("restart left stopClosed set; the next Stop would skip closing")
	}
	select {
	case <-second:
		t.Fatal("restarted channel started already stopped")
	default:
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	select {
	case <-second:
	default:
		t.Fatal("second Stop did not close the restarted stop channel")
	}
	// A third Stop must be a no-op, not a close-of-closed-channel panic.
	if err := d.Stop(); err != nil {
		t.Fatalf("third Stop: %v", err)
	}
}

// TestDiscordChannel_StopIsIdempotent pins that a double Stop after a real
// close cannot panic even if running were somehow still set.
func TestDiscordChannel_StopIsIdempotent(t *testing.T) {
	d, _ := newTestDiscordChannel(t, nil)
	d.mu.Lock()
	d.running = true
	d.mu.Unlock()
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	d.mu.Lock()
	d.running = true // force the second Stop past the running latch
	d.mu.Unlock()
	if err := d.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestDiscordChannel_HandleMessageCreate covers the self/bot reply-loop guard,
// which is the only thing standing between joshbot and an infinite
// conversation with itself.
func TestDiscordChannel_HandleMessageCreate(t *testing.T) {
	cases := []struct {
		name      string
		author    *discordgo.User
		wantOnBus bool
	}{
		{name: "nil author", author: nil},
		{name: "bot author", author: &discordgo.User{ID: "999", Username: "someBot", Bot: true}},
		{name: "self author", author: &discordgo.User{ID: "self-id", Username: "joshbot"}},
		{name: "allowed human", author: &discordgo.User{ID: "111", Username: "alice"}, wantOnBus: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDiscordChannel(t, []string{"111", "999", "self-id"})
			d.mu.Lock()
			d.selfID = "self-id"
			d.mu.Unlock()

			m := &discordgo.MessageCreate{Message: &discordgo.Message{
				ChannelID: "c1",
				Content:   "hello",
				Author:    tc.author,
			}}
			d.handleMessageCreate(nil, m)

			select {
			case msg := <-d.bus.InboundChannel():
				if !tc.wantOnBus {
					t.Fatalf("message reached the bus but should have been ignored: %+v", msg)
				}
				if !strings.Contains(msg.SenderID, "111") {
					t.Errorf("SenderID = %q, want it to identify user 111", msg.SenderID)
				}
			case <-time.After(150 * time.Millisecond):
				if tc.wantOnBus {
					t.Fatal("expected message on the bus")
				}
			}
		})
	}
}
