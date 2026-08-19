package channels

import (
	"strconv"

	telebot "gopkg.in/telebot.v3"

	"github.com/bigknoxy/joshbot/internal/log"
)

// Reactions-as-acknowledgement (issue #314). Telegram's setMessageReaction
// (Bot API 7.0) puts an emoji on the *user's own* message, so it costs no
// message slot, works in groups, and gives a "it heard me" signal before the
// first token of the reply exists.
//
// Two rules live here. The emoji must be in Telegram's free (non-premium)
// reaction set — ✅ is not, which is why completion is 👍 and not a checkmark;
// a premium-only emoji is rejected by the API with REACTION_INVALID and the
// ack silently never appears. And setMessageReaction *sets* rather than
// appends, so writing 👍 replaces 👀 with no clearing call.
const (
	// ackAdmittedEmoji goes on as soon as the turn is accepted by the bus.
	ackAdmittedEmoji = "👀"
	// ackDoneEmoji replaces it when the reply is on its way.
	ackDoneEmoji = "👍"
)

// ackTarget names the message a reaction lands on. telebot.Bot.React reads
// only the id half of MessageSig — the chat comes from the recipient — and a
// bare &telebot.Message{ID: n} would nil-dereference its Chat there, so the
// target is its own tiny Editable rather than a half-built Message.
type ackTarget int

func (a ackTarget) MessageSig() (string, int64) {
	return strconv.Itoa(int(a)), 0
}

// reactionsEnabled reports whether channels.telegram.reactions is on. It is
// opt-in: a bot in a group without the reaction permission would otherwise log
// a failure on every single turn.
func (t *TelegramChannel) reactionsEnabled() bool {
	return t.cfg != nil && t.cfg.Reactions
}

// react sets emoji on messageID in the chat identified by recipient. It is
// best effort: a reaction is an ornament on the turn, never part of it, so a
// failure is logged at debug and the turn continues.
func (t *TelegramChannel) react(recipient telebot.Recipient, messageID int, emoji string) {
	if !t.reactionsEnabled() || messageID == 0 {
		return
	}
	key, ok := recipientKey(recipient)
	if !ok {
		return
	}

	t.mu.RLock()
	notifier := t.currentNotifierLocked()
	t.mu.RUnlock()
	if notifier == nil {
		return
	}

	target := ackTarget(messageID)
	go func() {
		err := notifier.React(recipient, target, telebot.ReactionOptions{
			Reactions: []telebot.Reaction{{Type: "emoji", Emoji: emoji}},
		})
		if err != nil {
			log.Debug("reaction ack failed", "chat", key, "emoji", emoji, "error", err)
		}
	}()
}

// rememberAck records the inbound message a later reply should mark done.
func (t *TelegramChannel) rememberAck(recipient telebot.Recipient, messageID int) {
	if !t.reactionsEnabled() || messageID == 0 {
		return
	}
	key, ok := recipientKey(recipient)
	if !ok {
		return
	}
	t.mu.Lock()
	if t.ackPending == nil {
		t.ackPending = make(map[string]int)
	}
	t.ackPending[key] = messageID
	t.mu.Unlock()
}

// takeAck removes and returns the pending inbound message id for a chat.
func (t *TelegramChannel) takeAck(recipient telebot.Recipient) int {
	if !t.reactionsEnabled() {
		return 0
	}
	key, ok := recipientKey(recipient)
	if !ok {
		return 0
	}
	t.mu.Lock()
	id := t.ackPending[key]
	delete(t.ackPending, key)
	t.mu.Unlock()
	return id
}

// ackAdmitted marks an inbound message as heard and remembers it for the
// completion ack.
func (t *TelegramChannel) ackAdmitted(recipient telebot.Recipient, messageID int) {
	t.rememberAck(recipient, messageID)
	t.react(recipient, messageID, ackAdmittedEmoji)
}

// ackDone replaces the admitted ack with the completion one, if this chat has
// an inbound message still waiting on a reply.
func (t *TelegramChannel) ackDone(recipient telebot.Recipient) {
	if id := t.takeAck(recipient); id != 0 {
		t.react(recipient, id, ackDoneEmoji)
	}
}
