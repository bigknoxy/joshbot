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

type fakePickerBackend struct {
	mu        sync.Mutex
	models    []PickerChoice
	personas  []PickerChoice
	processed []bus.InboundMessage
	reply     string
	err       error
}

func (f *fakePickerBackend) ModelChoices(context.Context, bus.InboundMessage) ([]PickerChoice, error) {
	return f.models, nil
}
func (f *fakePickerBackend) PersonalityChoices(context.Context, bus.InboundMessage) ([]PickerChoice, error) {
	return f.personas, nil
}
func (f *fakePickerBackend) Process(_ context.Context, msg bus.InboundMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.processed = append(f.processed, msg)
	if f.err != nil {
		return "", f.err
	}
	// Move the marker the way the agent would.
	for i := range f.models {
		f.models[i].Current = "/model "+f.models[i].Spec == msg.Content
	}
	return f.reply, nil
}

func pickerTestChannel(t *testing.T) (*TelegramChannel, *Picker, *fakePickerBackend, *fakeEditor) {
	t.Helper()
	tg := newTestTelegramChannel()
	ed := &fakeEditor{}
	tg.mu.Lock()
	tg.editor = ed
	tg.mu.Unlock()
	b := &fakePickerBackend{
		models: []PickerChoice{
			{Spec: "nvidia", Label: "nvidia · z-ai/glm-5.2", Current: true},
			{Spec: "poolside", Label: "poolside · poolside/laguna-s-2.1"},
			{Spec: "groq", Label: "groq · llama"},
		},
		personas: []PickerChoice{{Spec: "concise", Label: "concise"}, {Spec: "none", Label: "none", Current: true}},
		reply:    "✓ Model switched to poolside for this session.",
	}
	p, err := tg.NewPicker(b)
	if err != nil {
		t.Fatalf("NewPicker: %v", err)
	}
	return tg, p, b, ed
}

func buttonTexts(rm *telebot.ReplyMarkup) [][]string {
	var out [][]string
	for _, row := range rm.InlineKeyboard {
		var r []string
		for _, b := range row {
			r = append(r, b.Text)
		}
		out = append(out, r)
	}
	return out
}

// A bare /model on this channel gets a keyboard: two buttons per row, the
// current entry marked, every button a decodable envelope in the model
// namespace carrying the spec as payload.
func TestPicker_KeyboardForBareCommand(t *testing.T) {
	_, p, _, _ := pickerTestChannel(t)
	kb := p.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/model"})
	if kb == nil {
		t.Fatal("bare /model should get a keyboard")
	}
	rm, err := kb.Build()
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(buttonTexts(rm))
	want := "[[✅ nvidia · z-ai/glm-5.2 poolside · poolside/laguna-s-2.1] [groq · llama]]"
	if got != want {
		t.Errorf("layout = %s, want %s", got, want)
	}
	a, err := DecodeCallback(rm.InlineKeyboard[0][1].Data)
	if err != nil || a.Namespace != ModelPickerNamespace || a.Action != pickerPickAction || a.Payload != "poolside" {
		t.Errorf("button envelope = %+v, %v", a, err)
	}

	for _, msg := range []bus.InboundMessage{
		{Channel: "telegram", Content: "/model poolside"}, // a switch, not a listing
		{Channel: "telegram", Content: "/model --global"}, // not bare
		{Channel: "discord", Content: "/model"},           // other channel
		{Channel: "telegram", Content: "/status"},         // not a picker
	} {
		if p.Keyboard(context.Background(), msg) != nil {
			t.Errorf("%q on %s should get no keyboard", msg.Content, msg.Channel)
		}
	}
	if kb := p.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/personality"}); kb == nil {
		t.Error("bare /personality should get a keyboard")
	}
	var nilPicker *Picker
	if nilPicker.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/model"}) != nil {
		t.Error("a nil picker must be inert")
	}
}

// A spec that cannot fit callback_data is left off rather than truncated —
// a truncated envelope can decode as a different, valid choice.
func TestPicker_OverlongSpecIsLeftOff(t *testing.T) {
	long := strings.Repeat("x", 70)
	kb := pickerKeyboard(ModelPickerNamespace, []PickerChoice{{Spec: long, Label: "long"}, {Spec: "ok", Label: "ok"}})
	rm, err := kb.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(buttonTexts(rm)); got != "[[ok]]" {
		t.Errorf("layout = %s, want only the fitting button", got)
	}
	if pickerKeyboard(ModelPickerNamespace, []PickerChoice{{Spec: long, Label: "long"}}) != nil {
		t.Error("a keyboard with no buttons left must be nil, not empty")
	}
}

// A press runs the command turn for the presser's own session and edits the
// picker message in place with the reply and a refreshed keyboard.
func TestPicker_PressSwitchesAndEditsInPlace(t *testing.T) {
	_, p, b, ed := pickerTestChannel(t)
	err := p.handleModelPress(context.Background(), CallbackPress{
		Action:    CallbackAction{Namespace: ModelPickerNamespace, Action: pickerPickAction, Payload: "poolside"},
		ChatID:    42,
		MessageID: 7,
		SenderID:  1234,
		Username:  "josh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.processed) != 1 {
		t.Fatalf("expected one command turn, got %d", len(b.processed))
	}
	in := b.processed[0]
	if in.Content != "/model poolside" || in.SenderID != "telegram_1234" || in.Channel != "telegram" {
		t.Errorf("synthesized inbound = %+v", in)
	}
	if in.Metadata["is_command"] != true || in.Metadata["chat_id"] != int64(42) {
		t.Errorf("inbound metadata = %+v", in.Metadata)
	}
	ed.mu.Lock()
	defer ed.mu.Unlock()
	if len(ed.calls) != 1 || !ed.calls[0].edit || ed.calls[0].chat != "42" || ed.calls[0].text != b.reply {
		t.Fatalf("expected one in-place edit with the reply, got %+v", ed.calls)
	}
	if ed.calls[0].markup == nil {
		t.Fatal("edit should carry a refreshed keyboard")
	}
	texts := fmt.Sprint(buttonTexts(ed.calls[0].markup))
	if !strings.Contains(texts, "✅ poolside") || strings.Contains(texts, "✅ nvidia") {
		t.Errorf("marker should have moved to poolside: %s", texts)
	}
}

func TestPicker_IgnoresForeignActionsAndClaimsNamespacesOnce(t *testing.T) {
	tg, p, b, ed := pickerTestChannel(t)
	_ = p.handleModelPress(context.Background(), CallbackPress{
		Action: CallbackAction{Namespace: ModelPickerNamespace, Action: "other", Payload: "poolside"},
	})
	_ = p.handleModelPress(context.Background(), CallbackPress{
		Action: CallbackAction{Namespace: ModelPickerNamespace, Action: pickerPickAction, Payload: ""},
	})
	if len(b.processed) != 0 || len(ed.calls) != 0 {
		t.Errorf("unknown action or empty payload must do nothing: %d turns, %d edits", len(b.processed), len(ed.calls))
	}
	if _, err := tg.NewPicker(b); err == nil {
		t.Error("a second picker must be refused: the namespace is claimed")
	}
	if _, err := tg.NewPicker(nil); err == nil {
		t.Error("nil backend must be refused")
	}
}

// A failed turn edits the message with a safe text and no keyboard: the raw
// error (which can wrap provider or path detail) stays in the log, and a
// picker under a failure would invite a press into it. An in-band "Error:"
// reply is shown as the command would show it, also without a keyboard.
func TestPicker_FailureShowsNoKeyboardAndNoRawError(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		err         error
		wantText    string
	}{
		{"process error", "", errors.New("dial tcp 10.0.0.7: secret detail"), "Could not apply that choice"},
		{"in-band error", "Error: unknown model \"x\"", nil, "Error: unknown model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p, b, ed := pickerTestChannel(t)
			b.reply, b.err = tc.reply, tc.err
			if err := p.handleModelPress(context.Background(), CallbackPress{
				Action: CallbackAction{Namespace: ModelPickerNamespace, Action: pickerPickAction, Payload: "poolside"},
				ChatID: 42, MessageID: 7, SenderID: 1,
			}); err != nil {
				t.Fatal(err)
			}
			ed.mu.Lock()
			defer ed.mu.Unlock()
			if len(ed.calls) != 1 || !strings.Contains(ed.calls[0].text, tc.wantText) {
				t.Fatalf("edit = %+v, want text containing %q", ed.calls, tc.wantText)
			}
			if strings.Contains(ed.calls[0].text, "secret detail") {
				t.Error("raw error reached the chat")
			}
			if ed.calls[0].markup != nil {
				t.Error("no keyboard under a failure")
			}
		})
	}
}
