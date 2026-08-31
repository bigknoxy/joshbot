package bus

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
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

	handlerCalled := make(chan bool, 1)
	handler := func(ctx context.Context, msg InboundMessage) {
		select {
		case handlerCalled <- true:
		default:
		}
	}

	mb.Subscribe("test", handler)

	// Manually dispatch a message to test handler
	mb.dispatchInbound(context.Background(), InboundMessage{
		SenderID: "user1",
		Channel:  "test",
		Content:  "hello",
	})

	select {
	case <-handlerCalled:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Error("handler should have been called")
	}

	// Test unsubscribe
	mb.Unsubscribe("test", handler)
	mb.dispatchInbound(context.Background(), InboundMessage{
		SenderID: "user1",
		Channel:  "test",
		Content:  "hello",
	})

	// Give it time to potentially call handler
	time.Sleep(50 * time.Millisecond)

	select {
	case <-handlerCalled:
		t.Error("handler should not have been called after unsubscribe")
	default:
		// Success - no message received
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
	// Don't start - so the channel will block

	// Fill the channel completely so it blocks
	for i := 0; i < MaxQueueSize; i++ {
		mb.inboundCh <- InboundMessage{
			SenderID:  fmt.Sprintf("user%d", i),
			Content:   "test",
			Channel:   "test",
			Timestamp: time.Now(),
		}
	}

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

// This is the regression test for "enable Telegram or Discord, not both"
// (AGENTS.md/CLAUDE.md): before fan-out, every channel implementation called
// OutboundChannel() and got back the *same* underlying channel, so two
// channels running together raced an ordinary Go channel receive over each
// message — the loser silently never saw it, with no error anywhere. Two
// independent consumers here must each see every message published,
// regardless of which one is addressed to them.
func TestOutboundChannelAccess_TwoConsumersEachSeeEveryMessage(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	telegramCh := mb.OutboundChannel()
	discordCh := mb.OutboundChannel()

	const n = 50
	for i := 0; i < n; i++ {
		channel := "telegram"
		if i%2 == 0 {
			channel = "discord"
		}
		if !mb.Publish(OutboundMessage{Content: fmt.Sprintf("msg-%d", i), Channel: channel}) {
			t.Fatalf("Publish failed at message %d", i)
		}
	}

	// Each consumer must receive all n messages — its own addressed ones
	// plus the ones meant for the other channel, exactly as a real channel
	// implementation's consumeOutbound loop receives everything and then
	// filters by msg.Channel itself.
	drain := func(ch <-chan OutboundMessage) int {
		got := 0
		timeout := time.After(2 * time.Second)
		for got < n {
			select {
			case <-ch:
				got++
			case <-timeout:
				return got
			}
		}
		return got
	}

	gotTelegram := drain(telegramCh)
	gotDiscord := drain(discordCh)

	if gotTelegram != n {
		t.Errorf("telegram consumer received %d of %d messages, want all %d — a message was lost to fan-out", gotTelegram, n, n)
	}
	if gotDiscord != n {
		t.Errorf("discord consumer received %d of %d messages, want all %d — a message was lost to fan-out", gotDiscord, n, n)
	}
}

// A consumer registered via RegisterOutboundConsumer directly (not through
// OutboundChannel's sugar) must fan out identically.
func TestRegisterOutboundConsumer_ReceivesFannedOutMessages(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	ch := mb.RegisterOutboundConsumer()

	if !mb.Publish(OutboundMessage{Content: "hello", Channel: "telegram"}) {
		t.Fatal("Publish failed")
	}

	select {
	case msg := <-ch:
		if msg.Content != "hello" {
			t.Errorf("Content = %q, want %q", msg.Content, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registered consumer never received the published message")
	}
}

// A slow or absent consumer must not stall delivery to other consumers, and
// must not block Publish or the dispatcher loop — a full per-consumer
// buffer drops that one message for that one consumer only.
func TestFanOutOutbound_SlowConsumerDoesNotBlockOthers(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	slow := mb.RegisterOutboundConsumer() // never drained
	fast := mb.RegisterOutboundConsumer()

	// Overfill the slow consumer's own buffer (MaxQueueSize) plus a bit more,
	// then confirm the fast consumer still receives fresh messages published
	// afterward — the slow one being permanently full must not back up the
	// shared dispatcher loop.
	for i := 0; i < MaxQueueSize+5; i++ {
		mb.Publish(OutboundMessage{Content: fmt.Sprintf("m%d", i), Channel: "x"})
	}

	select {
	case <-fast:
	case <-time.After(2 * time.Second):
		t.Fatal("fast consumer never received a message; a slow consumer stalled fan-out")
	}
	_ = slow
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

// TestBoundedConcurrency verifies that the semaphore limits concurrent handler executions.
func TestBoundedConcurrency(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	// Track maximum concurrent handler executions
	var maxConcurrent int
	var mu sync.Mutex
	currentConcurrent := 0

	var wg sync.WaitGroup

	handler := func(ctx context.Context, msg InboundMessage) {
		mu.Lock()
		currentConcurrent++
		if currentConcurrent > maxConcurrent {
			maxConcurrent = currentConcurrent
		}
		mu.Unlock()

		// Hold the handler for a bit to allow concurrency to build up
		time.Sleep(10 * time.Millisecond)

		mu.Lock()
		currentConcurrent--
		mu.Unlock()
		wg.Done()
	}

	// Subscribe handler to test topic
	mb.Subscribe("test", handler)

	// Send multiple messages rapidly to trigger concurrent executions
	const numMessages = 50
	wg.Add(numMessages)
	for i := 0; i < numMessages; i++ {
		mb.Send(InboundMessage{
			SenderID:  fmt.Sprintf("user%d", i),
			Content:   "test",
			Channel:   "test",
			Timestamp: time.Now(),
		})
	}

	// Wait for all handlers to complete
	wg.Wait()

	// Verify that concurrency was bounded
	if maxConcurrent > MaxConcurrentHandlers {
		t.Errorf("max concurrent handlers %d exceeded limit %d", maxConcurrent, MaxConcurrentHandlers)
	}

	// Verify some concurrency actually happened (otherwise test is meaningless)
	if maxConcurrent < 2 {
		t.Error("expected some concurrent execution, but max was", maxConcurrent)
	}

	t.Logf("Max concurrent handlers: %d (limit: %d)", maxConcurrent, MaxConcurrentHandlers)
}

// TestBoundedConcurrencyWithAllTopic verifies bounded concurrency works for "all" topic too.
func TestBoundedConcurrencyWithAllTopic(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	var maxConcurrent int
	var mu sync.Mutex
	currentConcurrent := 0

	var wg sync.WaitGroup

	handler := func(ctx context.Context, msg InboundMessage) {
		mu.Lock()
		currentConcurrent++
		if currentConcurrent > maxConcurrent {
			maxConcurrent = currentConcurrent
		}
		mu.Unlock()

		time.Sleep(10 * time.Millisecond)

		mu.Lock()
		currentConcurrent--
		mu.Unlock()
		wg.Done()
	}

	// Subscribe to "all" topic
	mb.Subscribe("all", handler)

	// Send messages to a specific channel (not "all")
	const numMessages = 30
	wg.Add(numMessages)
	for i := 0; i < numMessages; i++ {
		mb.Send(InboundMessage{
			SenderID:  fmt.Sprintf("user%d", i),
			Content:   "test",
			Channel:   "telegram", // Different from "all"
			Timestamp: time.Now(),
		})
	}

	wg.Wait()

	if maxConcurrent > MaxConcurrentHandlers {
		t.Errorf("max concurrent handlers %d exceeded limit %d", maxConcurrent, MaxConcurrentHandlers)
	}

	t.Logf("Max concurrent handlers (all topic): %d (limit: %d)", maxConcurrent, MaxConcurrentHandlers)
}

// TestMaxConcurrentHandlersConstant verifies the constant is set to a reasonable value.
func TestMaxConcurrentHandlersConstant(t *testing.T) {
	if MaxConcurrentHandlers <= 0 {
		t.Errorf("MaxConcurrentHandlers should be positive, got %d", MaxConcurrentHandlers)
	}
	if MaxConcurrentHandlers > 1000 {
		t.Errorf("MaxConcurrentHandlers should be reasonable (<1000), got %d", MaxConcurrentHandlers)
	}
}

// TestBoundedConcurrencyStrict verifies the semaphore strictly limits concurrency
// by using handlers that block briefly, allowing us to measure concurrent executions.
func TestBoundedConcurrencyStrict(t *testing.T) {
	mb := NewMessageBus()
	mb.Start()
	defer mb.Stop()

	// Use a barrier to synchronize handler starts
	startBarrier := make(chan struct{})
	continueBarrier := make(chan struct{})

	var maxConcurrent int
	var mu sync.Mutex
	currentConcurrent := 0

	handler := func(ctx context.Context, msg InboundMessage) {
		mu.Lock()
		currentConcurrent++
		if currentConcurrent > maxConcurrent {
			maxConcurrent = currentConcurrent
		}
		mu.Unlock()

		// Signal that this handler has started
		startBarrier <- struct{}{}

		// Wait until told to continue
		<-continueBarrier

		mu.Lock()
		currentConcurrent--
		mu.Unlock()
	}

	mb.Subscribe("test", handler)

	// Send messages equal to the limit plus some extra
	numMessages := MaxConcurrentHandlers + 20

	// Start sending messages and waiting for handlers to accumulate
	for i := 0; i < numMessages; i++ {
		mb.Send(InboundMessage{
			SenderID:  fmt.Sprintf("user%d", i),
			Content:   "test",
			Channel:   "test",
			Timestamp: time.Now(),
		})

		// Wait for each handler to start before sending the next
		// This ensures we build up concurrent handlers
		if i < MaxConcurrentHandlers {
			<-startBarrier
		}
	}

	// Now let all accumulated handlers complete
	close(continueBarrier)

	// Give time for all to complete
	time.Sleep(100 * time.Millisecond)

	// The max should be bounded by the semaphore limit (or close to it)
	// We use > not >= because there can be some timing variance
	if maxConcurrent > MaxConcurrentHandlers {
		t.Errorf("max concurrent handlers %d exceeded limit %d", maxConcurrent, MaxConcurrentHandlers)
	}

	// Also verify we actually achieved significant concurrency
	if maxConcurrent < MaxConcurrentHandlers/2 {
		t.Logf("Warning: max concurrent %d seems low compared to limit %d", maxConcurrent, MaxConcurrentHandlers)
	}

	t.Logf("Max concurrent handlers: %d (limit: %d)", maxConcurrent, MaxConcurrentHandlers)
}

// TestSendPublishRaceWithStop hammers Send/Publish concurrently while Stop runs.
//
// Against the previous implementation (Stop closed and replaced inboundCh /
// outboundCh under mb.mu while Send/Publish wrote to them without the lock),
// this reproduced two failures: a "send on closed channel" panic when a writer
// hit the channel between close and reassignment, and a data race that
// `go test -race` flags on the channel fields (Stop reassigning them, Send/
// Publish reading them). With cancel-only shutdown (channels never closed or
// replaced) it passes cleanly.
func TestSendPublishRaceWithStop(t *testing.T) {
	for iter := 0; iter < 10; iter++ {
		mb := NewMessageBus()
		mb.Start()

		var wg sync.WaitGroup

		// Writers modeling unsynchronized senders (channels, handler
		// goroutines). Each does a bounded number of writes, yielding between
		// them so Stop is guaranteed to overlap them mid-flight rather than
		// starving the scheduler with a tight spin.
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 500; j++ {
					mb.Send(InboundMessage{Channel: "test", Content: "in"})
					mb.Publish(OutboundMessage{Channel: "test", Content: "out"})
					runtime.Gosched()
				}
			}()
		}

		// Stop concurrently with the in-flight writers. Against the old code
		// this was the window where a writer could hit a just-closed channel.
		wg.Add(1)
		go func() {
			defer wg.Done()
			mb.Stop()
		}()

		wg.Wait()
	}
}

// TestRestartUnderLoadIsRaceFree pins the data race that Start/Stop/Start used
// to trigger: Start reassigned mb.ctx under mb.mu while handler goroutines from
// the previous run — which Stop deliberately does not wait for — still read the
// field unlocked. The context is now threaded through processInbound →
// dispatchInbound → dispatchToHandlers, so nothing reads mb.ctx off-goroutine.
// Meaningful only under -race.
func TestRestartUnderLoadIsRaceFree(t *testing.T) {
	mb := NewMessageBus()

	var handled atomic.Int64
	mb.Subscribe("all", func(ctx context.Context, msg InboundMessage) {
		// Touch the context the way a real handler would.
		select {
		case <-ctx.Done():
		default:
		}
		handled.Add(1)
	})

	stop := make(chan struct{})
	var senders sync.WaitGroup
	for i := 0; i < 4; i++ {
		senders.Add(1)
		go func() {
			defer senders.Done()
			for {
				select {
				case <-stop:
					return
				default:
					mb.Send(InboundMessage{Channel: "load", Content: "x"})
					// Yield between sends: an unthrottled sender can keep the
					// drain loop in processInbound permanently non-empty.
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	for i := 0; i < 20; i++ {
		mb.Start()
		time.Sleep(time.Millisecond)
		mb.Stop()
	}

	close(stop)
	senders.Wait()

	if mb.IsRunning() {
		t.Error("bus should be stopped")
	}
	_ = handled.Load()
}

// TestHandlerContextIsPerStartCycle is the deterministic companion to the race
// test above. -race only reports the unsynchronized read if the scheduler
// happens to interleave it with a Start, so a partial revert (reading mb.ctx
// inside dispatchToHandlers rather than threading the run's own context) can
// slip through several runs. This asserts the observable consequence instead:
// each Start cycle hands its handlers a distinct context, and the context a
// handler received is cancelled by that cycle's Stop — never left live, never
// shared with the next cycle.
func TestHandlerContextIsPerStartCycle(t *testing.T) {
	mb := NewMessageBus()

	got := make(chan context.Context, 8)
	mb.Subscribe("all", func(ctx context.Context, msg InboundMessage) {
		got <- ctx
	})

	var seen []context.Context
	for cycle := 0; cycle < 3; cycle++ {
		mb.Start()
		mb.Send(InboundMessage{Channel: "load", Content: "x"})

		var ctx context.Context
		select {
		case ctx = <-got:
		case <-time.After(2 * time.Second):
			t.Fatalf("cycle %d: handler never ran", cycle)
		}
		if ctx == nil {
			t.Fatalf("cycle %d: handler received a nil context", cycle)
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("cycle %d: handler received an already-cancelled context: %v", cycle, err)
		}

		mb.Stop()

		// The cycle's own context must be cancelled by that cycle's Stop.
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("cycle %d: Stop did not cancel the context the handler received", cycle)
		}

		for i, prev := range seen {
			if prev == ctx {
				t.Fatalf("cycle %d reused the context from cycle %d; handlers are not getting a per-run context", cycle, i)
			}
		}
		seen = append(seen, ctx)
	}
}
