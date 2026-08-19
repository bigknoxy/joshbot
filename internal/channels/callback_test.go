package channels

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v3"
)

// TestCallbackEncodeRoundTrip pins the envelope grammar, including the two
// properties that are easy to lose: a payload may contain the separator
// (only the first two fields are split) and an empty payload is legal.
func TestCallbackEncodeRoundTrip(t *testing.T) {
	cases := []CallbackAction{
		{Namespace: "approve", Action: "allow", Payload: "7f3a"},
		{Namespace: "model", Action: "set", Payload: "openrouter:z-ai/glm-5.2"},
		{Namespace: "hb", Action: "toggle", Payload: ""},
	}
	for _, want := range cases {
		encoded, err := want.Encode()
		if err != nil {
			t.Fatalf("Encode(%+v): %v", want, err)
		}
		got, err := DecodeCallback(encoded)
		if err != nil {
			t.Fatalf("DecodeCallback(%q): %v", encoded, err)
		}
		if got != want {
			t.Errorf("round trip of %q = %+v, want %+v", encoded, got, want)
		}
	}
}

// TestCallbackEncodeRefusesOverLimit is the reason Encode returns an error at
// all. Telegram caps callback_data at 64 bytes and rejects a longer one, so a
// silent truncation ships a button that decodes to some other action when
// pressed — a wrong-action bug that only appears in production.
func TestCallbackEncodeRefusesOverLimit(t *testing.T) {
	fits := CallbackAction{Namespace: "ns", Action: "a", Payload: strings.Repeat("x", 59)}
	if _, err := fits.Encode(); err != nil {
		t.Fatalf("a %d-byte envelope should encode: %v", CallbackDataMaxBytes, err)
	}

	over := CallbackAction{Namespace: "ns", Action: "a", Payload: strings.Repeat("x", 60)}
	encoded, err := over.Encode()
	if err == nil {
		t.Fatalf("a %d-byte envelope encoded to %q instead of erroring", len(encoded), encoded)
	}
	if encoded != "" {
		t.Errorf("a failed Encode returned data %q; a caller ignoring the error would send it", encoded)
	}
}

// TestCallbackEncodeRefusesMalformedFields: a separator inside a namespace or
// action makes decoding pick up the wrong fields, so it has to be refused at
// build time rather than producing a button that dispatches elsewhere.
func TestCallbackEncodeRefusesMalformedFields(t *testing.T) {
	cases := map[string]CallbackAction{
		"empty namespace":     {Action: "a"},
		"empty action":        {Namespace: "ns"},
		"separator in ns":     {Namespace: "n:s", Action: "a"},
		"separator in action": {Namespace: "ns", Action: "a:b"},
	}
	for name, a := range cases {
		if _, err := a.Encode(); err == nil {
			t.Errorf("%s: Encode succeeded, want an error", name)
		}
	}
}

// TestDecodeCallbackReportsNonEnvelopeData: unknown data must be
// distinguishable so handleCallback can fall back to the agent instead of
// dropping the press.
func TestDecodeCallbackReportsNonEnvelopeData(t *testing.T) {
	for _, data := range []string{"", "plain", "one:two", ":action:p", "ns::p"} {
		if _, err := DecodeCallback(data); !errors.Is(err, ErrNotCallbackAction) {
			t.Errorf("DecodeCallback(%q) error = %v, want ErrNotCallbackAction", data, err)
		}
	}
}

// TestDecodeCallbackStripsTelebotPrefix: telebot prefixes the data of a button
// bound to a unique endpoint with a form feed. Folded into the namespace it
// would make every action unknown and silently route every press back to the
// agent — the exact regression this package exists to remove.
func TestDecodeCallbackStripsTelebotPrefix(t *testing.T) {
	got, err := DecodeCallback("\fapprove:allow:7f3a")
	if err != nil {
		t.Fatalf("DecodeCallback: %v", err)
	}
	if got.Namespace != "approve" {
		t.Errorf("Namespace = %q, want approve", got.Namespace)
	}
}

// TestKeyboardBuildEncodesAndPropagatesErrors: Build is where an unencodable
// button is caught. Returning a partial keyboard would ship the good buttons
// and silently drop the bad one.
func TestKeyboardBuildEncodesAndPropagatesErrors(t *testing.T) {
	k := (&Keyboard{}).
		Row(ActionButton("Allow", "approve", "allow", "7f3a"), ActionButton("Deny", "approve", "deny", "7f3a")).
		Row(URLButton("Docs", "https://example.com"))

	rm, err := k.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rm.InlineKeyboard) != 2 || len(rm.InlineKeyboard[0]) != 2 {
		t.Fatalf("keyboard shape = %v", rm.InlineKeyboard)
	}
	if rm.InlineKeyboard[0][0].Data != "approve:allow:7f3a" {
		t.Errorf("Data = %q", rm.InlineKeyboard[0][0].Data)
	}
	if rm.InlineKeyboard[1][0].URL != "https://example.com" {
		t.Errorf("URL button lost its URL: %+v", rm.InlineKeyboard[1][0])
	}
	if rm.InlineKeyboard[1][0].Data != "" {
		t.Errorf("a URL button must carry no callback data, got %q", rm.InlineKeyboard[1][0].Data)
	}

	bad := (&Keyboard{}).Row(ActionButton("Too long", "approve", "allow", strings.Repeat("x", 64)))
	if rm, err := bad.Build(); err == nil {
		t.Fatalf("Build accepted an over-limit button: %+v", rm)
	}

	noText := (&Keyboard{}).Row(ActionButton("", "approve", "allow", "x"))
	if _, err := noText.Build(); err == nil {
		t.Error("Build accepted a button with no text")
	}
}

// TestRegisterCallbackRefusesDuplicates: two features sharing a namespace
// means one of them silently never runs, and the press lands in the other's
// handler.
func TestRegisterCallbackRefusesDuplicates(t *testing.T) {
	tg := mediaChannel(t, "1234")
	noop := func(context.Context, CallbackPress) error { return nil }

	if err := tg.RegisterCallback("approve", noop); err != nil {
		t.Fatalf("first RegisterCallback: %v", err)
	}
	if err := tg.RegisterCallback("approve", noop); err == nil {
		t.Error("a duplicate namespace registered instead of erroring")
	}
	if err := tg.RegisterCallback("", noop); err == nil {
		t.Error("an empty namespace registered")
	}
	if err := tg.RegisterCallback("a:b", noop); err == nil {
		t.Error("a namespace containing the separator registered")
	}
	if err := tg.RegisterCallback("other", nil); err == nil {
		t.Error("a nil handler registered")
	}
}

// TestHandleCallbackRoutesRegisteredNamespace is the point of the whole
// change: a registered press runs its handler with the decoded fields and does
// NOT become a synthetic "[Callback: ...]" user message, which previously
// spent a full ReAct turn per button press.
func TestHandleCallbackRoutesRegisteredNamespace(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)

	tg := mediaChannel(t, "1234")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	got := make(chan CallbackPress, 1)
	if err := tg.RegisterCallback("approve", func(_ context.Context, p CallbackPress) error {
		got <- p
		return nil
	}); err != nil {
		t.Fatalf("RegisterCallback: %v", err)
	}

	cb := &telebot.Callback{
		ID:      "cb1",
		Sender:  &telebot.User{ID: 1234, Username: "josh"},
		Message: &telebot.Message{ID: 8, Chat: &telebot.Chat{ID: 77}},
		Data:    "approve:allow:7f3a",
	}
	if err := tg.handleCallback(bot.NewContext(telebot.Update{Callback: cb})); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}

	select {
	case p := <-got:
		if p.Action.Action != "allow" || p.Action.Payload != "7f3a" {
			t.Errorf("handler got %+v", p.Action)
		}
		if p.ChatID != 77 || p.MessageID != 8 || p.SenderID != 1234 || p.CallbackID != "cb1" {
			t.Errorf("handler lost press context: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("the registered handler never ran")
	}

	select {
	case msg := <-tg.bus.InboundChannel():
		t.Fatalf("a routed press still spent an agent turn: %q", msg.Content)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestHandleCallbackFallsBackForUnknownNamespace: an unregistered namespace,
// and data that is not an envelope at all, must both still reach the agent.
// Dropping them would break every button an older joshbot produced.
func TestHandleCallbackFallsBackForUnknownNamespace(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)

	tg := mediaChannel(t, "1234")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	if err := tg.RegisterCallback("approve", func(context.Context, CallbackPress) error {
		t.Error("the approve handler ran for another namespace")
		return nil
	}); err != nil {
		t.Fatalf("RegisterCallback: %v", err)
	}

	for _, data := range []string{"model:set:gpt", "legacy-button"} {
		cb := &telebot.Callback{
			ID:      "cb",
			Sender:  &telebot.User{ID: 1234, Username: "josh"},
			Message: &telebot.Message{ID: 8, Chat: &telebot.Chat{ID: 77}},
			Data:    data,
		}
		if err := tg.handleCallback(bot.NewContext(telebot.Update{Callback: cb})); err != nil {
			t.Fatalf("handleCallback(%q): %v", data, err)
		}
		select {
		case msg := <-tg.bus.InboundChannel():
			if msg.Content != "[Callback: "+data+"]" {
				t.Errorf("Content = %q, want the legacy form for %q", msg.Content, data)
			}
		case <-time.After(time.Second):
			t.Fatalf("an unrouted press for %q never reached the agent", data)
		}
	}
}

// TestNormalizeKeyboardRowsAcceptsJSONShape: metadata that arrived as decoded
// JSON is []any of []any of map[string]any, which the old
// [][]map[string]any type assertion could never match — so both keyboard
// branches of buildReplyMarkup were unreachable and returned an empty markup
// with no error at all.
func TestNormalizeKeyboardRowsAcceptsJSONShape(t *testing.T) {
	tg := mediaChannel(t, "1234")

	rm := tg.buildReplyMarkup(map[string]any{
		"inline_keyboard": []any{
			[]any{
				map[string]any{"text": "Yes", "callback_data": "approve:allow:1"},
				map[string]any{"text": "Docs", "url": "https://example.com"},
			},
		},
	})
	if len(rm.InlineKeyboard) != 1 || len(rm.InlineKeyboard[0]) != 2 {
		t.Fatalf("JSON-shaped inline keyboard did not build: %+v", rm.InlineKeyboard)
	}
	if rm.InlineKeyboard[0][0].Data != "approve:allow:1" {
		t.Errorf("Data = %q", rm.InlineKeyboard[0][0].Data)
	}
	if rm.InlineKeyboard[0][1].URL != "https://example.com" {
		t.Errorf("URL = %q", rm.InlineKeyboard[0][1].URL)
	}

	// The in-process Go shape must keep working.
	rm = tg.buildReplyMarkup(map[string]any{
		"keyboard": [][]map[string]any{{{"text": "Share", "request_contact": true}}},
	})
	if len(rm.ReplyKeyboard) != 1 || !rm.ReplyKeyboard[0][0].Contact {
		t.Errorf("reply keyboard did not build: %+v", rm.ReplyKeyboard)
	}

	// A shape that is neither must not be interpreted as an empty keyboard.
	rm = tg.buildReplyMarkup(map[string]any{"inline_keyboard": "nonsense"})
	if len(rm.InlineKeyboard) != 0 {
		t.Errorf("garbage produced buttons: %+v", rm.InlineKeyboard)
	}
}
