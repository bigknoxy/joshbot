package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// Attachment is one outbound file. It is an alias for bus.Attachment rather
// than a second type, so a file crosses the tool boundary and the bus boundary
// as the same value with no lossy conversion in between.
type Attachment = bus.Attachment

// MessageSender is an interface for sending messages.
//
// SendFile is a separate method rather than an extra argument on SendMessage
// because the two have different failure modes and different limits, and every
// existing caller of SendMessage must keep compiling unchanged. A caption-less
// file send passes an empty att.Caption equivalent via content.
type MessageSender interface {
	SendMessage(ctx context.Context, channel, content string) error
	SendFile(ctx context.Context, channel string, att Attachment, caption string) error
}

// MessageTool allows the agent to send messages to channels.
type MessageTool struct {
	sender MessageSender
}

// NewMessageTool creates a new MessageTool.
func NewMessageTool(sender MessageSender) *MessageTool {
	return &MessageTool{
		sender: sender,
	}
}

// Name returns the name of the tool.
func (t *MessageTool) Name() string {
	return "message"
}

// Description returns a description of the tool.
func (t *MessageTool) Description() string {
	return `Send a message to a different channel (e.g., Telegram). Not for responding to current conversation - return response as text instead.`
}

// Parameters returns the parameters for the tool.
func (t *MessageTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "channel",
			Type:        ParamString,
			Description: "Target channel: telegram, cli, or same",
			Required:    false,
			Default:     "same",
		},
		{
			Name:        "content",
			Type:        ParamString,
			Description: "Message content to send",
			Required:    true,
		},
	}
}

// Execute sends a message.
func (t *MessageTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.sender == nil {
		return ToolResult{Error: errors.New("message sender not configured")}
	}

	content, _ := args["content"].(string)
	if content == "" {
		return ToolResult{Error: errors.New("content is required")}
	}

	channel, _ := args["channel"].(string)
	if channel == "" || channel == "same" {
		// Try to get channel from context
		channel = "cli" // Default to CLI
	}

	// Get context for timeout
	var execCtx context.Context
	var ok bool
	if ctx != nil {
		execCtx, ok = ctx.(context.Context)
	}
	if !ok || execCtx == nil {
		execCtx = context.Background()
	}

	err := t.sender.SendMessage(execCtx, channel, content)
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to send message: %w", err)}
	}

	return ToolResult{Output: fmt.Sprintf("Message sent to %s", channel)}
}

// SetSender sets the message sender.
func (t *MessageTool) SetSender(sender MessageSender) {
	t.sender = sender
}

// ChannelMessageTool provides tools for working with channels in messages.
type ChannelMessageTool struct {
	sender MessageSender
}

// NewChannelMessageTool creates a new ChannelMessageTool.
func NewChannelMessageTool(sender MessageSender) *ChannelMessageTool {
	return &ChannelMessageTool{
		sender: sender,
	}
}

// Name returns the name of the tool.
func (t *ChannelMessageTool) Name() string {
	return "channel"
}

// Description returns a description of the tool.
func (t *ChannelMessageTool) Description() string {
	return `Send a message to a different channel (e.g., Telegram). Not for responding to current conversation - return response as text instead.`
}

// Parameters returns the parameters for the tool.
func (t *ChannelMessageTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "operation",
			Type:        ParamString,
			Description: "Operation: send_message",
			Required:    true,
			Enum:        []string{"send_message"},
		},
		{
			Name:        "channel",
			Type:        ParamString,
			Description: "Target channel: 'telegram', 'cli', or 'all'",
			Required:    false,
			Default:     "cli",
			Enum:        []string{"telegram", "cli", "all"},
		},
		{
			Name:        "content",
			Type:        ParamString,
			Description: "Message content to send",
			Required:    true,
		},
		{
			Name:        "metadata",
			Type:        ParamObject,
			Description: "Optional metadata (e.g., parse_mode)",
			Required:    false,
		},
	}
}

// Execute runs the channel operation.
func (t *ChannelMessageTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.sender == nil {
		return ToolResult{Error: errors.New("message sender not configured")}
	}

	operation, _ := args["operation"].(string)
	content, _ := args["content"].(string)
	channel, _ := args["channel"].(string)

	if content == "" {
		return ToolResult{Error: errors.New("content is required")}
	}

	if channel == "" {
		channel = "cli"
	}

	// Get context
	var execCtx context.Context
	var ok bool
	if ctx != nil {
		execCtx, ok = ctx.(context.Context)
	}
	if !ok || execCtx == nil {
		execCtx = context.Background()
	}

	switch operation {
	case "send_message":
		channels := []string{channel}
		if channel == "all" {
			channels = []string{"telegram", "cli"}
		}

		var sent []string
		var failed []string

		for _, ch := range channels {
			err := t.sender.SendMessage(execCtx, ch, content)
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", ch, err))
			} else {
				sent = append(sent, ch)
			}
		}

		var result strings.Builder
		if len(sent) > 0 {
			result.WriteString(fmt.Sprintf("Sent to: %s", strings.Join(sent, ", ")))
		}
		if len(failed) > 0 {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(fmt.Sprintf("Failed: %s", strings.Join(failed, ", ")))
		}

		return ToolResult{Output: result.String()}

	default:
		return ToolResult{Error: fmt.Errorf("unknown operation: %s", operation)}
	}
}

// SetSender sets the message sender.
func (t *ChannelMessageTool) SetSender(sender MessageSender) {
	t.sender = sender
}
