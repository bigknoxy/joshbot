// Package tools provides the tool system for joshbot's agent.
package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// ErrNoChatID is returned when no chat ID is stored for a channel.
var ErrNoChatID = errors.New("no chat ID stored for channel")

// ErrQueueFull is returned when the outbound message queue is full.
var ErrQueueFull = errors.New("message queue full")

// BusMessageSender implements MessageSender for proactive messaging.
// It stores chat IDs per channel and publishes messages to the message bus.
type BusMessageSender struct {
	mu       sync.RWMutex
	chatIDs  map[string]string
	bus      *bus.MessageBus
	senderID string
}

// NewBusMessageSender creates a new BusMessageSender.
func NewBusMessageSender(messageBus *bus.MessageBus) *BusMessageSender {
	return &BusMessageSender{
		chatIDs: make(map[string]string),
		bus:     messageBus,
	}
}

// SetChatID stores a chat ID for a channel.
func (s *BusMessageSender) SetChatID(channel, chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatIDs[channel] = chatID
}

// GetChatID retrieves a stored chat ID for a channel.
func (s *BusMessageSender) GetChatID(channel string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.chatIDs[channel]
	return id, ok
}

// SetSenderID sets the sender ID for outgoing messages.
func (s *BusMessageSender) SetSenderID(senderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.senderID = senderID
}

// SendMessage sends a message to the specified channel via the message bus.
func (s *BusMessageSender) SendMessage(ctx context.Context, channel, content string) error {
	s.mu.RLock()
	chatID, ok := s.chatIDs[channel]
	senderID := s.senderID
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrNoChatID, channel)
	}

	msg := bus.OutboundMessage{
		Content:   content,
		Channel:   channel,
		ChannelID: chatID,
		Timestamp: time.Now(),
		Metadata:  make(map[string]any),
		SenderID:  senderID,
	}

	// Try non-blocking publish first
	if s.bus.Publish(msg) {
		return nil
	}

	// Queue full, try with context timeout
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrQueueFull, ctx.Err())
	default:
		// Try blocking publish with a short timeout
		publishCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()

		err := s.bus.PublishBlocking(publishCtx, msg)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrQueueFull, err)
		}
		return nil
	}
}
