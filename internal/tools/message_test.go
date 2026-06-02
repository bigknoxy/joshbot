package tools

import (
	"context"
	"errors"
	"testing"
)

// mockSender implements MessageSender for testing.
type mockSender struct {
	sendFn func(ctx context.Context, channel, content string) error
}

func (m *mockSender) SendMessage(ctx context.Context, channel, content string) error {
	if m.sendFn != nil {
		return m.sendFn(ctx, channel, content)
	}
	return nil
}

func TestMessageTool_Name(t *testing.T) {
	tool := NewMessageTool(nil)
	if got := tool.Name(); got != "message" {
		t.Errorf("Name() = %q, want %q", got, "message")
	}
}

func TestMessageTool_Description(t *testing.T) {
	tool := NewMessageTool(nil)
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() should not be empty")
	}
}

func TestMessageTool_Parameters(t *testing.T) {
	tool := NewMessageTool(nil)
	params := tool.Parameters()

	if len(params) != 2 {
		t.Fatalf("expected 2 parameters, got %d", len(params))
	}

	if params[0].Name != "channel" {
		t.Errorf("first parameter name = %q, want %q", params[0].Name, "channel")
	}
	if params[0].Type != ParamString {
		t.Errorf("channel type = %v, want %v", params[0].Type, ParamString)
	}
	if params[0].Required {
		t.Error("channel should not be required (default 'same')")
	}

	if params[1].Name != "content" {
		t.Errorf("second parameter name = %q, want %q", params[1].Name, "content")
	}
	if params[1].Type != ParamString {
		t.Errorf("content type = %v, want %v", params[1].Type, ParamString)
	}
	if !params[1].Required {
		t.Error("content should be required")
	}
}

func TestMessageTool_Execute_NilSender(t *testing.T) {
	tool := NewMessageTool(nil)
	result := tool.Execute(nil, map[string]any{
		"content": "hello",
	})

	if result.Error == nil {
		t.Fatal("expected error for nil sender")
	}
	if result.Error.Error() != "message sender not configured" {
		t.Errorf("unexpected error: %v", result.Error)
	}
}

func TestMessageTool_Execute_EmptyContent(t *testing.T) {
	sender := &mockSender{}
	tool := NewMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"content": "",
	})

	if result.Error == nil {
		t.Fatal("expected error for empty content")
	}
	if result.Error.Error() != "content is required" {
		t.Errorf("unexpected error: %v", result.Error)
	}
}

func TestMessageTool_Execute_MissingContent(t *testing.T) {
	sender := &mockSender{}
	tool := NewMessageTool(sender)

	result := tool.Execute(nil, map[string]any{})

	if result.Error == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestMessageTool_Execute_Success(t *testing.T) {
	var capturedChannel, capturedContent string
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			capturedChannel = channel
			capturedContent = content
			return nil
		},
	}
	tool := NewMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"channel": "telegram",
		"content": "Hello from agent!",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Output != "Message sent to telegram" {
		t.Errorf("expected 'Message sent to telegram', got %q", result.Output)
	}
	if capturedChannel != "telegram" {
		t.Errorf("expected channel 'telegram', got %q", capturedChannel)
	}
	if capturedContent != "Hello from agent!" {
		t.Errorf("expected content 'Hello from agent!', got %q", capturedContent)
	}
}

func TestMessageTool_Execute_DefaultChannel(t *testing.T) {
	var capturedChannel string
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			capturedChannel = channel
			return nil
		},
	}
	tool := NewMessageTool(sender)

	// No channel specified, should default to "cli"
	result := tool.Execute(nil, map[string]any{
		"content": "test",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if capturedChannel != "cli" {
		t.Errorf("expected default channel 'cli', got %q", capturedChannel)
	}
}

func TestMessageTool_Execute_EmptyChannelDefaultsToCli(t *testing.T) {
	var capturedChannel string
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			capturedChannel = channel
			return nil
		},
	}
	tool := NewMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"channel": "",
		"content": "test",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if capturedChannel != "cli" {
		t.Errorf("expected default channel 'cli', got %q", capturedChannel)
	}
}

func TestMessageTool_Execute_SendError(t *testing.T) {
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			return errors.New("network error")
		},
	}
	tool := NewMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"content": "test",
	})

	if result.Error == nil {
		t.Fatal("expected error from send failure")
	}
	if result.Error.Error() != "failed to send message: network error" {
		t.Errorf("unexpected error: %v", result.Error)
	}
}

func TestMessageTool_Execute_WithContext(t *testing.T) {
	var capturedCtx context.Context
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			capturedCtx = ctx
			return nil
		},
	}
	tool := NewMessageTool(sender)

	ctx := context.Background()
	result := tool.Execute(ctx, map[string]any{
		"content": "test",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if capturedCtx == nil {
		t.Error("expected context to be passed through")
	}
}

func TestMessageTool_SetSender(t *testing.T) {
	sender1 := &mockSender{}
	sender2 := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			return nil
		},
	}

	tool := NewMessageTool(sender1)

	// Initially sender1 works
	result := tool.Execute(nil, map[string]any{"content": "test"})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Replace sender
	tool.SetSender(sender2)
	result = tool.Execute(nil, map[string]any{"content": "test"})
	if result.Error != nil {
		t.Fatalf("unexpected error after SetSender: %v", result.Error)
	}
}

// --- ChannelMessageTool tests ---

func TestChannelMessageTool_Name(t *testing.T) {
	tool := NewChannelMessageTool(nil)
	if got := tool.Name(); got != "channel" {
		t.Errorf("Name() = %q, want %q", got, "channel")
	}
}

func TestChannelMessageTool_Description(t *testing.T) {
	tool := NewChannelMessageTool(nil)
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() should not be empty")
	}
}

func TestChannelMessageTool_Parameters(t *testing.T) {
	tool := NewChannelMessageTool(nil)
	params := tool.Parameters()

	if len(params) != 4 {
		t.Fatalf("expected 4 parameters, got %d", len(params))
	}

	if params[0].Name != "operation" {
		t.Errorf("first parameter name = %q, want %q", params[0].Name, "operation")
	}
	if params[0].Type != ParamString {
		t.Errorf("operation type = %v, want %v", params[0].Type, ParamString)
	}
	if !params[0].Required {
		t.Error("operation should be required")
	}
	if len(params[0].Enum) != 1 || params[0].Enum[0] != "send_message" {
		t.Errorf("operation enum = %v, want [send_message]", params[0].Enum)
	}

	if params[1].Name != "channel" {
		t.Errorf("second parameter name = %q, want %q", params[1].Name, "channel")
	}
	if params[1].Type != ParamString {
		t.Errorf("channel type = %v, want %v", params[1].Type, ParamString)
	}

	if params[2].Name != "content" {
		t.Errorf("third parameter name = %q, want %q", params[2].Name, "content")
	}
	if params[2].Type != ParamString {
		t.Errorf("content type = %v, want %v", params[2].Type, ParamString)
	}
	if !params[2].Required {
		t.Error("content should be required")
	}

	if params[3].Name != "metadata" {
		t.Errorf("fourth parameter name = %q, want %q", params[3].Name, "metadata")
	}
	if params[3].Type != ParamObject {
		t.Errorf("metadata type = %v, want %v", params[3].Type, ParamObject)
	}
}

func TestChannelMessageTool_Execute_NilSender(t *testing.T) {
	tool := NewChannelMessageTool(nil)
	result := tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"content":   "hello",
	})

	if result.Error == nil {
		t.Fatal("expected error for nil sender")
	}
	if result.Error.Error() != "message sender not configured" {
		t.Errorf("unexpected error: %v", result.Error)
	}
}

func TestChannelMessageTool_Execute_EmptyContent(t *testing.T) {
	sender := &mockSender{}
	tool := NewChannelMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"content":   "",
	})

	if result.Error == nil {
		t.Fatal("expected error for empty content")
	}
	if result.Error.Error() != "content is required" {
		t.Errorf("unexpected error: %v", result.Error)
	}
}

func TestChannelMessageTool_Execute_UnknownOperation(t *testing.T) {
	sender := &mockSender{}
	tool := NewChannelMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"operation": "invalid_op",
		"content":   "hello",
	})

	if result.Error == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestChannelMessageTool_Execute_SendMessage(t *testing.T) {
	var capturedChannel, capturedContent string
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			capturedChannel = channel
			capturedContent = content
			return nil
		},
	}
	tool := NewChannelMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"channel":   "telegram",
		"content":   "Hello via channel tool!",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if capturedChannel != "telegram" {
		t.Errorf("expected channel 'telegram', got %q", capturedChannel)
	}
	if capturedContent != "Hello via channel tool!" {
		t.Errorf("expected content 'Hello via channel tool!', got %q", capturedContent)
	}
	if result.Output != "Sent to: telegram" {
		t.Errorf("expected 'Sent to: telegram', got %q", result.Output)
	}
}

func TestChannelMessageTool_Execute_DefaultChannel(t *testing.T) {
	var capturedChannel string
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			capturedChannel = channel
			return nil
		},
	}
	tool := NewChannelMessageTool(sender)

	// No channel specified, should default to "cli"
	result := tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"content":   "test",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if capturedChannel != "cli" {
		t.Errorf("expected default channel 'cli', got %q", capturedChannel)
	}
}

func TestChannelMessageTool_Execute_SendToAll(t *testing.T) {
	var sentChannels []string
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			sentChannels = append(sentChannels, channel)
			return nil
		},
	}
	tool := NewChannelMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"channel":   "all",
		"content":   "broadcast message",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if len(sentChannels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(sentChannels))
	}
	if sentChannels[0] != "telegram" {
		t.Errorf("expected first channel 'telegram', got %q", sentChannels[0])
	}
	if sentChannels[1] != "cli" {
		t.Errorf("expected second channel 'cli', got %q", sentChannels[1])
	}
}

func TestChannelMessageTool_Execute_PartialFailure(t *testing.T) {
	var sentChannels []string
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			sentChannels = append(sentChannels, channel)
			if channel == "telegram" {
				return errors.New("telegram unavailable")
			}
			return nil
		},
	}
	tool := NewChannelMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"channel":   "all",
		"content":   "partial test",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Output != "Sent to: cli\nFailed: telegram: telegram unavailable" {
		t.Errorf("unexpected output: %q", result.Output)
	}
}

func TestChannelMessageTool_Execute_AllFail(t *testing.T) {
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			return errors.New("all down")
		},
	}
	tool := NewChannelMessageTool(sender)

	result := tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"channel":   "all",
		"content":   "fail test",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Output != "Failed: telegram: all down, cli: all down" {
		t.Errorf("unexpected output: %q", result.Output)
	}
}

func TestChannelMessageTool_Execute_WithContext(t *testing.T) {
	var capturedCtx context.Context
	sender := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			capturedCtx = ctx
			return nil
		},
	}
	tool := NewChannelMessageTool(sender)

	ctx := context.Background()
	result := tool.Execute(ctx, map[string]any{
		"operation": "send_message",
		"content":   "test",
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if capturedCtx == nil {
		t.Error("expected context to be passed through")
	}
}

func TestChannelMessageTool_SetSender(t *testing.T) {
	sender1 := &mockSender{}
	sender2 := &mockSender{
		sendFn: func(ctx context.Context, channel, content string) error {
			return nil
		},
	}

	tool := NewChannelMessageTool(sender1)

	// Initially sender1 works
	result := tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"content":   "test",
	})
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// Replace sender
	tool.SetSender(sender2)
	result = tool.Execute(nil, map[string]any{
		"operation": "send_message",
		"content":   "test",
	})
	if result.Error != nil {
		t.Fatalf("unexpected error after SetSender: %v", result.Error)
	}
}
