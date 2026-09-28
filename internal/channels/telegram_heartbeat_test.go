package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
)

type fakeHeartbeatBackend struct {
	tasks     []HeartbeatTask
	toggleErr error
	tasksErr  error
	toggled   []string
}

func (f *fakeHeartbeatBackend) Tasks() ([]HeartbeatTask, error) { return f.tasks, f.tasksErr }

func (f *fakeHeartbeatBackend) Toggle(line int, key string, shownDone bool) ([]HeartbeatTask, string, error) {
	f.toggled = append(f.toggled, fmt.Sprintf("%d:%s:%v", line, key, shownDone))
	if f.toggleErr != nil {
		return nil, "", f.toggleErr
	}
	for i := range f.tasks {
		if f.tasks[i].Line == line && f.tasks[i].Key == key {
			f.tasks[i].Done = !f.tasks[i].Done
		}
	}
	return f.tasks, f.Summary(f.tasks), nil
}

func (f *fakeHeartbeatBackend) Summary(tasks []HeartbeatTask) string {
	return fmt.Sprintf("summary of %d", len(tasks))
}

func heartbeatTestChannel(t *testing.T) (*TelegramChannel, *HeartbeatBoard, *fakeHeartbeatBackend, *fakeEditor) {
	t.Helper()
	tg := newTestTelegramChannel()
	ed := &fakeEditor{}
	tg.mu.Lock()
	tg.editor = ed
	tg.mu.Unlock()
	b := &fakeHeartbeatBackend{tasks: []HeartbeatTask{
		{Line: 1, Key: "aaaaaaaa", Text: "water plants"},
		{Line: 3, Key: "bbbbbbbb", Text: "pay rent", Done: true},
	}}
	hb, err := tg.NewHeartbeatBoard(b)
	if err != nil {
		t.Fatalf("NewHeartbeatBoard: %v", err)
	}
	return tg, hb, b, ed
}

func hbPress(payload string) CallbackPress {
	return CallbackPress{
		Action: CallbackAction{Namespace: HeartbeatBoardNamespace, Action: heartbeatToggleAction, Payload: payload},
		ChatID: 42, MessageID: 7, SenderID: 1,
	}
}

// One column, the state on the label, and a payload naming line and key.
func TestHeartbeatBoard_KeyboardIsOneColumnWithStateInTheLabels(t *testing.T) {
	_, hb, _, _ := heartbeatTestChannel(t)
	kb := hb.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/heartbeat"})
	if kb == nil {
		t.Fatal("bare /heartbeat should get the keyboard")
	}
	rm, err := kb.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(buttonTexts(rm)); got != "[[☐ water plants] [✅ pay rent]]" {
		t.Errorf("buttons = %s", got)
	}
	act, err := DecodeCallback(rm.InlineKeyboard[1][0].Data)
	if err != nil || act.Namespace != HeartbeatBoardNamespace || act.Payload != "3:bbbbbbbb:1" {
		t.Errorf("second button decodes to %+v, %v", act, err)
	}
	for _, msg := range []bus.InboundMessage{
		{Channel: "discord", Content: "/heartbeat"},
		{Channel: "telegram", Content: "/heartbeat now"},
		{Channel: "telegram", Content: "/status"},
	} {
		if hb.Keyboard(context.Background(), msg) != nil {
			t.Errorf("no keyboard expected for %+v", msg)
		}
	}
	var nilBoard *HeartbeatBoard
	if nilBoard.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/heartbeat"}) != nil {
		t.Error("a nil board must return no keyboard")
	}
}

func TestHeartbeatBoard_NoTasksOrUnreadableMeansNoKeyboard(t *testing.T) {
	_, hb, b, _ := heartbeatTestChannel(t)
	b.tasks = nil
	if hb.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/heartbeat"}) != nil {
		t.Error("no tasks: no keyboard")
	}
	b.tasksErr = errors.New("permission denied")
	if hb.Keyboard(context.Background(), bus.InboundMessage{Channel: "telegram", Content: "/heartbeat"}) != nil {
		t.Error("unreadable file: no keyboard")
	}
}

func TestHeartbeatBoard_LongListsAndLabelsAreBounded(t *testing.T) {
	var tasks []HeartbeatTask
	for i := 0; i < heartbeatMaxButtons+5; i++ {
		tasks = append(tasks, HeartbeatTask{Line: i, Key: "cccccccc", Text: strings.Repeat("é", 80)})
	}
	kb := heartbeatKeyboard(tasks)
	if len(kb.Rows) != heartbeatMaxButtons {
		t.Errorf("rows = %d, want the cap %d", len(kb.Rows), heartbeatMaxButtons)
	}
	label := kb.Rows[0][0].Text
	if r := []rune(strings.TrimPrefix(label, "☐ ")); len(r) != heartbeatLabelRunes || r[len(r)-1] != '…' {
		t.Errorf("label not truncated on a rune boundary: %q", label)
	}
	if _, err := kb.Build(); err != nil {
		t.Errorf("capped keyboard must build: %v", err)
	}
}

func TestHeartbeatBoard_PressTogglesAndEditsInPlace(t *testing.T) {
	_, hb, b, ed := heartbeatTestChannel(t)
	if err := hb.handlePress(context.Background(), hbPress("1:aaaaaaaa:0")); err != nil {
		t.Fatal(err)
	}
	if len(b.toggled) != 1 || b.toggled[0] != "1:aaaaaaaa:false" {
		t.Fatalf("toggled = %v", b.toggled)
	}
	if len(ed.calls) != 1 || !ed.calls[0].edit || ed.calls[0].chat != "42" || ed.calls[0].text != "summary of 2" {
		t.Fatalf("edits = %+v", ed.calls)
	}
	if got := fmt.Sprint(buttonTexts(ed.calls[0].markup)); got != "[[✅ water plants] [✅ pay rent]]" {
		t.Errorf("keyboard should show the new state: %s", got)
	}
}

// A stale press shows the file as it is now, with a note, and toggles nothing;
// a write failure says so without the raw error.
func TestHeartbeatBoard_StaleOrFailedPressRerendersCurrentList(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"stale", ErrHeartbeatTaskChanged, heartbeatChanged},
		{"write failed", errors.New("open /home/x/HEARTBEAT.md: read-only file system"), heartbeatFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, hb, b, ed := heartbeatTestChannel(t)
			b.toggleErr = tc.err
			if err := hb.handlePress(context.Background(), hbPress("1:aaaaaaaa:0")); err != nil {
				t.Fatal(err)
			}
			if len(ed.calls) != 1 || ed.calls[0].text != tc.want+"\n\nsummary of 2" {
				t.Fatalf("edit = %+v", ed.calls)
			}
			if strings.Contains(ed.calls[0].text, "read-only") {
				t.Error("raw error reached the chat")
			}
			if ed.calls[0].markup == nil {
				t.Error("the current keyboard should be redrawn so the next press is fresh")
			}
		})
	}
}

func TestHeartbeatBoard_IgnoresMalformedPressesAndClaimsNamespaceOnce(t *testing.T) {
	tg, hb, b, ed := heartbeatTestChannel(t)
	for _, p := range []CallbackPress{
		{Action: CallbackAction{Namespace: HeartbeatBoardNamespace, Action: "other", Payload: "1:aaaaaaaa:0"}},
		hbPress("x:aaaaaaaa:0"),
		hbPress("-1:aaaaaaaa:0"),
		hbPress("1::0"),
		hbPress("1:aaaaaaaa"),
		hbPress("1:aaaaaaaa:2"),
		hbPress("1:aaaaaaaa:0:extra"),
	} {
		if err := hb.handlePress(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.toggled) != 0 || len(ed.calls) != 0 {
		t.Errorf("malformed presses must do nothing: %v, %d edits", b.toggled, len(ed.calls))
	}
	if _, err := tg.NewHeartbeatBoard(b); err == nil {
		t.Error("a second board must be refused: the namespace is claimed")
	}
	if _, err := tg.NewHeartbeatBoard(nil); err == nil {
		t.Error("nil backend must be refused")
	}
}
