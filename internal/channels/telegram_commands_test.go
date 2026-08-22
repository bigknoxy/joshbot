package channels

import (
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	telebot "gopkg.in/telebot.v3"
)

// commandChannel wires a channel to a fake Bot API so ctx.Bot().Send() is real
// but offline, with the typing keep-alive parked.
func commandChannel(t *testing.T, allow ...string) (*TelegramChannel, *fakeTelegramServer, *telebot.Bot) {
	t.Helper()
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg := newTestTelegramChannel(allow...)
	tg.mu.Lock()
	tg.bot = bot
	tg.notifier = &fakeNotifier{}
	tg.typingInterval = time.Hour
	tg.mu.Unlock()
	t.Cleanup(func() { _ = tg.Stop() })
	return tg, srv, bot
}

func textCtx(bot *telebot.Bot, senderID int64, text string) telebot.Context {
	return bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID:     3,
		Chat:   &telebot.Chat{ID: 88},
		Sender: &telebot.User{ID: senderID, Username: "someone", FirstName: "4242", LastName: "Impostor"},
		Text:   text,
	}})
}

// TestHandleMessageForwardsPlainText pins the ordinary path: text reaches the
// agent with the routing metadata the outbound side needs to reply, and the
// typing keep-alive is started so the user sees the bot working.
func TestHandleMessageForwardsPlainText(t *testing.T) {
	tg, _, bot := commandChannel(t, "1234")

	if err := tg.handleMessage(textCtx(bot, 1234, "hello there")); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	select {
	case got := <-tg.bus.InboundChannel():
		if got.Content != "hello there" {
			t.Errorf("Content = %q", got.Content)
		}
		if got.SenderID != "telegram_1234" {
			t.Errorf("SenderID = %q", got.SenderID)
		}
		if got.Metadata["chat_id"] != int64(88) {
			t.Errorf("chat_id = %v, want 88; the reply cannot be routed back without it", got.Metadata["chat_id"])
		}
	case <-time.After(time.Second):
		t.Fatal("the message never reached the agent")
	}

	tg.mu.RLock()
	typing := len(tg.typingStop)
	tg.mu.RUnlock()
	if typing != 1 {
		t.Fatalf("typing keep-alives = %d, want 1", typing)
	}
}

// A known command must be left to its own handler. Answering it here too would
// send the turn to the agent twice.
func TestHandleMessageLeavesKnownCommandsToTheirHandlers(t *testing.T) {
	tg, srv, bot := commandChannel(t, "1234")

	if err := tg.handleMessage(textCtx(bot, 1234, "/new")); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if got := srv.texts(); len(got) != 0 {
		t.Fatalf("a registered command was answered by handleMessage too: %v", got)
	}
	select {
	case got := <-tg.bus.InboundChannel():
		t.Fatalf("a registered command was forwarded twice: %q", got.Content)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestHandleMessageUnknownCommandNeverLeaksTheMenuToStrangers pins both halves
// of the unknown-command fallback: an allowed user is told what exists — from
// botCommands, the same source as the Telegram menu, so the two cannot drift —
// and a stranger gets silence, because the command list discloses what the bot
// is and confirms it is live.
func TestHandleMessageUnknownCommandNeverLeaksTheMenuToStrangers(t *testing.T) {
	tg, srv, bot := commandChannel(t, "1234")

	if err := tg.handleMessage(textCtx(bot, 1234, "/nope")); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	texts := srv.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "Unknown command: /nope") {
		t.Fatalf("an unknown command was swallowed: %v", texts)
	}
	for _, c := range botCommands {
		if !strings.Contains(texts[0], "/"+c.Text) {
			t.Errorf("/%s is in the command menu but missing from the unknown-command listing", c.Text)
		}
	}

	// Sender 9999 is not allowed; its FIRST NAME is the numeric allowlist
	// entry, which is attacker-chosen and must never match.
	if err := tg.handleMessage(textCtx(bot, 9999, "/nope")); err != nil {
		t.Fatalf("handleMessage for a stranger: %v", err)
	}
	if got := srv.texts(); len(got) != 1 {
		t.Fatalf("the command list was disclosed to a disallowed sender: %v", got)
	}
}

// A disallowed sender of ordinary text is told they are not authorized, and
// nothing reaches the agent.
func TestHandleMessageRefusesDisallowedSenders(t *testing.T) {
	tg, srv, bot := commandChannel(t, "4242") // digits; the stranger's first name is "4242"

	if err := tg.handleMessage(textCtx(bot, 9999, "do my bidding")); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	select {
	case got := <-tg.bus.InboundChannel():
		t.Fatalf("a numeric allowlist entry matched an attacker-chosen display name; "+
			"%q from user 9999 reached the agent", got.Content)
	case <-time.After(100 * time.Millisecond):
	}
	texts := srv.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "not authorized") {
		t.Fatalf("a rejected sender was not told why: %v", texts)
	}
	tg.mu.RLock()
	typing := len(tg.typingStop)
	tg.mu.RUnlock()
	if typing != 0 {
		t.Fatal("a refused sender still started a typing keep-alive")
	}
}

// A full bus must be reported: silence looks like the bot is thinking, and the
// user waits forever.
func TestHandleMessageReportsAFullBus(t *testing.T) {
	tg, srv, bot := commandChannel(t, "1234")
	for tg.bus.Send(bus.InboundMessage{Content: "filler", Channel: "telegram"}) {
	}

	if err := tg.handleMessage(textCtx(bot, 1234, "hello")); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	texts := srv.texts()
	if len(texts) != 1 || !strings.Contains(strings.ToLower(texts[0]), "couldn't process") {
		t.Fatalf("a dropped message was not reported to the user: %v", texts)
	}
}

// handleMessage recovers from a panic instead of taking the poller down with
// it — telebot runs handlers on the polling goroutine.
func TestHandleMessagePanicIsContained(t *testing.T) {
	tg, _, bot := commandChannel(t, "1234")
	// A message with no Sender panics at the first dereference.
	ctx := bot.NewContext(telebot.Update{Message: &telebot.Message{ID: 4, Chat: &telebot.Chat{ID: 1}}})

	err := tg.handleMessage(ctx)
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("a panicking handler must be reported, got %v", err)
	}
}

// /new is dispatched by telebot straight to its own handler, so the allowlist
// check inside handleMessage is never reached — it must be repeated here or an
// unallowed caller can still reset a session and drive agent work.
func TestHandleNewGatesOnTheAllowlist(t *testing.T) {
	tg, srv, bot := commandChannel(t, "1234")

	if err := tg.handleCommandForward(textCtx(bot, 1234, "/new"), "/new"); err != nil {
		t.Fatalf("handleNew: %v", err)
	}
	select {
	case got := <-tg.bus.InboundChannel():
		if got.Content != "/new" || got.Metadata["is_command"] != true {
			t.Fatalf("/new reached the agent as %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("/new never reached the agent")
	}
	if got := srv.texts(); len(got) != 0 {
		t.Fatalf("/new must not be acknowledged locally (the agent's reply is the ack): %v", got)
	}

	if err := tg.handleCommandForward(textCtx(bot, 9999, "/new"), "/new"); err != nil {
		t.Fatalf("handleNew for a stranger: %v", err)
	}
	select {
	case got := <-tg.bus.InboundChannel():
		t.Fatalf("a disallowed sender reset a session: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if got := srv.texts(); len(got) != 0 {
		t.Fatalf("a disallowed sender was answered, confirming the bot is live: %v", got)
	}
}

// A dropped /new must be reported: silence reads as a successful reset, and the
// user carries on in a session they believe is fresh.
func TestHandleNewReportsAFullBus(t *testing.T) {
	tg, srv, bot := commandChannel(t, "1234")
	for tg.bus.Send(bus.InboundMessage{Content: "filler", Channel: "telegram"}) {
	}

	if err := tg.handleCommandForward(textCtx(bot, 1234, "/new"), "/new"); err != nil {
		t.Fatalf("handleNew: %v", err)
	}
	got := srv.texts()
	if len(got) != 1 || !strings.Contains(strings.ToLower(got[0]), "sorry") {
		t.Fatalf("a dropped /new was not reported: %v", got)
	}
}

// The forwarded commands (/status, /model, /personality, /compact) disclose bot
// configuration, so each repeats the allowlist check that handleMessage never
// gets to apply to them.
func TestHandleCommandForwardGatesEveryForwardedCommand(t *testing.T) {
	for _, cmd := range forwardedCommands {
		t.Run(cmd, func(t *testing.T) {
			tg, srv, bot := commandChannel(t, "1234")

			if err := tg.handleCommandForward(textCtx(bot, 1234, cmd+" arg"), cmd); err != nil {
				t.Fatalf("handleCommandForward: %v", err)
			}
			select {
			case got := <-tg.bus.InboundChannel():
				if got.Content != cmd+" arg" {
					t.Errorf("the raw command text must reach the agent, got %q", got.Content)
				}
				if got.Metadata["is_command"] != true {
					t.Errorf("%s was not tagged as a command: %+v", cmd, got.Metadata)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s never reached the agent", cmd)
			}

			if err := tg.handleCommandForward(textCtx(bot, 9999, cmd), cmd); err != nil {
				t.Fatalf("handleCommandForward for a stranger: %v", err)
			}
			select {
			case got := <-tg.bus.InboundChannel():
				t.Fatalf("%s from a disallowed sender reached the agent: %+v", cmd, got)
			case <-time.After(100 * time.Millisecond):
			}
			if got := srv.texts(); len(got) != 0 {
				t.Fatalf("a disallowed sender was answered: %v", got)
			}
		})
	}
}

// A dropped command must be reported rather than leaving the user waiting on a
// /status that will never arrive.
func TestHandleCommandForwardReportsAFullBus(t *testing.T) {
	tg, srv, bot := commandChannel(t, "1234")
	for tg.bus.Send(bus.InboundMessage{Content: "filler", Channel: "telegram"}) {
	}

	if err := tg.handleCommandForward(textCtx(bot, 1234, "/status"), "/status"); err != nil {
		t.Fatalf("handleCommandForward: %v", err)
	}
	got := srv.texts()
	if len(got) != 1 || !strings.Contains(strings.ToLower(got[0]), "couldn't process") {
		t.Fatalf("a dropped command was not reported: %v", got)
	}
}
