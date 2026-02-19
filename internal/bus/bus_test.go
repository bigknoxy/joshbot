package bus

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNewMessageBus(t *testing.T) {
	mb := NewMessageBus()
	if mb == nil {
		t.Fatal("NewMessageBus returned nil")
	}
	if mb.started {
		t.Error("bus should not be started by default")
	}
	if mb.registry == nil {
		t.Error("registry should not be nil")
	}
}

func TestNewMessageBusWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mb := NewMessageBusWithContext(ctx)
	if mb == nil {
		t.Fatal("NewMessageBusWithContext returned nil")
	}
	// The bus wraps the context, so check it was initialized
	if mb.ctx == nil {
		t.Error("bus should have a non-nil context")
	}
}

func TestMessageBusStart(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	if !mb.IsRunning() {
		t.Error("bus should be running after Start")
	}
}

func TestMessageBusStartTwice(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	mb.Start() // Should not panic or cause issues
	defer mb.Stop()

	if !mb.IsRunning() {
		t.Error("bus should be running")
	}
}

func TestMessageBusStop(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	mb.Stop()

	if mb.IsRunning() {
		t.Error("bus should not be running after Stop")
	}
}

func TestMessageBusStopMultiple(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	mb.Stop()
	mb.Stop() // Should not panic
	mb.Stop()
}

func TestSubscribeUnsubscribe(t *testing.T) {
	mb := NewMessageBus()

	handlerCalled := false
	handler := func(ctx context.Context, msg InboundMessage) {
		handlerCalled = true
	}

	mb.Subscribe("test", handler)

	// Manually dispatch a message to test handler
	mb.dispatchInbound(InboundMessage{
		SenderID: "user1",
		Channel:  "test",
		Content:  "hello",
	})

	time.Sleep(50 * time.Millisecond)
	if !handlerCalled {
		t.Error("handler should have been called")
	}

	// Test unsubscribe
	mb.Unsubscribe("test", handler)
	handlerCalled = false
	mb.dispatchInbound(InboundMessage{
		SenderID: "user1",
		Channel:  "test",
		Content:  "hello",
	})
	time.Sleep(50 * time.Millisecond)
	if handlerCalled {
		t.Error("handler should not have been called after unsubscribe")
	}
}

func TestSendNonBlocking(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	msg := InboundMessage{
		SenderID:  "user1",
		Content:   "test message",
		Channel:   "test",
		Timestamp: time.Now(),
	}

	if !mb.Send(msg) {
		t.Error("Send should succeed when queue has capacity")
	}
}

func TestSendNonBlockingFull(t *testing.T) {
	mb := NewMessageBus()
	// Don't start - we want to test queue behavior without processing

	// Fill the queue
	for i := 0; i < MaxQueueSize; i++ {
		msg := InboundMessage{
			SenderID:  "user1",
			Content:   "test message",
			Channel:   "test",
			Timestamp: time.Now(),
		}
		if !mb.Send(msg) {
			// Queue is full as expected
			break
		}
	}

	// Now queue should be full
	msg := InboundMessage{
		SenderID:  "user1",
		Content:   "overflow",
		Channel:   "test",
		Timestamp: time.Now(),
	}
	if mb.Send(msg) {
		t.Error("Send should fail when queue is full")
	}
}

func TestSendBlocking(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	msg := InboundMessage{
		SenderID:  "user1",
		Content:   "test",
		Channel:   "test",
		Timestamp: time.Now(),
	}

	err := mb.SendBlocking(ctx, msg)
	if err != nil {
		t.Errorf("SendBlocking failed: %v", err)
	}
}

func TestSendBlockingCancelled(t *testing.T) {
	mb := NewMessageBus()
	// Don't start - so the channel will block forever

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	msg := InboundMessage{
		SenderID:  "user1",
		Content:   "test",
		Channel:   "test",
		Timestamp: time.Now(),
	}

	err := mb.SendBlocking(ctx, msg)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestPublishNonBlocking(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	msg := OutboundMessage{
		Content:   "response",
		Channel:   "telegram",
		ChannelID: "chat123",
		Timestamp: time.Now(),
	}

	if !mb.Publish(msg) {
		t.Error("Publish should succeed when queue has capacity")
	}
}

func TestPublishNonBlockingFull(t *testing.T) {
	mb := NewMessageBus()

	// Fill the queue
	for i := 0; i < MaxQueueSize; i++ {
		msg := OutboundMessage{
			Content:   "response",
			Channel:   "telegram",
			ChannelID: "chat123",
			Timestamp: time.Now(),
		}
		if !mb.Publish(msg) {
			break
		}
	}

	// Now queue should be full
	msg := OutboundMessage{
		Content:   "overflow",
		Channel:   "telegram",
		ChannelID: "chat123",
		Timestamp: time.Now(),
	}
	if mb.Publish(msg) {
		t.Error("Publish should fail when queue is full")
	}
}

func TestPublishBlocking(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	msg := OutboundMessage{
		Content:   "response",
		Channel:   "telegram",
		ChannelID: "chat123",
		Timestamp: time.Now(),
	}

	err := mb.PublishBlocking(ctx, msg)
	if err != nil {
		t.Errorf("PublishBlocking failed: %v", err)
	}
}

func TestHandlerAllTopic(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	var wg sync.WaitGroup
	wg.Add(1)

	handler := func(ctx context.Context, msg InboundMessage) {
		defer wg.Done()
		if msg.Content != "test" {
			t.Errorf("expected 'test', got %q", msg.Content)
		}
	}

	mb.Subscribe("all", handler)

	mb.Send(InboundMessage{
		SenderID:  "user1",
		Content:   "test",
		Channel:   "telegram", // Different from "all"
		Timestamp: time.Now(),
	})

	wg.Wait()
}

func TestMultipleHandlers(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	var count int
	var mu sync.Mutex

	handler1 := func(ctx context.Context, msg InboundMessage) {
		mu.Lock()
		count++
		mu.Unlock()
	}
	handler2 := func(ctx context.Context, msg InboundMessage) {
		mu.Lock()
		count++
		mu.Unlock()
	}

	mb.Subscribe("test", handler1)
	mb.Subscribe("test", handler2)

	mb.Send(InboundMessage{
		SenderID:  "user1",
		Content:   "test",
		Channel:   "test",
		Timestamp: time.Now(),
	})

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if count != 2 {
		t.Errorf("expected 2 handlers called, got %d", count)
	}
	mu.Unlock()
}

func TestQueueLength(t *testing.T) {
	mb := NewMessageBus()
	// Don't start the bus - check raw channel lengths

	inbound, outbound := mb.QueueLength()
	if inbound != 0 || outbound != 0 {
		t.Errorf("expected empty queues, got inbound=%d outbound=%d", inbound, outbound)
	}

	// Add some messages directly to channels (bus not running)
	mb.inboundCh <- InboundMessage{Channel: "test", Content: "1"}
	mb.inboundCh <- InboundMessage{Channel: "test", Content: "2"}
	mb.outboundCh <- OutboundMessage{Channel: "test", Content: "a"}

	inbound, outbound = mb.QueueLength()
	if inbound != 2 {
		t.Errorf("expected 2 inbound messages, got %d", inbound)
	}
	if outbound != 1 {
		t.Errorf("expected 1 outbound message, got %d", outbound)
	}
}

func TestInboundChannelAccess(t *testing.T) {
	mb := NewMessageBus()
	ch := mb.InboundChannel()
	if ch == nil {
		t.Error("InboundChannel returned nil")
	}
}

func TestOutboundChannelAccess(t *testing.T) {
	mb := NewMessageBus()
	ch := mb.OutboundChannel()
	if ch == nil {
		t.Error("OutboundChannel returned nil")
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mb := NewMessageBusWithContext(ctx)
	mb.Start()

	var wg sync.WaitGroup
	wg.Add(1)

	handler := func(ctx context.Context, msg InboundMessage) {
		<-ctx.Done()
		wg.Done()
	}

	mb.Subscribe("test", handler)
	mb.Send(InboundMessage{Channel: "test", Content: "test"})

	time.Sleep(20 * time.Millisecond)
	cancel()
	wg.Wait()
	mb.Stop()
}

func TestHandlerRegistry(t *testing.T) {
	registry := NewHandlerRegistry()

	handler1 := func(ctx context.Context, msg InboundMessage) {}
	handler2 := func(ctx context.Context, msg InboundMessage) {}

	registry.Subscribe("topic1", handler1)
	registry.Subscribe("topic1", handler2)

	handlers := registry.GetHandlers("topic1")
	if len(handlers) != 2 {
		t.Errorf("expected 2 handlers, got %d", len(handlers))
	}

	// Unsubscribe one
	registry.Unsubscribe("topic1", handler1)
	handlers = registry.GetHandlers("topic1")
	if len(handlers) != 1 {
		t.Errorf("expected 1 handler after unsubscribe, got %d", len(handlers))
	}

	// Non-existent topic
	handlers = registry.GetHandlers("nonexistent")
	if len(handlers) != 0 {
		t.Errorf("expected 0 handlers for nonexistent topic, got %d", len(handlers))
	}
}

func TestMessageBuilder(t *testing.T) {
	builder := NewMessageBuilder()
	msg := builder.
		WithSender("user123").
		WithContent("Hello world").
		WithChannel("telegram").
		WithMetadata("chat_id", 12345).
		Build()

	if msg.SenderID != "user123" {
		t.Errorf("expected SenderID 'user123', got %q", msg.SenderID)
	}
	if msg.Content != "Hello world" {
		t.Errorf("expected Content 'Hello world', got %q", msg.Content)
	}
	if msg.Channel != "telegram" {
		t.Errorf("expected Channel 'telegram', got %q", msg.Channel)
	}
	if msg.Metadata["chat_id"] != 12345 {
		t.Errorf("expected metadata chat_id 12345, got %v", msg.Metadata["chat_id"])
	}
}

func TestOutboundBuilder(t *testing.T) {
	builder := NewOutboundBuilder()
	msg := builder.
		WithContent("Response message").
		WithChannel("telegram").
		WithChannelID("chat123").
		WithSenderID("user123").
		WithMetadata("parse_mode", "Markdown").
		Build()

	if msg.Content != "Response message" {
		t.Errorf("expected Content 'Response message', got %q", msg.Content)
	}
	if msg.Channel != "telegram" {
		t.Errorf("expected Channel 'telegram', got %q", msg.Channel)
	}
	if msg.ChannelID != "chat123" {
		t.Errorf("expected ChannelID 'chat123', got %q", msg.ChannelID)
	}
	if msg.SenderID != "user123" {
		t.Errorf("expected SenderID 'user123', got %q", msg.SenderID)
	}
	if msg.Metadata["parse_mode"] != "Markdown" {
		t.Errorf("expected metadata parse_mode Markdown, got %v", msg.Metadata["parse_mode"])
	}
}

func TestBusEvent(t *testing.T) {
	event, err := NewBusEvent(EventMessage, "telegram", EventData{
		SenderID: "user1",
		Content:  "hello",
	})
	if err != nil {
		t.Fatalf("failed to create event: %v", err)
	}

	if event.Type != EventMessage {
		t.Errorf("expected type EventMessage, got %v", event.Type)
	}
	if event.Source != "telegram" {
		t.Errorf("expected source telegram, got %v", event.Source)
	}

	var data EventData
	err = event.ParseData(&data)
	if err != nil {
		t.Fatalf("failed to parse event data: %v", err)
	}
	if data.SenderID != "user1" {
		t.Errorf("expected sender user1, got %v", data.SenderID)
	}
	if data.Content != "hello" {
		t.Errorf("expected content hello, got %v", data.Content)
	}
}

func TestBusEventString(t *testing.T) {
	event := &BusEvent{
		Type:      EventCommand,
		Timestamp: time.Now(),
		Source:    "cli",
	}
	str := event.String()
	if str == "" {
		t.Error("String() should not return empty string")
	}
}

func TestMessageFields(t *testing.T) {
	now := time.Now()
	msg := InboundMessage{
		SenderID:  "user1",
		Content:   "test content",
		Channel:   "telegram",
		Timestamp: now,
		Metadata: map[string]any{
			"chat_id":  int64(12345),
			"username": "testuser",
		},
	}

	if msg.SenderID != "user1" {
		t.Errorf("expected SenderID 'user1', got %q", msg.SenderID)
	}
	if msg.Content != "test content" {
		t.Errorf("expected Content 'test content', got %q", msg.Content)
	}
	if msg.Channel != "telegram" {
		t.Errorf("expected Channel 'telegram', got %q", msg.Channel)
	}
	if !msg.Timestamp.Equal(now) {
		t.Errorf("expected Timestamp %v, got %v", now, msg.Timestamp)
	}
	if msg.Metadata["chat_id"] != int64(12345) {
		t.Errorf("expected chat_id 12345, got %v", msg.Metadata["chat_id"])
	}
}

func TestOutboundMessageFields(t *testing.T) {
	now := time.Now()
	msg := OutboundMessage{
		Content:   "response content",
		Channel:   "telegram",
		ChannelID: "chat123",
		Timestamp: now,
		Metadata:  map[string]any{"reply_to": "msg456"},
		SenderID:  "user1",
	}

	if msg.Content != "response content" {
		t.Errorf("expected Content 'response content', got %q", msg.Content)
	}
	if msg.Channel != "telegram" {
		t.Errorf("expected Channel 'telegram', got %q", msg.Channel)
	}
	if msg.ChannelID != "chat123" {
		t.Errorf("expected ChannelID 'chat123', got %q", msg.ChannelID)
	}
	if msg.SenderID != "user1" {
		t.Errorf("expected SenderID 'user1', got %q", msg.SenderID)
	}
}

func TestMaxQueueSize(t *testing.T) {
	if MaxQueueSize != 1000 {
		t.Errorf("expected MaxQueueSize 1000, got %d", MaxQueueSize)
	}
}

func TestTopicConstants(t *testing.T) {
	tests := []struct {
		got  string
		want string
	}{
		{TopicAll, "all"},
		{TopicAgent, "agent"},
		{TopicCommands, "commands"},
		{TopicTelegram, "telegram"},
		{TopicCLI, "cli"},
		{TopicOutbound, "outbound"},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("expected topic %q, got %q", tt.want, tt.got)
		}
	}
}

func TestEventTypeConstants(t *testing.T) {
	tests := []struct {
		got  EventType
		want EventType
	}{
		{EventMessage, "message"},
		{EventTyping, "typing"},
		{EventEdit, "edit"},
		{EventDelete, "delete"},
		{EventCommand, "command"},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("expected event type %q, got %q", tt.want, tt.got)
		}
	}
}

// Benchmark tests for performance
func BenchmarkSend(b *testing.B) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	msg := InboundMessage{
		SenderID:  "user1",
		Content:   "test",
		Channel:   "test",
		Timestamp: time.Now(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mb.Send(msg)
	}
}

func BenchmarkPublish(b *testing.B) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	msg := OutboundMessage{
		Content:   "response",
		Channel:   "telegram",
		ChannelID: "chat123",
		Timestamp: time.Now(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mb.Publish(msg)
	}
}
