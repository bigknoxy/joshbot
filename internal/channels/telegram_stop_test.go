package channels

import (
	"context"
	"testing"
	"time"
)

func stopTestChannel(t *testing.T) (*TelegramChannel, *StopCoordinator, *fakeEditor) {
	t.Helper()
	tg, ed := streamTestChannel(t, 0)
	c, err := tg.NewStopCoordinator()
	if err != nil {
		t.Fatalf("NewStopCoordinator: %v", err)
	}
	return tg, c, ed
}

// A press on an armed token cancels the turn's context immediately — from
// the caller's goroutine, with no bus hop and no lock — and reports pressed.
func TestStop_PressCancelsTheArmedTurn(t *testing.T) {
	_, c, _ := stopTestChannel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	token, pressed, release := c.Arm(42, cancel)
	defer release()

	rm := c.Markup(token)
	if rm == nil || len(rm.InlineKeyboard) != 1 || rm.InlineKeyboard[0][0].Text != stopButtonText {
		t.Fatalf("markup = %+v", rm)
	}
	a, err := DecodeCallback(rm.InlineKeyboard[0][0].Data)
	if err != nil || a.Namespace != StopCallbackNamespace || a.Action != stopAction || a.Payload != token {
		t.Fatalf("button envelope = %+v, %v", a, err)
	}

	if err := c.handlePress(context.Background(), CallbackPress{Action: a, ChatID: 42}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("press did not cancel the turn context")
	}
	if !pressed() {
		t.Error("pressed() should report the press")
	}
}

// A press from another chat, with a stale token, a wrong action, or after
// release does nothing: the button is valid only for the chat and the turn
// it was issued for.
func TestStop_ForeignStaleAndReleasedPressesAreIgnored(t *testing.T) {
	_, c, _ := stopTestChannel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	token, pressed, release := c.Arm(42, cancel)

	for name, press := range map[string]CallbackPress{
		"other chat":   {Action: CallbackAction{Namespace: StopCallbackNamespace, Action: stopAction, Payload: token}, ChatID: 43},
		"stale token":  {Action: CallbackAction{Namespace: StopCallbackNamespace, Action: stopAction, Payload: "zz"}, ChatID: 42},
		"wrong action": {Action: CallbackAction{Namespace: StopCallbackNamespace, Action: "x", Payload: token}, ChatID: 42},
	} {
		_ = c.handlePress(context.Background(), press)
		if ctx.Err() != nil || pressed() {
			t.Fatalf("%s: cancelled the turn", name)
		}
	}
	release()
	_ = c.handlePress(context.Background(), CallbackPress{Action: CallbackAction{Namespace: StopCallbackNamespace, Action: stopAction, Payload: token}, ChatID: 42})
	if ctx.Err() != nil {
		t.Error("a released token must not cancel anything")
	}
	c.mu.Lock()
	n := len(c.turns)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("release must drop the entry, %d left", n)
	}
}

// The button rides every interim send and edit — status line and partials —
// and is absent from the final edit, which is what removes it from the
// message once the turn it cancels is over.
func TestStop_ButtonOnInterimEditsNotOnFinal(t *testing.T) {
	tg, c, ed := stopTestChannel(t)
	token, _, release := c.Arm(7, func() {})
	defer release()
	var slept time.Duration
	s := newTestStreamer(t, tg, 7, &slept)
	s.SetInterimMarkup(c.Markup(token))

	s.Status("⚙️ shell")
	s.Delta("Hello")
	s.Delta(" world")
	if !s.Finish(nil) {
		t.Fatal("Finish should report delivery")
	}
	ed.mu.Lock()
	defer ed.mu.Unlock()
	if len(ed.calls) < 2 {
		t.Fatalf("expected interim and final calls, got %+v", ed.calls)
	}
	for i, call := range ed.calls[:len(ed.calls)-1] {
		if call.markup == nil {
			t.Errorf("interim call %d (%q) carries no Stop button", i, call.text)
		}
	}
	if last := ed.calls[len(ed.calls)-1]; last.markup != nil || last.text != "Hello world" {
		t.Errorf("final edit must drop the button: %+v", last)
	}
}
