package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"gopkg.in/telebot.v3"
)

type fakeStatusBackend struct {
	mu        sync.Mutex
	processed []bus.InboundMessage
	reply     string
	err       error
}

func (f *fakeStatusBackend) Process(_ context.Context, msg bus.InboundMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.processed = append(f.processed, msg)
	return f.reply, f.err
}

// pinEditor adds Pin to fakeEditor, the way *telebot.Bot has it.
type pinEditor struct {
	*fakeEditor
	pinErr  error
	pinned  []string
	pinOpts [][]interface{}
}

func (p *pinEditor) Pin(msg telebot.Editable, opts ...interface{}) error {
	id, chat := msg.MessageSig()
	p.pinned = append(p.pinned, fmt.Sprintf("%d/%s", chat, id))
	p.pinOpts = append(p.pinOpts, opts)
	return p.pinErr
}

func statusTestChannel(t *testing.T, ed telegramEditor) (*TelegramChannel, *StatusPanel, *fakeStatusBackend) {
	t.Helper()
	tg := newTestTelegramChannel()
	tg.mu.Lock()
	tg.editor = ed
	tg.mu.Unlock()
	b := &fakeStatusBackend{reply: "Status:\n  As of: 10:05 UTC\n  Model: nvidia"}
	sp, err := tg.NewStatusPanel(b)
	if err != nil {
		t.Fatalf("NewStatusPanel: %v", err)
	}
	return tg, sp, b
}

func statusPress(action, payload, oldText string) CallbackPress {
	return CallbackPress{
		Action:      CallbackAction{Namespace: StatusPanelNamespace, Action: action, Payload: payload},
		ChatID:      42,
		MessageID:   7,
		SenderID:    1234,
		Username:    "josh",
		MessageText: oldText,
	}
}

func TestStatusPanel_KeyboardOnlyForBareStatusOnThisChannel(t *testing.T) {
	_, sp, _ := statusTestChannel(t, &fakeEditor{})
	kb := sp.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/status"})
	if kb == nil {
		t.Fatal("bare /status should get the panel keyboard")
	}
	rm, err := kb.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(buttonTexts(rm)); got != "[[🔃 Refresh 📌 Pin]]" {
		t.Errorf("buttons = %s", got)
	}
	for _, msg := range []bus.InboundMessage{
		{Channel: "discord", Content: "/status"},
		{Channel: "telegram", Content: "/status now"},
		{Channel: "telegram", Content: "/model"},
		{Channel: "telegram", Content: "status please"},
	} {
		if sp.Keyboard(context.Background(), msg) != nil {
			t.Errorf("no keyboard expected for %+v", msg)
		}
	}
	var nilPanel *StatusPanel
	if nilPanel.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/status"}) != nil {
		t.Error("a nil panel must return no keyboard")
	}
}

// Refresh runs /status for the presser's own session and edits in place.
func TestStatusPanel_RefreshEditsInPlace(t *testing.T) {
	ed := &fakeEditor{}
	_, sp, b := statusTestChannel(t, ed)
	if err := sp.handlePress(context.Background(), statusPress(statusRefreshAction, "", "old")); err != nil {
		t.Fatal(err)
	}
	if len(b.processed) != 1 {
		t.Fatalf("want one command turn, got %d", len(b.processed))
	}
	in := b.processed[0]
	if in.Content != "/status" || in.SenderID != "telegram_1234" || in.Metadata["is_command"] != true || in.Metadata["chat_id"] != int64(42) {
		t.Errorf("synthesized inbound = %+v", in)
	}
	if len(ed.calls) != 1 || !ed.calls[0].edit || ed.calls[0].chat != "42" || ed.calls[0].text != b.reply {
		t.Fatalf("want one in-place edit with the fresh status, got %+v", ed.calls)
	}
	if got := fmt.Sprint(buttonTexts(ed.calls[0].markup)); got != "[[🔃 Refresh 📌 Pin]]" {
		t.Errorf("unpinned refresh keeps both buttons, got %s", got)
	}
}

// Pin pins silently, then the keyboard carries the pinned state itself: only
// Refresh remains, and a later refresh keeps it that way without a store.
func TestStatusPanel_PinIsRememberedByTheKeyboard(t *testing.T) {
	ed := &pinEditor{fakeEditor: &fakeEditor{}}
	_, sp, _ := statusTestChannel(t, ed)
	if err := sp.handlePress(context.Background(), statusPress(statusPinAction, "", "old")); err != nil {
		t.Fatal(err)
	}
	if len(ed.pinned) != 1 || ed.pinned[0] != "42/7" {
		t.Fatalf("pinned = %v, want the pressed message", ed.pinned)
	}
	silent := false
	for _, o := range ed.pinOpts[0] {
		if o == telebot.Silent {
			silent = true
		}
	}
	if !silent {
		t.Error("the pin must be silent")
	}
	markup := ed.calls[0].markup
	if got := fmt.Sprint(buttonTexts(markup)); got != "[[🔃 Refresh]]" {
		t.Fatalf("after a pin only Refresh remains, got %s", got)
	}
	data := markup.InlineKeyboard[0][0].Data
	act, err := DecodeCallback(data)
	if err != nil || act.Payload != statusPinnedPayload {
		t.Fatalf("refresh button should carry the pinned state, data=%q err=%v", data, err)
	}
	// A refresh from the pinned keyboard stays pinned.
	if err := sp.handlePress(context.Background(), statusPress(statusRefreshAction, act.Payload, "old")); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(buttonTexts(ed.calls[1].markup)); got != "[[🔃 Refresh]]" {
		t.Errorf("refresh of a pinned panel re-offered Pin: %s", got)
	}
	if len(ed.pinned) != 1 {
		t.Error("a refresh must not pin again")
	}
}

func TestStatusPanel_PinFailureSaysSoAndKeepsOffering(t *testing.T) {
	for _, tc := range []struct {
		name string
		ed   telegramEditor
	}{
		{"pin refused", &pinEditor{fakeEditor: &fakeEditor{}, pinErr: errors.New("not enough rights")}},
		{"editor cannot pin", &fakeEditor{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sp, b := statusTestChannel(t, tc.ed)
			if err := sp.handlePress(context.Background(), statusPress(statusPinAction, "", "old")); err != nil {
				t.Fatal(err)
			}
			var calls []editorCall
			switch e := tc.ed.(type) {
			case *pinEditor:
				calls = e.calls
			case *fakeEditor:
				calls = e.calls
			}
			if len(calls) != 1 || !strings.HasPrefix(calls[0].text, statusPinFailed) || !strings.Contains(calls[0].text, b.reply) {
				t.Fatalf("edit = %+v, want the pin failure note over the fresh status", calls)
			}
			if strings.Contains(calls[0].text, "not enough rights") {
				t.Error("raw error reached the chat")
			}
			if got := fmt.Sprint(buttonTexts(calls[0].markup)); got != "[[🔃 Refresh 📌 Pin]]" {
				t.Errorf("a failed pin should keep offering Pin, got %s", got)
			}
		})
	}
}

// A failed refresh keeps the last good render, marks it outdated, never shows
// the raw error, and does not stack warnings across repeated failures.
func TestStatusPanel_FailedRefreshMarksTheOldRenderOutdated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		err   error
	}{
		{"process error", "", errors.New("dial tcp 10.0.0.7: secret detail")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ed := &fakeEditor{}
			_, sp, b := statusTestChannel(t, ed)
			b.reply, b.err = tc.reply, tc.err
			old := "Status:\n  As of: 09:00 UTC\n  Model: nvidia"
			if err := sp.handlePress(context.Background(), statusPress(statusRefreshAction, "", old)); err != nil {
				t.Fatal(err)
			}
			want := statusStalePrefix + "\n\n" + old
			if ed.calls[0].text != want {
				t.Fatalf("text = %q, want %q", ed.calls[0].text, want)
			}
			// Fail again, pressing on the already-marked render.
			if err := sp.handlePress(context.Background(), statusPress(statusRefreshAction, "", ed.calls[0].text)); err != nil {
				t.Fatal(err)
			}
			if ed.calls[1].text != want {
				t.Errorf("warnings stacked: %q", ed.calls[1].text)
			}
			if ed.calls[1].markup == nil {
				t.Error("the Refresh button must stay so the user can retry")
			}
		})
	}
}

// Two refreshes inside one minute render identical text; Telegram answers
// "message is not modified", which is a successful refresh.
func TestStatusPanel_NotModifiedIsNotAnError(t *testing.T) {
	ed := &fakeEditor{editErrs: []error{errors.New("telegram: Bad Request: message is not modified (400)")}}
	_, sp, _ := statusTestChannel(t, ed)
	if err := sp.handlePress(context.Background(), statusPress(statusRefreshAction, "", "old")); err != nil {
		t.Errorf("not-modified should be swallowed, got %v", err)
	}
	ed2 := &fakeEditor{editErrs: []error{errors.New("telegram: Bad Request: message to edit not found (400)")}}
	_, sp2, _ := statusTestChannel(t, ed2)
	if err := sp2.handlePress(context.Background(), statusPress(statusRefreshAction, "", "old")); err == nil {
		t.Error("a real edit failure must be reported")
	}
}

func TestStatusPanel_IgnoresForeignActionsAndClaimsNamespaceOnce(t *testing.T) {
	ed := &fakeEditor{}
	tg, sp, b := statusTestChannel(t, ed)
	if err := sp.handlePress(context.Background(), statusPress("other", "", "")); err != nil {
		t.Fatal(err)
	}
	if len(b.processed) != 0 || len(ed.calls) != 0 {
		t.Error("an unknown action must do nothing")
	}
	if _, err := tg.NewStatusPanel(b); err == nil {
		t.Error("a second panel must be refused: the namespace is claimed")
	}
	if _, err := tg.NewStatusPanel(nil); err == nil {
		t.Error("nil backend must be refused")
	}
}
