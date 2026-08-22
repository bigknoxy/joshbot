package channels

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"

	"gopkg.in/telebot.v3"
)

// StopCallbackNamespace routes the [⏹ Stop] button. stopAction is its one
// action; the payload is the turn token Arm issued.
const (
	StopCallbackNamespace = "stop"
	stopAction            = "turn"
	stopButtonText        = "⏹ Stop"
)

// StopCoordinator cancels an in-flight turn from an inline button (#310).
//
// The routing is the whole feature: a press runs on the Telegram poller's
// goroutine and calls the turn's CancelFunc directly. It never publishes to
// the bus and never touches the per-key session lock, because a Stop that
// queues behind the turn it is cancelling — behind LockSession, or behind a
// bus dispatch slot — does nothing at all until that turn ends on its own.
//
// Turns are keyed by an opaque per-turn token rather than the session key:
// the token is what rides callback_data, so the button carries no sender or
// chat id, and a token is valid only while its turn runs (Release removes it)
// and only from the chat it was issued in.
type StopCoordinator struct {
	mu    sync.Mutex
	turns map[string]*stopEntry
}

type stopEntry struct {
	chatID  int64
	cancel  context.CancelFunc
	pressed atomic.Bool
}

var stopTokenSeq atomic.Int64

// NewStopCoordinator registers the stop namespace on the channel. Call
// before Start.
func (t *TelegramChannel) NewStopCoordinator() (*StopCoordinator, error) {
	c := &StopCoordinator{turns: make(map[string]*stopEntry)}
	if err := t.RegisterCallback(StopCallbackNamespace, c.handlePress); err != nil {
		return nil, err
	}
	return c, nil
}

// Arm registers a running turn's cancel func for the chat it answers in and
// returns the token its Stop button carries, a func that reports whether
// the button was pressed, and a release that must be called when the turn
// settles — an entry outliving its turn is a leak keyed by an unbounded
// sequence.
func (c *StopCoordinator) Arm(chatID int64, cancel context.CancelFunc) (token string, pressed func() bool, release func()) {
	e := &stopEntry{chatID: chatID, cancel: cancel}
	token = strconv.FormatInt(stopTokenSeq.Add(1), 36)
	c.mu.Lock()
	c.turns[token] = e
	c.mu.Unlock()
	return token, e.pressed.Load, func() {
		c.mu.Lock()
		delete(c.turns, token)
		c.mu.Unlock()
	}
}

// Markup is the one-button keyboard for a turn token, nil if the token
// cannot be encoded (it is a short base-36 counter, so it always can).
func (c *StopCoordinator) Markup(token string) *telebot.ReplyMarkup {
	kb := (&Keyboard{}).Row(ActionButton(stopButtonText, StopCallbackNamespace, stopAction, token))
	rm, err := kb.Build()
	if err != nil {
		return nil
	}
	return rm
}

// handlePress cancels the turn the token names, if it is still running and
// the press came from the chat it was issued in. A stale or foreign press is
// ignored: the button is answered (by the router) and nothing else happens.
func (c *StopCoordinator) handlePress(_ context.Context, press CallbackPress) error {
	if press.Action.Action != stopAction {
		return nil
	}
	c.mu.Lock()
	e, ok := c.turns[press.Action.Payload]
	c.mu.Unlock()
	if !ok || e.chatID != press.ChatID {
		return nil
	}
	e.pressed.Store(true)
	e.cancel()
	return nil
}
