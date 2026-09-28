package channels

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
	"gopkg.in/telebot.v3"
)

// HEARTBEAT.md as a toggle keyboard (#317). A bare /heartbeat reply carries
// one button per task, one column; a tap flips that task's box in the file.
const (
	HeartbeatBoardNamespace = "heartbeat"
	heartbeatToggleAction   = "toggle"
	// heartbeatMaxButtons keeps the keyboard usable on a phone and well
	// under Telegram's 100-button limit. Tasks past it are still listed in
	// the message text; they are toggled by editing the file.
	heartbeatMaxButtons = 30
	// heartbeatLabelRunes truncates a button label, on a rune boundary: the
	// full text is in the message above the keyboard.
	heartbeatLabelRunes = 48
	heartbeatChanged    = "The task list changed since this was shown — here is the current one."
	heartbeatFailed     = "Could not update HEARTBEAT.md. Edit the file directly instead."
)

// HeartbeatTask mirrors heartbeat.Task without importing it, the way
// PickerChoice mirrors agent.Choice.
type HeartbeatTask struct {
	Line int
	Key  string
	Text string
	Done bool
}

// ErrHeartbeatTaskChanged is what a backend's Toggle returns when the named
// line no longer holds the task the keyboard was drawn from.
var ErrHeartbeatTaskChanged = errors.New("heartbeat task changed")

// HeartbeatBackend reads and toggles the tasks. It is satisfied by an adapter
// in cmd/joshbot over *heartbeat.Service, whose file lock the tick shares.
type HeartbeatBackend interface {
	Tasks() ([]HeartbeatTask, error)
	// Toggle flips the task at line if it still carries key and is still in
	// the state the button showed, and returns the tasks after the write
	// plus the text the message should show.
	Toggle(line int, key string, shownDone bool) (tasks []HeartbeatTask, summary string, err error)
	// Summary renders tasks as the /heartbeat text.
	Summary(tasks []HeartbeatTask) string
}

// HeartbeatBoard owns the heartbeat callback namespace.
type HeartbeatBoard struct {
	t       *TelegramChannel
	backend HeartbeatBackend
}

// NewHeartbeatBoard wires the heartbeat namespace onto the channel. Call
// before Start.
func (t *TelegramChannel) NewHeartbeatBoard(b HeartbeatBackend) (*HeartbeatBoard, error) {
	if b == nil {
		return nil, fmt.Errorf("heartbeat backend is nil")
	}
	hb := &HeartbeatBoard{t: t, backend: b}
	if err := t.RegisterCallback(HeartbeatBoardNamespace, hb.handlePress); err != nil {
		return nil, err
	}
	return hb, nil
}

func isBareHeartbeat(content string) bool {
	fields := strings.Fields(content)
	return len(fields) == 1 && strings.EqualFold(strings.TrimPrefix(fields[0], "/"), "heartbeat")
}

// Keyboard returns the toggle keyboard for a bare /heartbeat reply on this
// channel, nil for anything else or when there are no tasks.
func (hb *HeartbeatBoard) Keyboard(_ context.Context, msg bus.InboundMessage) *Keyboard {
	if hb == nil || msg.Channel != hb.t.name || !isBareHeartbeat(msg.Content) {
		return nil
	}
	tasks, err := hb.backend.Tasks()
	if err != nil {
		log.Warn("heartbeat board: tasks unavailable", "error", err)
		return nil
	}
	return heartbeatKeyboard(tasks)
}

// heartbeatKeyboard draws one button per task, one column. Each button's
// label carries the state it shows (☐ open, ✅ done), and its payload names
// the line, the task's key and that state — so a press says exactly which
// task it saw and in which state, and a press drawn from an older file (or
// the second callback of a double tap) is refused rather than flipping
// whatever now sits on that line, or flipping the task back.
func heartbeatKeyboard(tasks []HeartbeatTask) *Keyboard {
	kb := &Keyboard{}
	for _, task := range tasks {
		if len(kb.Rows) == heartbeatMaxButtons {
			break
		}
		box, state := "☐ ", "0"
		if task.Done {
			box, state = "✅ ", "1"
		}
		b := ActionButton(box+truncateRunes(task.Text, heartbeatLabelRunes), HeartbeatBoardNamespace,
			heartbeatToggleAction, strconv.Itoa(task.Line)+":"+task.Key+":"+state)
		if _, err := b.Action.Encode(); err != nil {
			log.Warn("heartbeat task left off the keyboard", "line", task.Line, "error", err)
			continue
		}
		kb.Row(b)
	}
	if len(kb.Rows) == 0 {
		return nil
	}
	return kb
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// parseHeartbeatPayload splits "<line>:<key>:<0|1>", the last field being
// the state the button showed (1 = done).
func parseHeartbeatPayload(p string) (line int, key string, shownDone bool, ok bool) {
	parts := strings.Split(p, ":")
	if len(parts) != 3 || parts[1] == "" || (parts[2] != "0" && parts[2] != "1") {
		return 0, "", false, false
	}
	line, err := strconv.Atoi(parts[0])
	if err != nil || line < 0 {
		return 0, "", false, false
	}
	return line, parts[1], parts[2] == "1", true
}

// handlePress toggles one task and edits the message in place with the new
// list and keyboard. A stale press re-renders the current list instead.
func (hb *HeartbeatBoard) handlePress(_ context.Context, press CallbackPress) error {
	if press.Action.Action != heartbeatToggleAction {
		return nil
	}
	line, key, shownDone, ok := parseHeartbeatPayload(press.Action.Payload)
	if !ok {
		return nil
	}
	editor := hb.t.currentEditor()
	if editor == nil {
		return fmt.Errorf("heartbeat board: channel not connected")
	}
	tasks, text, err := hb.backend.Toggle(line, key, shownDone)
	if err != nil {
		note := heartbeatFailed
		if errors.Is(err, ErrHeartbeatTaskChanged) {
			note = heartbeatChanged
		} else {
			log.Warn("heartbeat board: toggle failed", "error", err)
		}
		// Show the file as it is now, so the next press is drawn from it.
		cur, lerr := hb.backend.Tasks()
		if lerr != nil {
			tasks, text = nil, note
		} else {
			tasks, text = cur, note+"\n\n"+hb.backend.Summary(cur)
		}
	}
	opts := &telebot.SendOptions{}
	if kb := heartbeatKeyboard(tasks); kb != nil {
		if rm, berr := kb.Build(); berr == nil {
			opts.ReplyMarkup = rm
		}
	}
	_, err = editMarkdown(editor, pickerTarget{chatID: press.ChatID, messageID: press.MessageID}, text, opts)
	return err
}
