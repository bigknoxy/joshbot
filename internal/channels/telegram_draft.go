package channels

import (
	"strconv"
	"sync/atomic"
	"unicode/utf8"

	"github.com/bigknoxy/joshbot/internal/log"
	"gopkg.in/telebot.v3"
)

// telegramRawCaller is the slice of *telebot.Bot the draft path needs.
// telebot v3.3.8 predates sendMessageDraft and exposes no typed wrapper for
// it, so the call goes out as a raw Bot API request. Bot.Raw already turns an
// {"ok":false} body into an error, which is what makes the self-disable in
// sendDraftLocked reliable rather than a silent no-op.
type telegramRawCaller interface {
	Raw(method string, payload interface{}) ([]byte, error)
}

// sendMessageDraftMethod streams a partial message to a private chat while it
// is being generated. Per the Bot API reference the draft is ephemeral — a
// temporary 30-second preview — and the finished text must still be sent with
// an ordinary sendMessage to persist it in the chat. That is exactly why the
// draft path never sets s.delivered: nothing a draft shows is in the chat, so
// Finish's "the whole answer reached the chat" contract is unaffected by it.
const sendMessageDraftMethod = "sendMessageDraft"

// draftSeq numbers drafts. draft_id must be non-zero and changes to drafts
// sharing an id are animated by the client, so one id per turn gives the
// growing answer a single animated slot; atomic.Int64.Add starts at 1, so the
// zero id is never handed out.
var draftSeq atomic.Int64

func nextDraftID() int64 { return draftSeq.Add(1) }

// privateChatID reports the numeric chat id when the recipient is a private
// chat. sendMessageDraft documents chat_id as "the target private chat" only,
// so a group or channel must never take this path — it would spend a request
// per delta to earn an error per delta.
func privateChatID(recipient telebot.Recipient) (int64, bool) {
	key, ok := recipientKey(recipient)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// currentRawCaller returns the raw Bot API caller, or nil when there is none.
func (t *TelegramChannel) currentRawCaller() telegramRawCaller {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.rawCaller != nil {
		return t.rawCaller
	}
	if t.bot != nil {
		return t.bot
	}
	return nil
}

// streamDraftsEnabled reports channels.telegram.stream_drafts.
func (t *TelegramChannel) streamDraftsEnabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cfg != nil && t.cfg.StreamDrafts
}

// Thinking shows the native "Thinking…" placeholder — an empty-text draft —
// before any output exists, so a phone shows the bot working the moment the
// turn starts rather than after the first token. It is a no-op when drafts are
// off, and deliberately does nothing once text has arrived.
func (s *TelegramStreamer) Thinking() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.draftsOK || s.buf != "" {
		return
	}
	s.sendDraftLocked("")
}

// sendDraftLocked pushes text into the turn's draft slot, disabling drafts for
// the turn on any error. Callers must hold s.mu.
func (s *TelegramStreamer) sendDraftLocked(text string) {
	if !s.draftsOK {
		return
	}
	if s.draftSent && text == s.draftShown {
		return
	}
	caller := s.ch.currentRawCaller()
	if caller == nil {
		s.draftsOK = false
		return
	}
	payload := map[string]interface{}{
		"chat_id":  s.draftChatID,
		"draft_id": s.draftID,
		"text":     draftTail(text),
	}
	if _, err := caller.Raw(sendMessageDraftMethod, payload); err != nil {
		// A Bot API server older than the method answers "method not found",
		// and an empty text is only accepted from Bot API 10.0 onward. Either
		// way the edit loop is a working fallback, so this turn falls back to
		// it rather than failing the reply — the whole point of streaming is
		// presentation, and losing the answer to it would be a poor trade.
		s.draftsOK = false
		log.Debug("telegram message drafts disabled for this turn", "error", err)
		return
	}
	s.draftSent = true
	s.draftShown = text
	s.nextEdit = s.now().Add(s.ch.streamInterval())
}

// draftTail trims text to the tail Telegram will accept. The draft caps at
// 4096 characters and the tail is the interesting end of a growing answer, so
// the head is dropped — and the cut is moved forward to a rune boundary,
// because a byte slice through a multi-byte rune is invalid UTF-8 and Telegram
// rejects the whole request for it.
func draftTail(text string) string {
	if len(text) <= TelegramMaxMessageLen {
		return text
	}
	tail := text[len(text)-TelegramMaxMessageLen:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return tail
}
