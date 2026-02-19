package session

import (
	"encoding/json"
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
	Role      Role       `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Timestamp time.Time  `json:"timestamp"`
}

// Session represents a conversation session.
type Session struct {
	ID        string    `json:"id"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
