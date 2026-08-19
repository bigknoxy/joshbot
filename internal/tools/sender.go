// Package tools provides the tool system for joshbot's agent.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
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
	// persistPath, when set, makes the chat-id map survive restarts — see
	// EnablePersistence.
	persistPath string
}

// NewBusMessageSender creates a new BusMessageSender.
func NewBusMessageSender(messageBus *bus.MessageBus) *BusMessageSender {
	return &BusMessageSender{
		chatIDs: make(map[string]string),
		bus:     messageBus,
	}
}

// SetChatID stores a chat ID for a channel, writing through to the persisted
// map when persistence is enabled and the value changed.
func (s *BusMessageSender) SetChatID(channel, chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chatIDs[channel] == chatID {
		return
	}
	s.chatIDs[channel] = chatID
	if err := s.persistLocked(); err != nil {
		log.Warn("failed to persist chat ids", "error", err)
	}
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

	return s.publish(ctx, msg)
}

// publish is the one delivery path every proactive send takes.
func (s *BusMessageSender) publish(ctx context.Context, msg bus.OutboundMessage) error {
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

// SendFile publishes an outbound message carrying one attachment, with caption
// as its text. It is deliberately the same publish path as SendMessage — the
// queue-full handling, the chat-id lookup and the sender id are identical, and
// only the payload differs — so an attachment can never take a delivery route
// that has not been exercised by ordinary text.
func (s *BusMessageSender) SendFile(ctx context.Context, channel string, att Attachment, caption string) error {
	s.mu.RLock()
	chatID, ok := s.chatIDs[channel]
	senderID := s.senderID
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrNoChatID, channel)
	}

	return s.publish(ctx, bus.OutboundMessage{
		Content:     caption,
		Channel:     channel,
		ChannelID:   chatID,
		Timestamp:   time.Now(),
		Metadata:    make(map[string]any),
		SenderID:    senderID,
		Attachments: []bus.Attachment{att},
	})
}

// EnablePersistence makes the chat-id map survive restarts: existing entries
// are loaded from path now, and every SetChatID that changes a value is
// written through atomically (0600 — chat ids identify the operator's chats).
//
// This exists for cron and the heartbeat: their inbound messages carry no
// chat id, so a proactive reply resolves the recipient from this map — which
// used to be empty after every gateway restart until the user happened to
// speak first, silently swallowing any reminder that fired before then.
func (s *BusMessageSender) EnablePersistence(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistPath = path

	stored := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read chat ids: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &stored); err != nil {
			// A damaged file must not take the gateway down; it just means
			// no recall until the user speaks. Treat it as empty — the
			// merge write-back below replaces it if memory holds anything.
			stored = map[string]string{}
		}
	}
	for channel, id := range stored {
		if _, live := s.chatIDs[channel]; !live {
			s.chatIDs[channel] = id
		}
	}
	// Write the merged map back unless the file already matches it —
	// otherwise an id set before enablement (which SetChatID's unchanged-skip
	// will never persist later) would not survive the next restart.
	if !mapsEqual(s.chatIDs, stored) {
		if err := s.persistLocked(); err != nil {
			return fmt.Errorf("failed to persist merged chat ids: %w", err)
		}
	}
	return nil
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// persistLocked writes the map through to disk. Callers hold s.mu. Failures
// are returned for logging but never block the send path.
func (s *BusMessageSender) persistLocked() error {
	if s.persistPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.chatIDs, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.persistPath), ".chat_ids-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, s.persistPath)
}
