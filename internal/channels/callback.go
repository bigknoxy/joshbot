package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/telebot.v3"
)

// CallbackDataMaxBytes is the Bot API's hard limit on the callback_data field
// of an inline button. Telegram rejects a longer value outright, so an
// over-long payload has to be a build-time error rather than a truncation:
// truncating produces a button that looks right, reaches the router, and
// decodes to some other action.
const CallbackDataMaxBytes = 64

// callbackSep separates the three fields of an encoded callback. It is a
// character no namespace or action may contain, which is what makes decoding
// unambiguous while still allowing a payload to contain separators of its own.
const callbackSep = ":"

// ErrNotCallbackAction is returned by DecodeCallback for data that is not in
// joshbot's envelope form at all. It is deliberately distinguishable from a
// malformed envelope: unknown data is forwarded to the agent as before, so a
// button from an older joshbot, or from some other producer, still does
// something rather than being dropped.
var ErrNotCallbackAction = errors.New("callback data is not a joshbot action envelope")

// CallbackAction is the structured form of an inline button's callback_data.
//
// Before this existed, TelegramChannel.handleCallback turned every button press
// into the synthetic user message "[Callback: <data>]" and fed it to the ReAct
// loop, which meant a button could only ever do what the model decided the
// string meant. Namespacing the data lets the channel dispatch a press to a
// real handler and answer it without spending an LLM turn, while leaving
// unrecognised data on the old path.
type CallbackAction struct {
	// Namespace identifies the feature that owns the button ("approve",
	// "model", "heartbeat"). It selects the handler.
	Namespace string
	// Action is the verb within that namespace ("allow", "deny", "set").
	Action string
	// Payload is opaque to the router and may be empty. It may contain the
	// separator; only the first two fields are split.
	Payload string
}

// Encode renders the action as callback_data.
//
// It returns an error rather than a best-effort string for every way the
// envelope can be invalid, because each of them produces a button that
// silently misbehaves at press time rather than at build time.
func (a CallbackAction) Encode() (string, error) {
	if a.Namespace == "" {
		return "", errors.New("callback namespace is empty")
	}
	if a.Action == "" {
		return "", errors.New("callback action is empty")
	}
	if strings.Contains(a.Namespace, callbackSep) {
		return "", fmt.Errorf("callback namespace %q contains %q", a.Namespace, callbackSep)
	}
	if strings.Contains(a.Action, callbackSep) {
		return "", fmt.Errorf("callback action %q contains %q", a.Action, callbackSep)
	}
	encoded := a.Namespace + callbackSep + a.Action + callbackSep + a.Payload
	if len(encoded) > CallbackDataMaxBytes {
		return "", fmt.Errorf(
			"callback data is %d bytes, over Telegram's limit of %d: %q",
			len(encoded), CallbackDataMaxBytes, encoded)
	}
	return encoded, nil
}

// DecodeCallback parses callback_data produced by CallbackAction.Encode.
//
// Data that does not carry both separators is reported as ErrNotCallbackAction
// so the caller can fall back to the legacy agent path instead of dropping the
// press.
func DecodeCallback(data string) (CallbackAction, error) {
	// telebot prefixes the data of a button bound to a unique endpoint with a
	// form feed. joshbot's buttons are not bound that way, but a stray prefix
	// would otherwise fold into the namespace and make every action unknown.
	data = strings.TrimPrefix(data, "\f")

	parts := strings.SplitN(data, callbackSep, 3)
	if len(parts) < 3 {
		return CallbackAction{}, ErrNotCallbackAction
	}
	if parts[0] == "" || parts[1] == "" {
		return CallbackAction{}, ErrNotCallbackAction
	}
	return CallbackAction{Namespace: parts[0], Action: parts[1], Payload: parts[2]}, nil
}

// InlineButton is one button of a typed inline keyboard.
//
// Exactly one of URL and Action is meaningful: a URL button opens a link and
// never reaches the bot, an Action button posts its encoded envelope back as
// callback_data.
type InlineButton struct {
	Text   string
	URL    string
	Action *CallbackAction
}

// ActionButton is the common case: a button that dispatches to a namespace.
func ActionButton(text, namespace, action, payload string) InlineButton {
	return InlineButton{
		Text:   text,
		Action: &CallbackAction{Namespace: namespace, Action: action, Payload: payload},
	}
}

// URLButton is a button that opens a link instead of calling back.
func URLButton(text, url string) InlineButton {
	return InlineButton{Text: text, URL: url}
}

// Keyboard is a typed inline keyboard, built in-process and attached to an
// outbound message under the "reply_markup" metadata key.
//
// It exists because the map[string]any form cannot express a validated
// callback envelope: a map can only carry an already-encoded string, so an
// over-long or malformed one is discovered by Telegram at send time, or worse
// by the router at press time. Build reports those at build time instead.
type Keyboard struct {
	Rows [][]InlineButton
}

// Row appends a row of buttons and returns the keyboard for chaining.
func (k *Keyboard) Row(buttons ...InlineButton) *Keyboard {
	k.Rows = append(k.Rows, buttons)
	return k
}

// Build renders the keyboard, failing on any button whose action cannot be
// encoded — an empty namespace, a separator in a field, or a payload that puts
// the envelope over CallbackDataMaxBytes.
func (k *Keyboard) Build() (*telebot.ReplyMarkup, error) {
	rows := make([][]telebot.InlineButton, 0, len(k.Rows))
	for i, row := range k.Rows {
		out := make([]telebot.InlineButton, 0, len(row))
		for j, b := range row {
			if b.Text == "" {
				return nil, fmt.Errorf("button %d,%d has no text", i, j)
			}
			btn := telebot.InlineButton{Text: b.Text, URL: b.URL}
			if b.Action != nil {
				data, err := b.Action.Encode()
				if err != nil {
					return nil, fmt.Errorf("button %d,%d (%q): %w", i, j, b.Text, err)
				}
				btn.Data = data
			}
			out = append(out, btn)
		}
		rows = append(rows, out)
	}
	return &telebot.ReplyMarkup{InlineKeyboard: rows}, nil
}

// CallbackPress is what a registered handler receives for a button press. It
// carries everything a handler needs to answer the press and edit the message
// the button was attached to, without the handler reaching back into telebot.
type CallbackPress struct {
	Action     CallbackAction
	CallbackID string
	ChatID     int64
	MessageID  int
	SenderID   int64
	Username   string
}

// CallbackHandler handles a press for one namespace. Returning an error is
// logged; it does not reach the user, so a handler that wants to say something
// must send it.
type CallbackHandler func(ctx context.Context, press CallbackPress) error
