package channels

import (
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

func TestDiscordChannel_HelpCommand(t *testing.T) {
	d, fake := newTestDiscordChannel(t, []string{"111"})
	d.dispatch("111", "alice", "Alice", "chan1", "/help")
	msgs := fake.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "JoshBot") {
		t.Fatalf("expected help text, got %v", msgs)
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
	// And an acknowledgement was sent.
	if msgs := fake.messages(); len(msgs) != 1 || !strings.Contains(msgs[0], "new session") {
		t.Fatalf("expected new-session ack, got %v", msgs)
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
