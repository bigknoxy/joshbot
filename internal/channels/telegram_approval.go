package channels

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	telebot "gopkg.in/telebot.v3"

	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// Shell approval over Telegram (issue #311).
//
// tools.shell_approval used to be impossible to satisfy from Telegram: only
// the interactive TUI installed an approver, so every gated command on a
// gateway turn was denied immediately by DenyAll. This renders the approval
// as a message carrying the exact command with an inline keyboard beneath it,
// and blocks the turn on the button press, bounded by the turn's own context.
//
// Rules that carry over from the TUI approver and the gate's design:
//
//   - The command is shown whole and unabridged, as plain text — the
//     arguments are the dangerous part, and parse-mode rendering could
//     reflow or swallow characters of an arbitrary command string.
//   - Deny is the answer to everything that is not an explicit Allow press:
//     a timeout, a cancelled turn, an unknown action, a send failure.
//   - "Allow all" is offered only under shell_approval="interactive"
//     (ApprovalMode.RemembersSession), exactly like the TUI's [a]ll answer,
//     and lasts until the process exits.
//   - Only the chat the request was sent to may answer it: a button press is
//     matched to its pending request by id *and* chat, so a forwarded
//     approval message answered elsewhere is ignored. The allowlist check in
//     handleCallback runs before the press ever reaches this code.

// ApprovalCallbackNamespace is the callback namespace approval buttons
// dispatch on.
const ApprovalCallbackNamespace = "approve"

// approvalAnswer is one button press routed back to a waiting Approve call.
type approvalAnswer struct {
	decision tools.Decision
	// all marks an "allow all for this session" answer.
	all bool
}

// pendingApproval is one Approve call waiting for its press.
type pendingApproval struct {
	chatID int64
	answer chan approvalAnswer // capacity 1; only the first press lands
}

// ShellApprovalCoordinator owns the pending-approval table and the callback
// registration for one Telegram channel. One coordinator serves every turn;
// per-turn approvers bound to a chat come from ApproverFor.
type ShellApprovalCoordinator struct {
	ch       *TelegramChannel
	remember bool

	mu          sync.Mutex
	nextID      int64
	pending     map[string]*pendingApproval
	approvedAll bool
}

// NewShellApprovalCoordinator builds the coordinator and claims the
// "approve" callback namespace. Call once, at wiring time, before Start.
func (t *TelegramChannel) NewShellApprovalCoordinator(mode tools.ApprovalMode) (*ShellApprovalCoordinator, error) {
	c := &ShellApprovalCoordinator{
		ch:       t,
		remember: mode.RemembersSession(),
		pending:  make(map[string]*pendingApproval),
	}
	if err := t.RegisterCallback(ApprovalCallbackNamespace, c.handlePress); err != nil {
		return nil, err
	}
	return c, nil
}

// ApproverFor returns an approver bound to one chat, or nil when the id is
// not a usable Telegram chat id. A nil return means the caller installs no
// approver and the gate denies — never blocks — exactly as before.
func (c *ShellApprovalCoordinator) ApproverFor(channelID string) tools.Approver {
	chatID, err := strconv.ParseInt(channelID, 10, 64)
	if err != nil {
		return nil
	}
	return &telegramApprover{c: c, chatID: chatID}
}

// telegramApprover implements tools.Approver for one chat.
type telegramApprover struct {
	c      *ShellApprovalCoordinator
	chatID int64
}

// Approve implements tools.Approver. It posts the command with an inline
// keyboard and blocks until a button press or ctx expiry. It never returns
// Approve without an explicit press (or a remembered session-wide allow).
func (a *telegramApprover) Approve(ctx context.Context, req tools.ApprovalRequest) (tools.Decision, error) {
	c := a.c

	c.mu.Lock()
	if c.remember && c.approvedAll {
		c.mu.Unlock()
		return tools.Approve, nil
	}
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	p := &pendingApproval{chatID: a.chatID, answer: make(chan approvalAnswer, 1)}
	c.pending[id] = p
	c.mu.Unlock()
	defer c.forget(id)

	editor := c.ch.approvalEditor()
	if editor == nil {
		return tools.Deny, fmt.Errorf("%w: telegram is not connected, cannot ask", tools.ErrDenied)
	}

	kb := &Keyboard{}
	kb.Row(
		ActionButton("✅ Allow", ApprovalCallbackNamespace, "allow", id),
		ActionButton("❌ Deny", ApprovalCallbackNamespace, "deny", id),
	)
	if c.remember {
		kb.Row(ActionButton("🔓 Allow all (this session)", ApprovalCallbackNamespace, "all", id))
	}
	markup, err := kb.Build()
	if err != nil {
		return tools.Deny, fmt.Errorf("%w: could not build approval keyboard: %v", tools.ErrDenied, err)
	}

	// Plain text on purpose: the command string is arbitrary and a parse mode
	// could reflow it or fail the send; the operator must read it verbatim.
	text := fmt.Sprintf("⚠️ %s wants to run:\n\n%s", req.Tool, req.Command)
	if req.WorkingDir != "" {
		text += fmt.Sprintf("\n\nin %s", req.WorkingDir)
	}

	msg, err := editor.Send(telebot.ChatID(a.chatID), text, markup)
	if err != nil {
		return tools.Deny, fmt.Errorf("%w: could not deliver the approval prompt: %v", tools.ErrDenied, err)
	}

	settle := func(outcome string) {
		// Editing without a markup argument drops the keyboard, so a settled
		// prompt cannot be pressed again (a late press would find no pending
		// entry anyway; this makes it visible).
		if _, err := editor.Edit(msg, text+"\n\n"+outcome); err != nil {
			log.Debug("failed to settle approval prompt", "error", err)
		}
	}

	select {
	case <-ctx.Done():
		settle("⏰ no answer — denied")
		return tools.Deny, fmt.Errorf("approval was not answered: %w", ctx.Err())
	case ans := <-p.answer:
		if ans.decision != tools.Approve {
			settle("❌ denied")
			return tools.Deny, nil
		}
		if ans.all && c.remember {
			c.mu.Lock()
			c.approvedAll = true
			c.mu.Unlock()
			settle("🔓 approved — allowing every command for this session")
			return tools.Approve, nil
		}
		settle("✅ approved")
		return tools.Approve, nil
	}
}

// forget removes a pending entry. Deferred by Approve so an abandoned prompt
// cannot leak table entries, whatever path settled it.
func (c *ShellApprovalCoordinator) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// handlePress routes one button press to its waiting Approve call. The
// allowlist has already been enforced by handleCallback; this adds the
// request-identity checks: the id must be pending and the press must come
// from the chat the prompt was sent to.
func (c *ShellApprovalCoordinator) handlePress(_ context.Context, press CallbackPress) error {
	c.mu.Lock()
	p, ok := c.pending[press.Action.Payload]
	if ok {
		// Claim the entry under the lock so exactly one press lands even if
		// two arrive together.
		delete(c.pending, press.Action.Payload)
	}
	c.mu.Unlock()
	if !ok {
		// Stale or replayed press — the prompt was already settled.
		return nil
	}
	if p.chatID != press.ChatID {
		// A forwarded prompt answered from another chat is not an answer.
		// The entry was claimed above; put the denial through so the turn
		// settles rather than waiting for a press that can no longer land.
		log.Warn("approval press from a different chat; denying",
			"expected_chat", p.chatID, "press_chat", press.ChatID)
		p.answer <- approvalAnswer{decision: tools.Deny}
		return nil
	}

	switch press.Action.Action {
	case "allow":
		p.answer <- approvalAnswer{decision: tools.Approve}
	case "all":
		p.answer <- approvalAnswer{decision: tools.Approve, all: true}
	default:
		// "deny" and anything unrecognised: a gate that reads an unknown
		// action as consent is not a gate.
		p.answer <- approvalAnswer{decision: tools.Deny}
	}
	return nil
}

// approvalEditor returns the bot surface approvals send and edit through,
// nil when the channel is not connected.
func (t *TelegramChannel) approvalEditor() telegramEditor {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.currentEditorLocked()
}
