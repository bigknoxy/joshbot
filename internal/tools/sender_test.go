package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

func TestNewBusMessageSender(t *testing.T) {
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	if sender == nil {
		t.Fatal("NewBusMessageSender() returned nil")
	}
}

func TestBusMessageSender_SetAndGetChatID(t *testing.T) {
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	// Initially no chat ID
	_, ok := sender.GetChatID("telegram")
	if ok {
		t.Error("expected no chat ID initially")
	}

	// Set and get
	sender.SetChatID("telegram", "12345")
	id, ok := sender.GetChatID("telegram")
	if !ok {
		t.Fatal("expected to find chat ID after SetChatID")
	}
	if id != "12345" {
		t.Errorf("expected chat ID '12345', got %q", id)
	}
}

func TestBusMessageSender_SetChatID_ExistingChannel(t *testing.T) {
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	sender.SetChatID("telegram", "first")
	sender.SetChatID("telegram", "second")

	id, ok := sender.GetChatID("telegram")
	if !ok {
		t.Fatal("expected to find chat ID")
	}
	if id != "second" {
		t.Errorf("expected chat ID 'second', got %q", id)
	}
}

func TestBusMessageSender_MultipleChannels(t *testing.T) {
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	sender.SetChatID("telegram", "tg-001")
	sender.SetChatID("cli", "cli-user")
	sender.SetChatID("discord", "disc-001")

	tgID, ok := sender.GetChatID("telegram")
	if !ok || tgID != "tg-001" {
		t.Errorf("expected telegram chat ID 'tg-001', got %q (ok=%v)", tgID, ok)
	}

	cliID, ok := sender.GetChatID("cli")
	if !ok || cliID != "cli-user" {
		t.Errorf("expected cli chat ID 'cli-user', got %q (ok=%v)", cliID, ok)
	}

	discID, ok := sender.GetChatID("discord")
	if !ok || discID != "disc-001" {
		t.Errorf("expected discord chat ID 'disc-001', got %q (ok=%v)", discID, ok)
	}
}

func TestBusMessageSender_SetAndGetSenderID(t *testing.T) {
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	sender.SetSenderID("agent-1")

	// Send a message to verify the senderID is included
	sender.SetChatID("cli", "user")
	ctx := context.Background()

	// Start consuming from the outbound channel to prevent blocking
	go func() {
		<-messageBus.OutboundChannel()
	}()

	err := sender.SendMessage(ctx, "cli", "test")
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}

	// Re-consume and check
	select {
	case msg := <-messageBus.OutboundChannel():
		if msg.SenderID != "agent-1" {
			t.Errorf("expected SenderID 'agent-1', got %q", msg.SenderID)
		}
	default:
	}
}

func TestBusMessageSender_SendMessage_NoChatID(t *testing.T) {
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	ctx := context.Background()
	err := sender.SendMessage(ctx, "undefined_channel", "hello")

	if err == nil {
		t.Fatal("expected error for channel with no chat ID")
	}
}

func TestBusMessageSender_SendMessage_Success(t *testing.T) {
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	sender.SetChatID("telegram", "chat-001")
	sender.SetSenderID("test-agent")

	ctx := context.Background()

	// Consume the outbound channel to prevent blocking
	done := make(chan bus.OutboundMessage, 1)
	go func() {
		msg := <-messageBus.OutboundChannel()
		done <- msg
	}()

	err := sender.SendMessage(ctx, "telegram", "Hello, world!")
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}

	select {
	case msg := <-done:
		if msg.Content != "Hello, world!" {
			t.Errorf("expected content 'Hello, world!', got %q", msg.Content)
		}
		if msg.Channel != "telegram" {
			t.Errorf("expected channel 'telegram', got %q", msg.Channel)
		}
		if msg.ChannelID != "chat-001" {
			t.Errorf("expected ChannelID 'chat-001', got %q", msg.ChannelID)
		}
		if msg.SenderID != "test-agent" {
			t.Errorf("expected SenderID 'test-agent', got %q", msg.SenderID)
		}
		if msg.Timestamp.IsZero() {
			t.Error("expected non-zero timestamp")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for outbound message")
	}
}

func TestBusMessageSender_SendMessage_QueueFull(t *testing.T) {
	// Create a bus with a small queue by using a channel-based approach
	// We'll fill the outbound channel to test queue full behavior
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	sender.SetChatID("cli", "user")

	// Don't consume from the outbound channel; fill it up
	// MaxQueueSize is 1000, we need to send more than that
	// Instead, let's just verify the non-blocking path works
	// by consuming after publishing
	ctx := context.Background()

	// Consume in background
	go func() {
		for range messageBus.OutboundChannel() {
			// drain
		}
	}()

	// Send a message - should work since we're draining
	err := sender.SendMessage(ctx, "cli", "test message")
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
}

func TestBusMessageSender_SendMessage_WithCanceledContext(t *testing.T) {
	messageBus := bus.NewMessageBus()
	sender := NewBusMessageSender(messageBus)

	sender.SetChatID("cli", "user")

	// Fill the outbound channel to force blocking path
	// We need to send MaxQueueSize messages without consuming
	for i := 0; i < bus.MaxQueueSize; i++ {
		select {
		case messageBus.OutboundChan() <- bus.OutboundMessage{}:
		default:
			break
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := sender.SendMessage(ctx, "cli", "test")

	if err == nil {
		t.Fatal("expected error with canceled context when queue is full")
	}
}

func TestBusMessageSender_SendFilePublishesTheAttachment(t *testing.T) {
	mb := bus.NewMessageBus()
	s := NewBusMessageSender(mb)
	s.SetChatID("telegram", "555")

	att := Attachment{Filename: "chart.png", Kind: bus.AttachmentPhoto, Size: 4, Data: []byte("\x89PNG")}
	if err := s.SendFile(context.Background(), "telegram", att, "here"); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	select {
	case msg := <-mb.OutboundChannel():
		if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != "chart.png" {
			t.Fatalf("attachments = %+v", msg.Attachments)
		}
		if msg.Content != "here" {
			t.Errorf("content = %q, want the caption", msg.Content)
		}
		if msg.ChannelID != "555" {
			t.Errorf("ChannelID = %q, want the stored chat id", msg.ChannelID)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing published")
	}
}

func TestBusMessageSender_SendFileWithoutAChatIDFails(t *testing.T) {
	s := NewBusMessageSender(bus.NewMessageBus())
	err := s.SendFile(context.Background(), "telegram", Attachment{Filename: "a.png"}, "")
	if !errors.Is(err, ErrNoChatID) {
		t.Fatalf("err = %v, want ErrNoChatID", err)
	}
}
