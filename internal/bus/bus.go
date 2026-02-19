package bus

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// MaxQueueSize is the maximum number of messages that can be buffered in any channel.
const MaxQueueSize = 1000

// InboundMessage represents an incoming message from a chat channel.
type InboundMessage struct {
	SenderID  string         // Unique identifier for the sender
	Content   string         // Message text content
	Channel   string         // Source channel (e.g., "telegram", "cli")
	Timestamp time.Time      // When the message was received
	Metadata  map[string]any // Additional context (username, chat ID, etc.)
}

// OutboundMessage represents a message to be sent to a chat channel.
type OutboundMessage struct {
	Content   string         // Message text content
	Channel   string         // Target channel (e.g., "telegram", "cli")
	ChannelID string         // Specific channel identifier for response
	Timestamp time.Time      // When the message is being sent
	Metadata  map[string]any // Additional context (reply to, parse mode, etc.)
	SenderID  string         // Original sender ID for reference
}

// MessageHandler is a function that processes inbound messages.
type MessageHandler func(ctx context.Context, msg InboundMessage)

// handlerEntry stores a handler with its unique ID.
type handlerEntry struct {
	id uintptr
	fn MessageHandler
}

// HandlerRegistry maintains topic -> handlers mapping.
type HandlerRegistry struct {
	mu       sync.RWMutex
	handlers map[string][]handlerEntry
}

// NewHandlerRegistry creates a new handler registry.
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		handlers: make(map[string][]handlerEntry),
	}
}

// Subscribe registers a handler for a specific topic.
func (r *HandlerRegistry) Subscribe(topic string, handler MessageHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Use the function's pointer value as ID
	r.handlers[topic] = append(r.handlers[topic], handlerEntry{
		id: reflect.ValueOf(handler).Pointer(),
		fn: handler,
	})
}

// Unsubscribe removes a handler from a topic.
func (r *HandlerRegistry) Unsubscribe(topic string, handler MessageHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	handlers := r.handlers[topic]
	targetID := reflect.ValueOf(handler).Pointer()
	for i, h := range handlers {
		if h.id == targetID {
			r.handlers[topic] = append(handlers[:i], handlers[i+1:]...)
			return
		}
	}
}

// GetHandlers returns all handlers for a topic.
func (r *HandlerRegistry) GetHandlers(topic string) []MessageHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Return a copy to avoid race conditions
	handlers := r.handlers[topic]
	result := make([]MessageHandler, len(handlers))
	for i, h := range handlers {
		result[i] = h.fn
	}
	return result
}

// MessageBus handles async message routing between channels and the agent.
type MessageBus struct {
	inboundCh  chan InboundMessage
	outboundCh chan OutboundMessage
	registry   *HandlerRegistry
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	started    bool
	mu         sync.RWMutex
}

// NewMessageBus creates a new MessageBus with default buffer sizes.
func NewMessageBus() *MessageBus {
	ctx, cancel := context.WithCancel(context.Background())
	return &MessageBus{
		inboundCh:  make(chan InboundMessage, MaxQueueSize),
		outboundCh: make(chan OutboundMessage, MaxQueueSize),
		registry:   NewHandlerRegistry(),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// NewMessageBusWithContext creates a new MessageBus with an external context.
func NewMessageBusWithContext(ctx context.Context) *MessageBus {
	ctx, cancel := context.WithCancel(ctx)
	return &MessageBus{
		inboundCh:  make(chan InboundMessage, MaxQueueSize),
		outboundCh: make(chan OutboundMessage, MaxQueueSize),
		registry:   NewHandlerRegistry(),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start begins processing messages. Must be called before Send/Publish.
func (mb *MessageBus) Start() {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if mb.started {
		return
	}
	mb.started = true
	mb.wg.Add(2)
	go mb.processInbound(mb.ctx)
	go mb.processOutbound(mb.ctx)
}

// Subscribe registers a handler for inbound messages on a topic.
func (mb *MessageBus) Subscribe(topic string, handler MessageHandler) {
	mb.registry.Subscribe(topic, handler)
}

// Unsubscribe removes a handler from a topic.
func (mb *MessageBus) Unsubscribe(topic string, handler MessageHandler) {
	mb.registry.Unsubscribe(topic, handler)
}

// Send publishes an inbound message to the bus (non-blocking).
// Returns false if the queue is full.
func (mb *MessageBus) Send(msg InboundMessage) bool {
	select {
	case mb.inboundCh <- msg:
		return true
	default:
		// Queue full - non-blocking overflow handling
		return false
	}
}

// SendBlocking publishes an inbound message, blocking if queue is full.
// Returns context.Canceled if context is cancelled.
func (mb *MessageBus) SendBlocking(ctx context.Context, msg InboundMessage) error {
	select {
	case mb.inboundCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Publish sends an outbound message to a channel (non-blocking).
// Returns false if the queue is full.
func (mb *MessageBus) Publish(msg OutboundMessage) bool {
	select {
	case mb.outboundCh <- msg:
		return true
	default:
		// Queue full - non-blocking overflow handling
		return false
	}
}

// PublishBlocking sends an outbound message, blocking if queue is full.
// Returns context.Canceled if context is cancelled.
func (mb *MessageBus) PublishBlocking(ctx context.Context, msg OutboundMessage) error {
	select {
	case mb.outboundCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// InboundChannel returns the inbound message channel for external consumers.
// Use this to receive messages directly from the bus.
func (mb *MessageBus) InboundChannel() <-chan InboundMessage {
	return mb.inboundCh
}

// OutboundChannel returns the outbound message channel for external consumers.
func (mb *MessageBus) OutboundChannel() <-chan OutboundMessage {
	return mb.outboundCh
}

// InboundChan returns the inbound channel for selecting.
func (mb *MessageBus) InboundChan() chan<- InboundMessage {
	return mb.inboundCh
}

// OutboundChan returns the outbound channel for selecting.
func (mb *MessageBus) OutboundChan() chan<- OutboundMessage {
	return mb.outboundCh
}

// processInbound handles inbound messages, dispatching to registered handlers.
func (mb *MessageBus) processInbound(ctx context.Context) {
	defer mb.wg.Done()
	for {
		select {
		case <-ctx.Done():
			// Drain remaining messages before exiting
			for {
				select {
				case msg := <-mb.inboundCh:
					mb.dispatchInbound(msg)
				default:
					return
				}
			}
		case msg := <-mb.inboundCh:
			mb.dispatchInbound(msg)
		}
	}
}

// dispatchInbound sends a message to all registered handlers for the channel.
func (mb *MessageBus) dispatchInbound(msg InboundMessage) {
	handlers := mb.registry.GetHandlers(msg.Channel)
	for _, handler := range handlers {
		select {
		case <-mb.ctx.Done():
			return
		default:
			// Execute handler in a goroutine to avoid blocking
			go handler(mb.ctx, msg)
		}
	}
	// Also dispatch to "all" topic if different from channel
	if msg.Channel != "all" {
		allHandlers := mb.registry.GetHandlers("all")
		for _, handler := range allHandlers {
			select {
			case <-mb.ctx.Done():
				return
			default:
				go handler(mb.ctx, msg)
			}
		}
	}
}

// processOutbound handles outbound messages.
// Currently logs them; in production would route to actual channels.
func (mb *MessageBus) processOutbound(ctx context.Context) {
	defer mb.wg.Done()
	for {
		select {
		case <-ctx.Done():
			// Drain remaining messages before exiting
			for {
				select {
				case msg := <-mb.outboundCh:
					_ = msg // Would be dispatched to channel handlers
				default:
					return
				}
			}
		case msg := <-mb.outboundCh:
			// Outbound messages would be handled by channel implementations
			// that subscribe to the outbound channel
			_ = msg
		}
	}
}

// Stop gracefully shuts down the message bus.
// It cancels the context and waits for all handlers to complete.
func (mb *MessageBus) Stop() {
	mb.mu.Lock()
	if !mb.started {
		mb.mu.Unlock()
		return
	}
	mb.cancel()
	mb.mu.Unlock()

	// Wait for all goroutines to finish
	mb.wg.Wait()

	// Close channels (optional, depends on usage pattern)
	mb.mu.Lock()
	close(mb.inboundCh)
	close(mb.outboundCh)
	mb.inboundCh = make(chan InboundMessage, MaxQueueSize)
	mb.outboundCh = make(chan OutboundMessage, MaxQueueSize)
	mb.started = false
	mb.mu.Unlock()
}

// IsRunning returns whether the bus is currently processing messages.
func (mb *MessageBus) IsRunning() bool {
	mb.mu.RLock()
	defer mb.mu.RUnlock()
	return mb.started
}

// QueueLength returns the current number of messages in both queues.
func (mb *MessageBus) QueueLength() (inbound, outbound int) {
	mb.mu.RLock()
	defer mb.mu.RUnlock()
	return len(mb.inboundCh), len(mb.outboundCh)
}
