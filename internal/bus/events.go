package bus

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType represents the type of bus event.
type EventType string

const (
	// EventMessage is a standard chat message.
	EventMessage EventType = "message"
	// EventTyping indicates the sender is typing.
	EventTyping EventType = "typing"
	// EventEdit indicates a message was edited.
	EventEdit EventType = "edit"
	// EventDelete indicates a message was deleted.
	EventDelete EventType = "delete"
	// EventCommand is a bot command.
	EventCommand EventType = "command"
)

// BusEvent is a wrapper around messages with additional metadata.
type BusEvent struct {
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Source    string          `json:"source"` // Channel name
	Data      json.RawMessage `json:"data"`   // Event-specific data
}

// EventData contains common event data fields.
type EventData struct {
	MessageID string         `json:"message_id,omitempty"`
	SenderID  string         `json:"sender_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// TypingEvent indicates a typing status change.
type TypingEvent struct {
	SenderID string `json:"sender_id"`
	Channel  string `json:"channel"`
	IsTyping bool   `json:"is_typing"`
}

// CommandEvent represents a bot command.
type CommandEvent struct {
	Command  string         `json:"command"`
	Args     string         `json:"args"`
	SenderID string         `json:"sender_id"`
	Channel  string         `json:"channel"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// String returns a string representation of the event.
func (e *BusEvent) String() string {
	return fmt.Sprintf("BusEvent{Type:%s Source:%s Time:%s}", e.Type, e.Source, e.Timestamp)
}

// NewBusEvent creates a new bus event with the current timestamp.
func NewBusEvent(eventType EventType, source string, data any) (*BusEvent, error) {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal event data: %w", err)
	}
	return &BusEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		Source:    source,
		Data:      dataBytes,
	}, nil
}

// ParseData unmarshals the event data into the given type.
func (e *BusEvent) ParseData(dest any) error {
	return json.Unmarshal(e.Data, dest)
}

// Topic constants for message routing.
const (
	// TopicAll is the wildcard topic that receives all messages.
	TopicAll = "all"
	// TopicAgent is the topic for messages destined for the agent loop.
	TopicAgent = "agent"
	// TopicCommands is the topic for command messages.
	TopicCommands = "commands"
	// TopicTelegram is the topic for Telegram channel messages.
	TopicTelegram = "telegram"
	// TopicCLI is the topic for CLI channel messages.
	TopicCLI = "cli"
	// TopicOutbound is the topic for outbound messages to channels.
	TopicOutbound = "outbound"
)

// MessageBuilder helps construct messages with defaults.
type MessageBuilder struct {
	msg InboundMessage
}

// NewMessageBuilder creates a new message builder.
func NewMessageBuilder() *MessageBuilder {
	return &MessageBuilder{
		msg: InboundMessage{
			Timestamp: time.Now(),
			Metadata:  make(map[string]any),
		},
	}
}

// WithSender sets the sender ID.
func (b *MessageBuilder) WithSender(senderID string) *MessageBuilder {
	b.msg.SenderID = senderID
	return b
}

// WithContent sets the message content.
func (b *MessageBuilder) WithContent(content string) *MessageBuilder {
	b.msg.Content = content
	return b
}

// WithChannel sets the channel.
func (b *MessageBuilder) WithChannel(channel string) *MessageBuilder {
	b.msg.Channel = channel
	return b
}

// WithMetadata adds metadata entries.
func (b *MessageBuilder) WithMetadata(key string, value any) *MessageBuilder {
	b.msg.Metadata[key] = value
	return b
}

// WithMetadataMap sets the entire metadata map.
func (b *MessageBuilder) WithMetadataMap(m map[string]any) *MessageBuilder {
	b.msg.Metadata = m
	return b
}

// WithTimestamp sets a specific timestamp.
func (b *MessageBuilder) WithTimestamp(t time.Time) *MessageBuilder {
	b.msg.Timestamp = t
	return b
}

// Build returns the constructed message.
func (b *MessageBuilder) Build() InboundMessage {
	return b.msg
}

// OutboundBuilder helps construct outbound messages with defaults.
type OutboundBuilder struct {
	msg OutboundMessage
}

// NewOutboundBuilder creates a new outbound message builder.
func NewOutboundBuilder() *OutboundBuilder {
	return &OutboundBuilder{
		msg: OutboundMessage{
			Timestamp: time.Now(),
			Metadata:  make(map[string]any),
		},
	}
}

// WithContent sets the message content.
func (b *OutboundBuilder) WithContent(content string) *OutboundBuilder {
	b.msg.Content = content
	return b
}

// WithChannel sets the target channel.
func (b *OutboundBuilder) WithChannel(channel string) *OutboundBuilder {
	b.msg.Channel = channel
	return b
}

// WithChannelID sets the channel ID for response routing.
func (b *OutboundBuilder) WithChannelID(channelID string) *OutboundBuilder {
	b.msg.ChannelID = channelID
	return b
}

// WithMetadata adds metadata entries.
func (b *OutboundBuilder) WithMetadata(key string, value any) *OutboundBuilder {
	b.msg.Metadata[key] = value
	return b
}

// WithSenderID sets the original sender ID for reference.
func (b *OutboundBuilder) WithSenderID(senderID string) *OutboundBuilder {
	b.msg.SenderID = senderID
	return b
}

// Build returns the constructed outbound message.
func (b *OutboundBuilder) Build() OutboundMessage {
	return b.msg
}
