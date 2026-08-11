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
	return &TelegramStreamer{
		ch:        t,
		recipient: recipient,
		parseMode: telebot.ModeDefault,
		now:       time.Now,
		sleep:     time.Sleep,
	}
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
	s.flushLocked(false)
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
	return s.delivered && !s.broken
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
		parts := splitMessage(s.buf, TelegramMaxMessageLen)
		if len(parts) == 1 {
			break
		}
		head, rest := parts[0], remainderAfter(s.buf, parts[0])
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

// remainderAfter returns what is left of buf once head has been sent as its own
// message. The tail is carried as raw, unsplit text on purpose: re-joining
// splitMessage's later parts would keep the fences it added for each of them,
// and the next flush would split that again — every rollover compounding a
// duplicated ``` until the message rendered as garbage.
func remainderAfter(buf, head string) string {
	const (
		fenceClose = "\n```"
		fenceOpen  = "```\n"
	)
	consumed := strings.TrimSuffix(head, fenceClose)
	rest := buf[len(consumed):]
	if consumed != head {
		// The head closed a fence this text is still inside, so reopen it.
		rest = fenceOpen + rest
	}
	return rest
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
	opts := &telebot.SendOptions{ParseMode: mode}

	var err error
	if s.msg == nil {
		var sent *telebot.Message
		sent, err = editor.Send(s.recipient, s.buf, opts)
		if err == nil {
			s.msg = sent
		}
	} else {
		_, err = editor.Edit(s.msg, s.buf, opts)
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
			}
		} else {
			_, err = editor.Edit(s.msg, s.buf, opts)
		}
	}

	switch {
	case err == nil, isNotModifiedError(err):
		s.shown = s.buf
		s.delivered = true
		s.nextEdit = s.now().Add(s.ch.streamInterval())
		return true

	case isFloodError(err):
		// Telegram said exactly how long to wait. Honour it: retrying before
		// then earns another 429 and pushes the wait out further.
		s.nextEdit = s.now().Add(floodRetryAfter(err))
		log.Debug("telegram rate-limited a streaming edit", "retry_after", floodRetryAfter(err))
		return false

	default:
		if s.msg == nil {
			// The placeholder never landed, so nothing is on screen and the
			// caller can still deliver the reply the ordinary way.
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
