package channels

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
	"gopkg.in/telebot.v3"
)

// Picker namespaces. A press is routed by namespace (see callback.go), so
// each picker owns one and RegisterCallback refuses a second claimant.
const (
	ModelPickerNamespace       = "model"
	PersonalityPickerNamespace = "persona"
	pickerPickAction           = "pick"
	// pickerTurnTimeout bounds the command turn a press runs. A /model or
	// /personality turn is a session read-modify-write, not an LLM call, but
	// it queues behind the per-key session lock like any turn.
	pickerTurnTimeout = 30 * time.Second
	// pickerButtonsPerRow is two: a phone screen fits two labels of the
	// "provider · model" shape; three truncates.
	pickerButtonsPerRow = 2
)

// PickerChoice is one button of a picker. It mirrors agent.Choice without
// importing it: channels must not depend on the agent package.
type PickerChoice struct {
	Spec    string
	Label   string
	Current bool
}

// PickerBackend is what a press needs from the agent: the choices to draw,
// and the command turn that applies one. It is satisfied by an adapter in
// cmd/joshbot over *agent.Agent.
type PickerBackend interface {
	ModelChoices(ctx context.Context, msg bus.InboundMessage) ([]PickerChoice, error)
	PersonalityChoices(ctx context.Context, msg bus.InboundMessage) ([]PickerChoice, error)
	Process(ctx context.Context, msg bus.InboundMessage) (string, error)
}

// Picker renders /model and /personality as inline keyboards and applies a
// press by running the same command turn a typed `/model <spec>` runs, so
// the button cannot do anything the command would refuse. The reply to the
// press edits the picker message in place — the ✅ moves — rather than
// posting a new message under it.
type Picker struct {
	t       *TelegramChannel
	backend PickerBackend
}

// NewPicker wires both picker namespaces onto the channel. Call before Start.
func (t *TelegramChannel) NewPicker(b PickerBackend) (*Picker, error) {
	if b == nil {
		return nil, fmt.Errorf("picker backend is nil")
	}
	p := &Picker{t: t, backend: b}
	if err := t.RegisterCallback(ModelPickerNamespace, p.handleModelPress); err != nil {
		return nil, err
	}
	if err := t.RegisterCallback(PersonalityPickerNamespace, p.handlePersonalityPress); err != nil {
		return nil, err
	}
	return p, nil
}

// pickerCommand reports which picker, if any, a bare command invokes: `/model`
// and `/personality` with no argument. Anything with an argument is a switch,
// not a listing, and gets no keyboard.
func pickerCommand(content string) string {
	fields := strings.Fields(content)
	if len(fields) != 1 {
		return ""
	}
	switch strings.ToLower(strings.TrimPrefix(fields[0], "/")) {
	case "model":
		return ModelPickerNamespace
	case "personality":
		return PersonalityPickerNamespace
	}
	return ""
}

// Keyboard returns the inline keyboard to attach to the reply for msg, or nil
// when msg is not a bare picker command on this channel. It is called by the
// gateway after the command turn has produced its text reply.
func (p *Picker) Keyboard(ctx context.Context, msg bus.InboundMessage) *Keyboard {
	if p == nil || msg.Channel != p.t.name {
		return nil
	}
	ns := pickerCommand(msg.Content)
	if ns == "" {
		return nil
	}
	choices, err := p.choices(ctx, ns, msg)
	if err != nil {
		log.Warn("picker choices unavailable", "namespace", ns, "error", err)
		return nil
	}
	return pickerKeyboard(ns, choices)
}

func (p *Picker) choices(ctx context.Context, ns string, msg bus.InboundMessage) ([]PickerChoice, error) {
	if ns == PersonalityPickerNamespace {
		return p.backend.PersonalityChoices(ctx, msg)
	}
	return p.backend.ModelChoices(ctx, msg)
}

// pickerKeyboard lays the choices out two per row, the current one marked.
// A spec too long for callback_data (64 bytes with the envelope) is left off
// rather than truncated: Build would refuse the whole keyboard for one bad
// button, and a truncated spec could decode as a different, valid choice.
func pickerKeyboard(ns string, choices []PickerChoice) *Keyboard {
	kb := &Keyboard{}
	var row []InlineButton
	for _, c := range choices {
		label := c.Label
		if c.Current {
			label = "✅ " + label
		}
		b := ActionButton(label, ns, pickerPickAction, c.Spec)
		if _, err := b.Action.Encode(); err != nil {
			log.Warn("picker choice left off the keyboard; type the command instead", "namespace", ns, "spec", c.Spec, "error", err)
			continue
		}
		row = append(row, b)
		if len(row) == pickerButtonsPerRow {
			kb.Row(row...)
			row = nil
		}
	}
	if len(row) > 0 {
		kb.Row(row...)
	}
	if len(kb.Rows) == 0 {
		return nil
	}
	return kb
}

func (p *Picker) handleModelPress(ctx context.Context, press CallbackPress) error {
	return p.handlePress(ctx, ModelPickerNamespace, "/model ", press)
}

func (p *Picker) handlePersonalityPress(ctx context.Context, press CallbackPress) error {
	return p.handlePress(ctx, PersonalityPickerNamespace, "/personality ", press)
}

// handlePress applies a choice by running the command turn for the presser's
// own session — the synthesized inbound carries the presser's sender id, so
// in a group each member switches their own session, never the chat's — and
// edits the picker message with the reply and a refreshed keyboard.
func (p *Picker) handlePress(ctx context.Context, ns, command string, press CallbackPress) error {
	if press.Action.Action != pickerPickAction || press.Action.Payload == "" {
		return nil
	}
	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", press.SenderID),
		Content:   command + press.Action.Payload,
		Channel:   p.t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id": press.MessageID,
			"chat_id":    press.ChatID,
			"username":   press.Username,
			"is_command": true,
		},
	}
	tctx, cancel := context.WithTimeout(ctx, pickerTurnTimeout)
	defer cancel()
	reply, err := p.backend.Process(tctx, inbound)
	failed := err != nil
	if failed {
		// The raw error stays in the log: a Process failure can wrap
		// provider or path detail, and the gateway path redacts those
		// before they reach a chat. The typed command is the recovery.
		log.Warn("picker command turn failed", "namespace", ns, "error", err)
		reply = "Could not apply that choice. Try typing the command instead."
	} else if strings.HasPrefix(reply, "Error") {
		// An in-band failure ("Error: unknown model", ReplyPrefix): the
		// text goes to the user as the command would send it.
		failed = true
	}
	editor := p.t.currentEditor()
	if editor == nil {
		return fmt.Errorf("picker: channel not connected")
	}
	opts := &telebot.SendOptions{}
	// No keyboard under a failure — the same rule the gateway applies to the
	// command reply: a picker under an error invites a press into it.
	if choices, cerr := p.choices(tctx, ns, inbound); !failed && cerr == nil {
		if kb := pickerKeyboard(ns, choices); kb != nil {
			if rm, berr := kb.Build(); berr == nil {
				opts.ReplyMarkup = rm
			}
		}
	}
	_, err = editor.Edit(pickerTarget{chatID: press.ChatID, messageID: press.MessageID}, reply, opts)
	return err
}

// pickerTarget is the message a press edits. telebot's Edit reads both halves
// of MessageSig, unlike React (see ackTarget), so the chat id is carried.
type pickerTarget struct {
	chatID    int64
	messageID int
}

func (p pickerTarget) MessageSig() (string, int64) {
	return strconv.Itoa(p.messageID), p.chatID
}
