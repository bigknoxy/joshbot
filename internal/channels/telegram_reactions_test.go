package channels

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"gopkg.in/telebot.v3"
)

// waitForReactions polls the fake for want entries. The reaction call is fired
// from a goroutine on purpose — it must never sit in front of the reply — so
// the assertion has to wait rather than read once.
var errReactionRefused = errors.New("Bad Request: REACTION_INVALID")

func waitForReactions(t *testing.T, fake *fakeNotifier, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := fake.reactionList()
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func reactionTestChannel(t *testing.T, enabled bool) (*TelegramChannel, *fakeTelegramServer, *fakeNotifier) {
	t.Helper()
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg := newTestTelegramChannel("42")
	fake := &fakeNotifier{}
	tg.mu.Lock()
	tg.bot = bot
	tg.notifier = fake
	tg.cfg.Reactions = enabled
	tg.mu.Unlock()
	return tg, srv, fake
}

func inboundCtx(bot *telebot.Bot, id int) telebot.Context {
	return bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID:     id,
		Text:   "hello",
		Chat:   &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 42, Username: "josh"},
	}})
}

// The whole point of the ack is that it lands the moment the turn is admitted,
// before any token of the reply exists. Pin the full path: handleMessage puts
// 👀 on the user's OWN message id, and the later reply replaces it with 👍 on
// that same id — not on the bot's reply, which is what a naive implementation
// reaching for the outbound message would do.
func TestTelegramChannel_ReactionAckMarksAdmitThenCompletion(t *testing.T) {
	tg, _, fake := reactionTestChannel(t, true)

	if err := tg.handleMessage(inboundCtx(tg.bot, 7)); err != nil {
		t.Fatalf("handleMessage returned %v", err)
	}
	select {
	case <-tg.bus.InboundChannel():
	case <-time.After(time.Second):
		t.Fatal("message never reached the bus")
	}

	got := waitForReactions(t, fake, 1)
	if len(got) != 1 || got[0] != "1:7:"+ackAdmittedEmoji {
		t.Fatalf("expected the admitted ack on the inbound message, got %v", got)
	}

	if err := tg.Send(bus.OutboundMessage{ChannelID: "1", Content: "hi"}); err != nil {
		t.Fatalf("Send returned %v", err)
	}
	got = waitForReactions(t, fake, 2)
	if len(got) != 2 || got[1] != "1:7:"+ackDoneEmoji {
		t.Fatalf("expected the completion ack on the same inbound message, got %v", got)
	}
}

// The ack is opt-in. A bot without the reaction permission in a group would
// otherwise log a failure on every turn, so the zero-value config must produce
// no reaction calls at all — not a call that happens to fail.
func TestTelegramChannel_ReactionAckOffByDefault(t *testing.T) {
	tg, _, fake := reactionTestChannel(t, false)

	if err := tg.handleMessage(inboundCtx(tg.bot, 7)); err != nil {
		t.Fatalf("handleMessage returned %v", err)
	}
	select {
	case <-tg.bus.InboundChannel():
	case <-time.After(time.Second):
		t.Fatal("message never reached the bus")
	}
	if err := tg.Send(bus.OutboundMessage{ChannelID: "1", Content: "hi"}); err != nil {
		t.Fatalf("Send returned %v", err)
	}

	if got := waitForReactions(t, fake, 1); len(got) != 0 {
		t.Fatalf("reactions fired with channels.telegram.reactions unset: %v", got)
	}
}

// A rejected sender must not be acknowledged: 👀 on a message that was refused
// tells the user the bot is working on it.
func TestTelegramChannel_ReactionAckSkipsUnauthorized(t *testing.T) {
	tg, _, fake := reactionTestChannel(t, true)

	ctx := tg.bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID:     9,
		Text:   "hello",
		Chat:   &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 999, Username: "mallory"},
	}})
	if err := tg.handleMessage(ctx); err != nil {
		t.Fatalf("handleMessage returned %v", err)
	}

	if got := waitForReactions(t, fake, 1); len(got) != 0 {
		t.Fatalf("an unauthorized message was acknowledged: %v", got)
	}
}

// A reaction is an ornament on the turn, never part of it. A failing
// setMessageReaction — no permission, a chat that forbids reactions — must not
// fail the message or the reply.
func TestTelegramChannel_ReactionFailureDoesNotFailTheTurn(t *testing.T) {
	tg, srv, fake := reactionTestChannel(t, true)
	fake.mu.Lock()
	fake.reactErr = errReactionRefused
	fake.mu.Unlock()

	if err := tg.handleMessage(inboundCtx(tg.bot, 7)); err != nil {
		t.Fatalf("handleMessage returned %v after a reaction failure", err)
	}
	select {
	case <-tg.bus.InboundChannel():
	case <-time.After(time.Second):
		t.Fatal("message never reached the bus")
	}
	if err := tg.Send(bus.OutboundMessage{ChannelID: "1", Content: "hi"}); err != nil {
		t.Fatalf("Send returned %v after a reaction failure", err)
	}
	if texts := srv.texts(); len(texts) != 1 || texts[0] != "hi" {
		t.Fatalf("the reply did not go out: %v", texts)
	}
}

// ✅ is premium-only in Telegram's reaction set and is rejected with
// REACTION_INVALID, which surfaces as an ack that silently never appears. Pin
// both chosen emoji against the free set so a "nicer" emoji cannot be
// substituted without this failing.
func TestReactionEmojiAreInTheFreeSet(t *testing.T) {
	// Telegram's free reaction list, from the Bot API 7.0 documentation.
	free := "👍 👎 ❤ 🔥 🥰 👏 😁 🤔 🤯 😱 🤬 😢 🎉 🤩 🤮 💩 🙏 👌 🕊 🤡 🥱 🥴 😍 🐳 ❤‍🔥 🌚 🌭 💯 🤣 ⚡ 🍌 🏆 💔 🤨 😐 🍓 🍾 💋 🖕 😈 😴 😭 🤓 👻 👨‍💻 👀 🎃 🙈 😇 😨 🤝 ✍ 🤗 🫡 🎅 🎄 ☃ 💅 🤪 🗿 🆒 💘 🙉 🦄 😘 💊 🙊 😎 👾 🤷‍♂ 🤷 🤷‍♀ 😡"
	for _, emoji := range []string{ackAdmittedEmoji, ackDoneEmoji} {
		if !strings.Contains(free, emoji) {
			t.Fatalf("%q is not in Telegram's free reaction set; it will be rejected with REACTION_INVALID", emoji)
		}
	}
}
