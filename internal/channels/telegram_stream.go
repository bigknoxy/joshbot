package channels

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/log"
	"gopkg.in/telebot.v3"
)

// telegramEditor is the slice of *telebot.Bot the streamer needs: send the
// placeholder message once, then edit it as text arrives. It is an interface
// so the streamer can be tested without a live Telegram connection, the same
// way telegramNotifier covers chat actions.
type telegramEditor interface {
	Send(to telebot.Recipient, what interface{}, opts ...interface{}) (*telebot.Message, error)
	Edit(msg telebot.Editable, what interface{}, opts ...interface{}) (*telebot.Message, error)
	Delete(msg telebot.Editable) error
}

// defaultStreamInterval is the minimum gap between two edits of the same
// message. Telegram allows roughly one message operation per second per chat
// and answers 429 beyond it, and every edit costs the same quota as a send —
// so the throttle is deliberately well above the limit rather than at it.
const defaultStreamInterval = 3 * time.Second

// maxFinalEditWait bounds how long Finish will wait out a rate limit before
// making the last edit.
const maxFinalEditWait = 5 * time.Second

// TelegramStreamer delivers one agent turn to one chat incrementally: the
// first delta sends a message, later deltas edit it, and Finish makes the
// last edit. Everything it needs is on the streamer, never on TelegramChannel
// — the gateway processes chats concurrently, and shared streaming state
// would cross-deliver one conversation's text into another's message, the
// same trap the per-request agent sink exists to avoid.
//
// A streamer is single-turn: create one per Process call, call Finish once.
type TelegramStreamer struct {
	ch        *TelegramChannel
	recipient telebot.Recipient
	// parseMode is applied only to the final edit. Interim edits go out as
	// plain text on purpose: a partial stream routinely splits a `**bold**`
	// or a code fence down the middle, and Telegram rejects the whole edit
	// for it. Formatting a half-written message is not worth losing it.
	parseMode telebot.ParseMode
	// replyTo anchors the initial send to the triggering message; nil sends
	// unthreaded.
	replyTo *telebot.Message
	// now and sleep are overridable so throttle behaviour is testable without
	// real time passing.
	now   func() time.Time
	sleep func(time.Duration)

	mu sync.Mutex
	// buf is the full text of the message currently being written, and shown
	// is what that message currently displays. They differ by exactly the
	// deltas not yet flushed.
	buf   string
	shown string
	msg   *telebot.Message
	// nextEdit is the earliest time the next edit may go out. A 429 pushes it
	// out by the retry_after Telegram asked for, rather than retrying blindly.
	nextEdit  time.Time
	delivered bool
	broken    bool
	// statusShown is the tool-progress line the message currently displays,
	// "" when the message holds (or will hold) reply text. Status text lives
	// in the same message slot as the reply on purpose: the first content
	// delta edits it away in place, so the chat never accumulates a trail of
	// stale progress lines above the answer.
	statusShown string

	// draftsOK reports that this turn may stream through sendMessageDraft
	// instead of the send/edit loop. It is set at construction (private chat,
	// stream_drafts on, a raw caller available) and cleared for good the
	// first time the API refuses a draft, which is what makes the feature
	// safe against a Bot API server that predates the method. draftShown is
	// the text the draft slot last displayed; draftSent distinguishes "never
	// sent" from "sent an empty placeholder", which are different states.
	draftsOK    bool
	draftSent   bool
	draftID     int64
	draftChatID int64
	draftShown  string
}

// NewStreamer returns a streamer for one turn in the chat identified by
// channelID (the numeric Telegram chat id, as carried on the bus), or nil
// when the id is unusable or the channel has no bot to send through.
func (t *TelegramChannel) NewStreamer(channelID string) *TelegramStreamer {
	chatID, err := strconv.ParseInt(channelID, 10, 64)
	if err != nil {
		return nil
	}
	return t.newStreamer(telebot.ChatID(chatID))
}

// newStreamer returns a streamer for one turn in one chat, or nil when the
// channel has no bot to send through or the recipient is unusable. A nil
// return means "no streaming for this turn" and callers fall back to sending
// the reply the ordinary way — streaming is a presentation nicety, and losing
// the whole answer to it would be a poor trade.
func (t *TelegramChannel) newStreamer(recipient telebot.Recipient) *TelegramStreamer {
	if _, ok := recipientKey(recipient); !ok {
		return nil
	}
	t.mu.RLock()
	editor := t.currentEditorLocked()
	t.mu.RUnlock()
	if editor == nil {
		return nil
	}
	s := &TelegramStreamer{
		ch:        t,
		recipient: recipient,
		// Markdown by default: every ordinary reply used to land as plain
		// text, showing literal ``` and ** on the phone, while the
		// parse-entity fallback below protected a path nothing took. The
		// fallback is what makes the default safe — a reply Telegram
		// rejects degrades to plain text, never gets lost.
		parseMode: telebot.ModeMarkdown,
		now:       time.Now,
		sleep:     time.Sleep,
	}
	// Drafts are opt-in and private-chat only. Deciding once here keeps the
	// per-delta path a bool test.
	if chatID, private := privateChatID(recipient); private && t.streamDraftsEnabled() && t.currentRawCaller() != nil {
		s.draftsOK = true
		s.draftChatID = chatID
		s.draftID = nextDraftID()
	}
	return s
}

// SetReplyTo threads the streamed reply to the message that triggered it, so
// an answer arriving after other traffic is anchored to its question. Only
// the initial send can carry it; edits keep the anchor automatically.
func (s *TelegramStreamer) SetReplyTo(messageID int) {
	if s == nil || messageID == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replyTo = &telebot.Message{ID: messageID}
}

// currentEditorLocked returns the editor to stream through. Callers must hold
// mu (read or write).
func (t *TelegramChannel) currentEditorLocked() telegramEditor {
	if t.editor != nil {
		return t.editor
	}
	if t.bot != nil {
		return t.bot
	}
	return nil
}

// editor returns the current editor without assuming a lock is held.
func (t *TelegramChannel) currentEditor() telegramEditor {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.currentEditorLocked()
}

// Delta records an incremental piece of assistant text and flushes it if the
// throttle allows. It is safe to call from the provider's stream goroutine.
func (s *TelegramStreamer) Delta(text string) {
	if s == nil || text == "" {
		return
	}

	// The first delta means the answer has started arriving, so the "typing…"
	// keep-alive has done its job. Doing this outside s.mu keeps the channel's
	// lock and the streamer's lock strictly unnested.
	s.mu.Lock()
	first := s.buf == ""
	s.buf += text
	s.mu.Unlock()
	if first {
		s.ch.stopTyping(s.recipient)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken || s.now().Before(s.nextEdit) {
		return
	}
	if s.draftsOK {
		s.sendDraftLocked(s.buf)
		// A draft that landed carried this delta; the persisting sendMessage
		// still happens in Finish. If the draft was refused, drafts are off
		// for good and the edit loop takes over from this delta on.
		if s.draftsOK {
			return
		}
	}
	s.flushLocked(false)
}

// Status shows a one-line tool-progress note ("⚙️ shell: go test ./...") in
// the chat while the agent works. It is strictly best-effort: it shares the
// edit-rate budget with content flushes and is skipped — never queued — when
// the throttle disallows it, and it stops entirely once reply text has begun
// arriving, because the message then belongs to the answer. Always plain
// text: a tool summary is arbitrary content and Markdown would reject it.
func (s *TelegramStreamer) Status(text string) {
	if s == nil || text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken || s.buf != "" || text == s.statusShown || s.now().Before(s.nextEdit) {
		return
	}
	if s.draftsOK {
		// In draft mode no status *message* is ever created, so there is
		// nothing for Finish to clean up and statusShown stays "".
		s.sendDraftLocked(text)
		if s.draftsOK {
			return
		}
	}
	editor := s.ch.currentEditor()
	if editor == nil {
		return
	}

	if s.msg == nil {
		opts := &telebot.SendOptions{}
		if s.replyTo != nil {
			opts.ReplyTo = s.replyTo
		}
		sent, err := editor.Send(s.recipient, text, opts)
		if err != nil {
			if isFloodError(err) {
				s.nextEdit = s.now().Add(floodRetryAfter(err))
			}
			log.Debug("failed to send tool-progress status", "error", err)
			return
		}
		// The status message claims the turn's message slot and its thread
		// anchor; the first content delta will edit the answer into it.
		// delivered stays false — it means reply text reached the chat, and
		// Finish's suppression contract depends on that.
		s.msg = sent
		s.replyTo = nil
	} else if _, err := editor.Edit(s.msg, text); err != nil {
		if isFloodError(err) {
			s.nextEdit = s.now().Add(floodRetryAfter(err))
		}
		log.Debug("failed to edit tool-progress status", "error", err)
		return
	}
	s.statusShown = text
	s.nextEdit = s.now().Add(s.ch.streamInterval())
}

// clearStatusLocked deletes a status message no reply ever replaced, so a
// turn whose answer arrives through the bus fallback does not leave a stale
// "⚙️ ..." line above it. Callers must hold s.mu.
func (s *TelegramStreamer) clearStatusLocked() {
	if s.statusShown == "" || s.msg == nil || s.delivered {
		return
	}
	editor := s.ch.currentEditor()
	if editor == nil {
		return
	}
	if err := editor.Delete(s.msg); err != nil {
		log.Debug("failed to delete tool-progress status", "error", err)
	}
	s.msg, s.statusShown = nil, ""
}

// Finish makes the final edit and reports whether the turn was delivered to
// Telegram. False means nothing reached the chat (nothing was streamed, or
// the very first send failed) and the caller must send the reply itself.
//
// procErr is the error the agent turn ended with, if any. When text was
// already shown, the error is appended as a visible marker rather than
// discarded: a partial answer that just stops reads as a complete one.
func (s *TelegramStreamer) Finish(procErr error) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.buf == "" || s.broken {
		// The reply is coming through the bus fallback; a leftover status
		// line above it would read as a second, stuck turn.
		s.clearStatusLocked()
		return false
	}
	if procErr != nil {
		s.buf += "\n\n⚠️ the response was cut short: " + procErr.Error()
	}
	// The final edit is the one that matters: it is the difference between a
	// chat holding the whole answer and a chat holding whatever happened to
	// be flushed last. If a 429 pushed the next edit into the future, wait
	// for it — bounded, so a hostile retry_after cannot pin a goroutine.
	if wait := s.nextEdit.Sub(s.now()); wait > 0 {
		if wait > maxFinalEditWait {
			wait = maxFinalEditWait
		}
		s.sleep(wait)
	}
	s.flushLocked(true)
	// A final flush that failed with nothing ever delivered leaves the reply
	// to the bus fallback — clear any status message here too, for the same
	// reason as the empty-buffer branch above. clearStatusLocked's own guards
	// make this a no-op once reply text has landed.
	s.clearStatusLocked()
	// The return value is a delivery contract with the gateway: true suppresses
	// the bus publish, so it may only mean "the whole answer is in the chat".
	// Reporting delivery because *something* landed is how a single failed final
	// edit — a user deleting the in-progress message, one transient 400 — used
	// to destroy the entire answer, with the fallback that exists to prevent
	// exactly that suppressed by the same bug. A partial stream plus the bus
	// copy is duplication; a suppressed publish after a failed final edit is
	// loss, and loss is the worse of the two.
	return s.delivered && !s.broken && s.shown == s.buf
}

// SetParseMode selects the formatting applied to the final edit. Interim
// edits are always plain text — see the parseMode field.
func (s *TelegramStreamer) SetParseMode(mode telebot.ParseMode) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parseMode = mode
}

// Delivered reports whether any text has reached the chat.
func (s *TelegramStreamer) Delivered() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered && !s.broken
}

// flushLocked writes the pending text. Callers must hold s.mu.
func (s *TelegramStreamer) flushLocked(final bool) {
	editor := s.ch.currentEditor()
	if editor == nil {
		s.broken = true
		return
	}

	// Roll over at the hard 4096-byte limit. splitMessage does it
	// code-fence-aware, so a message that ends mid-fence is closed and the
	// next one reopens it.
	for {
		if len(s.buf) <= TelegramMaxMessageLen {
			break
		}
		head, rest := splitOnce(s.buf, TelegramMaxMessageLen)
		// The buffer is only advanced once the head has actually landed:
		// truncating it first and then failing the write would drop the tail
		// on the floor, which is the one outcome streaming must never have.
		pending := s.buf
		s.buf = head
		// The part being closed off is final for its own message, whatever
		// the caller asked for: nothing more will ever be added to it.
		if !s.writeLocked(editor, true) {
			s.buf = pending
			return
		}
		s.msg, s.shown = nil, ""
		s.buf = rest
	}

	s.writeLocked(editor, final)
}

// writeLocked sends or edits the current message so it displays s.buf, and
// reports whether it may continue. Callers must hold s.mu.
func (s *TelegramStreamer) writeLocked(editor telegramEditor, final bool) bool {
	// Telegram answers an unchanged edit with an error, not a silent success,
	// so a no-op edit is worse than useless — it burns rate limit to produce
	// a log line. The final write is exempt only when it changes formatting.
	if s.buf == s.shown && !(final && s.parseMode != telebot.ModeDefault) {
		return true
	}

	mode := telebot.ModeDefault
	if final {
		mode = s.parseMode
	}
	// The final edit asks for Markdown and sends HTML: the legacy Markdown
	// parser rejects ordinary prose with a 400, and the recovery below then
	// strips every bit of formatting off the whole answer. s.buf and s.shown
	// stay the Markdown source, so Finish's shown == buf suppression rule is
	// unaffected -- only the bytes on the wire change.
	payload := s.buf
	if mode == telebot.ModeMarkdown {
		payload = MarkdownToHTML(payload)
		mode = telebot.ModeHTML
	}
	opts := &telebot.SendOptions{ParseMode: mode}
	if s.msg == nil && s.replyTo != nil {
		opts.ReplyTo = s.replyTo
	}

	var err error
	if s.msg == nil {
		var sent *telebot.Message
		sent, err = editor.Send(s.recipient, payload, opts)
		if err == nil {
			s.msg = sent
			// Only the first message of the turn threads to the question; a
			// 4096 rollover clears s.msg and must not re-anchor part two.
			s.replyTo = nil
		}
	} else {
		_, err = editor.Edit(s.msg, payload, opts)
	}

	// A formatting rejection must degrade to plain text rather than lose the
	// text, exactly as Send does. Matching stays narrow (isParseEntityError,
	// never a bare 400) so a real failure like "chat not found" is not
	// quietly downgraded and hidden.
	if err != nil && isParseEntityError(err) && opts.ParseMode != telebot.ModeDefault {
		opts.ParseMode = telebot.ModeDefault
		s.parseMode = telebot.ModeDefault
		if s.msg == nil {
			var sent *telebot.Message
			sent, err = editor.Send(s.recipient, s.buf, opts)
			if err == nil {
				s.msg = sent
				s.replyTo = nil
			}
		} else {
			_, err = editor.Edit(s.msg, s.buf, opts)
		}
	}

	switch {
	case err == nil, isNotModifiedError(err):
		s.shown = s.buf
		s.delivered = true
		// Reply text now owns the message; any status line is gone.
		s.statusShown = ""
		s.nextEdit = s.now().Add(s.ch.streamInterval())
		return true

	case isFloodError(err):
		// Telegram said exactly how long to wait. Honour it: retrying before
		// then earns another 429 and pushes the wait out further.
		s.nextEdit = s.now().Add(floodRetryAfter(err))
		log.Debug("telegram rate-limited a streaming edit", "retry_after", floodRetryAfter(err))
		return false

	default:
		if !s.delivered {
			// Nothing has ever reached the chat, so the caller can still deliver
			// the reply the ordinary way. Gating this on s.msg == nil instead was
			// wrong after a rollover: that clears s.msg while several full
			// messages are already on screen, so a failure there declared the
			// turn undelivered and the bus republished the whole answer on top of
			// them.
			s.broken = true
			log.Warn("telegram streaming disabled for this turn", "error", err)
			return false
		}
		// A message is already on screen. Keep the text pending and try again
		// on the next delta rather than abandoning the turn.
		s.nextEdit = s.now().Add(s.ch.streamInterval())
		log.Warn("failed to edit streaming message", "error", err)
		return false
	}
}

// streamInterval is the configured minimum gap between edits.
func (t *TelegramChannel) streamInterval() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.streamEditInterval > 0 {
		return t.streamEditInterval
	}
	return defaultStreamInterval
}

// isNotModifiedError reports Telegram's answer to an edit that would change
// nothing. It is a 400, but it means the message already says what we wanted
// it to say, so treating it as a failure would break the stream over success.
func isNotModifiedError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

// isFloodError reports a 429. telebot returns a typed FloodError only for
// descriptions it recognises, so the text is checked as well.
func isFloodError(err error) bool {
	var fe telebot.FloodError
	if errors.As(err, &fe) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "too many requests") || strings.Contains(s, "retry after")
}

// retryAfterPattern reads the seconds out of Telegram's flood description.
// telebot only fills FloodError.RetryAfter for descriptions in its static
// table, so the number has to be recoverable from the text as well — and it
// is the whole point of honouring a 429 rather than backing off blindly.
var retryAfterPattern = regexp.MustCompile(`retry after (\d+)`)

// floodRetryAfter extracts the wait Telegram asked for, falling back to the
// ordinary throttle when the error carries no usable number.
func floodRetryAfter(err error) time.Duration {
	var fe telebot.FloodError
	if errors.As(err, &fe) && fe.RetryAfter > 0 {
		return time.Duration(fe.RetryAfter) * time.Second
	}
	if m := retryAfterPattern.FindStringSubmatch(strings.ToLower(err.Error())); m != nil {
		if n, convErr := strconv.Atoi(m[1]); convErr == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultStreamInterval
}
