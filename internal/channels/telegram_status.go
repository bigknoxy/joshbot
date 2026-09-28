package channels

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
	"gopkg.in/telebot.v3"
)

// Status panel (#318). The TUI has a live status line; Telegram has no honest
// equivalent — a self-updating message burns the per-chat edit budget for
// nothing. So a bare /status reply carries a manual [🔃 Refresh] button and a
// [📌 Pin] button, and the text itself carries an "As of" stamp (written by the
// agent), so a pinned render never pretends to be current.
const (
	StatusPanelNamespace = "status"
	statusRefreshAction  = "refresh"
	statusPinAction      = "pin"
	// statusPinnedPayload rides the refresh button once the message is
	// pinned, so the keyboard itself remembers the pin and the Pin button is
	// not offered again — no store read or write on the press path.
	statusPinnedPayload = "pinned"
	// statusTurnTimeout bounds the /status command turn a press runs. It is
	// a session read, not an LLM call, but it queues behind the per-key
	// session lock like any turn.
	statusTurnTimeout = 30 * time.Second
	// statusStalePrefix marks a render known to be outdated: a refresh was
	// asked for and failed, so the old text stays but stops claiming to be
	// the latest word.
	statusStalePrefix = "⚠️ Outdated — refresh failed; showing the last status."
	statusPinFailed   = "📌 Could not pin — in a group the bot needs permission to pin messages."
)

// StatusBackend runs the /status command turn a press refreshes with. It is
// satisfied by the same cmd/joshbot adapter over *agent.Agent as the pickers.
type StatusBackend interface {
	Process(ctx context.Context, msg bus.InboundMessage) (string, error)
}

// StatusPanel owns the status callback namespace.
type StatusPanel struct {
	t       *TelegramChannel
	backend StatusBackend
}

// telegramPinner is the one call the panel needs beyond telegramEditor.
// *telebot.Bot satisfies it; it is asserted rather than added to
// telegramEditor so the streamer's fakes are not forced to implement it.
type telegramPinner interface {
	Pin(msg telebot.Editable, opts ...interface{}) error
}

// NewStatusPanel wires the status namespace onto the channel. Call before Start.
func (t *TelegramChannel) NewStatusPanel(b StatusBackend) (*StatusPanel, error) {
	if b == nil {
		return nil, fmt.Errorf("status backend is nil")
	}
	sp := &StatusPanel{t: t, backend: b}
	if err := t.RegisterCallback(StatusPanelNamespace, sp.handlePress); err != nil {
		return nil, err
	}
	return sp, nil
}

// isBareStatus reports whether content is `/status` with no argument.
func isBareStatus(content string) bool {
	fields := strings.Fields(content)
	return len(fields) == 1 && strings.EqualFold(strings.TrimPrefix(fields[0], "/"), "status")
}

// Keyboard returns the panel keyboard for a bare /status reply on this
// channel, nil for anything else. Called by the gateway after the command
// turn produced its text.
func (sp *StatusPanel) Keyboard(_ context.Context, msg bus.InboundMessage) *Keyboard {
	if sp == nil || msg.Channel != sp.t.name || !isBareStatus(msg.Content) {
		return nil
	}
	return statusKeyboard(false)
}

func statusKeyboard(pinned bool) *Keyboard {
	if pinned {
		return (&Keyboard{}).Row(ActionButton("🔃 Refresh", StatusPanelNamespace, statusRefreshAction, statusPinnedPayload))
	}
	return (&Keyboard{}).Row(
		ActionButton("🔃 Refresh", StatusPanelNamespace, statusRefreshAction, ""),
		ActionButton("📌 Pin", StatusPanelNamespace, statusPinAction, ""),
	)
}

// handlePress refreshes (and for Pin, first pins) the status message in
// place. The /status turn runs for the presser's own session, exactly like a
// picker press, so in a group each member's refresh shows their own model.
func (sp *StatusPanel) handlePress(ctx context.Context, press CallbackPress) error {
	var pinned bool
	switch press.Action.Action {
	case statusRefreshAction:
		pinned = press.Action.Payload == statusPinnedPayload
	case statusPinAction:
	default:
		return nil
	}
	editor := sp.t.currentEditor()
	if editor == nil {
		return fmt.Errorf("status panel: channel not connected")
	}
	target := pickerTarget{chatID: press.ChatID, messageID: press.MessageID}

	var note string
	if press.Action.Action == statusPinAction {
		pinner, ok := editor.(telegramPinner)
		var err error
		if !ok {
			err = fmt.Errorf("editor cannot pin")
		} else {
			// Silent: a pin notification to every member for a status
			// snapshot is noise.
			err = pinner.Pin(target, telebot.Silent)
		}
		if err != nil {
			log.Warn("status panel: pin failed", "chat", press.ChatID, "error", err)
			note = statusPinFailed
		} else {
			pinned = true
		}
	}

	tctx, cancel := context.WithTimeout(ctx, statusTurnTimeout)
	defer cancel()
	reply, err := sp.backend.Process(tctx, bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", press.SenderID),
		Content:   "/status",
		Channel:   sp.t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id": press.MessageID,
			"chat_id":    press.ChatID,
			"username":   press.Username,
			"is_command": true,
		},
	})
	if err != nil || strings.HasPrefix(reply, "Error") {
		// Keep the last good render and say it is outdated, rather than
		// replacing it with an error or pretending it is current. The raw
		// error stays in the log: it can wrap provider or path detail.
		log.Warn("status panel: refresh failed", "error", err, "reply_is_error", err == nil)
		reply = statusStalePrefix
		if old := stripStatusNotes(press.MessageText); old != "" {
			reply += "\n\n" + old
		}
	}
	if note != "" {
		reply = note + "\n\n" + reply
	}

	opts := &telebot.SendOptions{}
	if rm, berr := statusKeyboard(pinned).Build(); berr == nil {
		opts.ReplyMarkup = rm
	}
	_, err = editor.Edit(target, reply, opts)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		// Two presses inside the same minute render identical text; that is
		// a successful refresh, not a failure.
		return nil
	}
	return err
}

// stripStatusNotes removes the panel's own leading notes from a previous
// render, so repeated failures do not stack warning on warning.
func stripStatusNotes(text string) string {
	text = strings.TrimSpace(text)
	for _, p := range []string{statusPinFailed, statusStalePrefix} {
		text = strings.TrimSpace(strings.TrimPrefix(text, p))
	}
	return text
}
