package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Role represents the role of a message sender.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// ToolCall represents a tool invocation within a message.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Result    string          `json:"result,omitempty"`
}

// Message represents a single message in a session.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Timestamp  time.Time  `json:"timestamp"`
}

// Session represents a conversation session.
type Session struct {
	ID                  string            `json:"id"`
	Messages            []Message         `json:"messages"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	ConversationTopic   string            `json:"conversation_topic,omitempty"`
	ConversationContext map[string]string `json:"conversation_context,omitempty"`
}

// NewSession creates a new session with the given ID.
func NewSession(id string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:        id,
		Messages:  make([]Message, 0),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// AddMessage adds a new message to the session.
func (s *Session) AddMessage(msg Message) {
	s.Messages = append(s.Messages, msg)
	s.UpdatedAt = time.Now().UTC()
}

// GetMessages returns all messages in the session.
func (s *Session) GetMessages() []Message {
	return s.Messages
}

// LastMessages returns the last n messages from the session.
// If n is greater than the number of messages, returns all messages.
func (s *Session) LastMessages(n int) []Message {
	if n <= 0 {
		return []Message{}
	}
	if n >= len(s.Messages) {
		return s.Messages
	}
	return s.Messages[len(s.Messages)-n:]
}

// SetTopic summarizes the current conversation intent/topic.
// Called each turn to maintain a running understanding of the conversation.
func (s *Session) SetTopic(topic string) {
	existing := s.ConversationTopic
	if existing != "" && topic != existing {
		s.ConversationTopic = topic
	}
	if existing == "" && topic != "" {
		s.ConversationTopic = topic
	}
}

// UpdateContext stores a key-value piece of conversation context (e.g., user preference, decision).
func (s *Session) UpdateContext(key, value string) {
	if s.ConversationContext == nil {
		s.ConversationContext = make(map[string]string)
	}
	s.ConversationContext[key] = value
}

// ConversationSummary returns a short summary of the ongoing conversation for prompt injection.
func (s *Session) ConversationSummary() string {
	if s.ConversationTopic == "" && len(s.ConversationContext) == 0 {
		return ""
	}
	var b strings.Builder
	if s.ConversationTopic != "" {
		b.WriteString(fmt.Sprintf("Current topic: %s", s.ConversationTopic))
	}
	if len(s.ConversationContext) > 0 {
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		b.WriteString("Context: ")
		first := true
		for k, v := range s.ConversationContext {
			if !first {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%s: %s", k, v))
			first = false
		}
	}
	return b.String()
}

// MarshalJSON implements custom JSON marshaling for Session.
func (s Session) MarshalJSON() ([]byte, error) {
	type Alias Session
	return json.Marshal(&struct {
		Alias
	}{
		Alias: Alias(s),
	})
}

// UnmarshalJSON implements custom JSON unmarshaling for Session.
func (s *Session) UnmarshalJSON(data []byte) error {
	type Alias Session
	aux := &struct {
		Alias
	}{
		Alias: Alias{},
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	s.ID = aux.ID
	s.Messages = aux.Messages
	s.CreatedAt = aux.CreatedAt
	s.UpdatedAt = aux.UpdatedAt
	return nil
}

// MessageToJSONL converts a Message to a JSON line for storage.
func MessageToJSONL(msg Message) ([]byte, error) {
	return json.Marshal(msg)
}

// MessageFromJSONL parses a Message from a JSON line.
func MessageFromJSONL(data []byte) (Message, error) {
	var msg Message
	err := json.Unmarshal(data, &msg)
	return msg, err
}
