package session

import (
	"encoding/json"
	"fmt"
	"regexp"
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

	// Compaction marks this message as a stored context-compaction record: an
	// LLM-generated summary that stands in for the messages it replaced.
	//
	// It exists so the summary is computed once and then reused. Without it the
	// agent recomputed a summary of the whole history on every turn past the
	// compaction threshold, paying an extra provider round-trip forever while
	// the session file kept growing (issue #125).
	//
	// A session holds at most one of these, always at index 0. A later
	// compaction summarizes the existing record together with everything after
	// it and replaces it, so the count never grows.
	Compaction bool `json:"compaction,omitempty"`
}

// IsCompaction reports whether m is a stored compaction record.
func (m Message) IsCompaction() bool { return m.Compaction }

// CompactionRecord returns the session's compaction record and true when one is
// present. It is always the first message, so a session that starts with
// anything else has none.
func (s *Session) CompactionRecord() (Message, bool) {
	if len(s.Messages) > 0 && s.Messages[0].Compaction {
		return s.Messages[0], true
	}
	return Message{}, false
}

// CountCompactionRecords returns how many compaction records the session holds.
// The invariant is that this is never greater than 1; tests assert it.
func (s *Session) CountCompactionRecords() int {
	n := 0
	for _, m := range s.Messages {
		if m.Compaction {
			n++
		}
	}
	return n
}

// NewCompactionRecord builds a compaction record carrying the given summary.
//
// The role is deliberately RoleUser: an OpenAI-compatible provider treats a
// second system message inconsistently, and the summary must survive the same
// path as ordinary history. The envelope tags are what the model sees.
func NewCompactionRecord(summary string) Message {
	return Message{
		Role:       RoleUser,
		Content:    CompactionEnvelope(summary),
		Timestamp:  time.Now().UTC(),
		Compaction: true,
	}
}

// CompactionEnvelope wraps a summary in the tags the model is shown.
func CompactionEnvelope(summary string) string {
	return "<ctx_compress>\n" + summary + "\n</ctx_compress>"
}

// Session represents a conversation session.
type Session struct {
	ID                  string            `json:"id"`
	Messages            []Message         `json:"messages"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	ConversationTopic   string            `json:"conversation_topic,omitempty"`
	ConversationContext map[string]string `json:"conversation_context,omitempty"`
	// ModelOverride, when set, pins this session to a specific model name or
	// provider:model spec instead of the configured default. Persisted per
	// session so a /model switch survives restarts but never affects other
	// chats. Cleared by /new.
	ModelOverride string `json:"model_override,omitempty"`
	// Personality, when set, is an instruction appended to the system prompt
	// for this session only. Cleared by /new.
	Personality string `json:"personality,omitempty"`
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

// ExtractUserFacts scans a user message for self-descriptive patterns and stores them in ConversationContext.
// Recognized patterns: "I'm/I am [name]", "I work at [org]", "I'm a [role]", "my name is [name]", etc.
func (s *Session) ExtractUserFacts(msg string) {
	if s.ConversationContext == nil {
		s.ConversationContext = make(map[string]string)
	}
	lower := strings.ToLower(strings.TrimSpace(msg))

	// "my name is X" or "I'm X" (only if X looks like a name)
	if m := namePattern.FindStringSubmatch(lower); m != nil {
		if _, exists := s.ConversationContext["user_name"]; !exists {
			s.ConversationContext["user_name"] = strings.Title(m[1])
		}
	}

	// "I work at X" or "I'm at X"
	if m := orgPattern.FindStringSubmatch(lower); m != nil {
		org := strings.TrimSpace(m[1])
		if len(org) > 2 {
			s.ConversationContext["organization"] = org
		}
	}

	// "I'm a [role]" or "I'm an [role]"
	if m := rolePattern.FindStringSubmatch(lower); m != nil {
		role := strings.TrimSpace(m[1])
		if len(role) > 2 {
			s.ConversationContext["role"] = role
		}
	}
}

var (
	namePattern = regexp.MustCompile(`my name is (\w[\w\s]{0,20}\w)`)
	orgPattern  = regexp.MustCompile(`i work at ([\w\s]{2,40}?)(?:\.|,|$)`)
	rolePattern = regexp.MustCompile(`i'm (?:an?|a) ([\w\s]{2,40}?)(?:\.|,|$)`)
)

// ClearMessages removes all conversation messages but preserves conversation context and topic.
func (s *Session) ClearMessages() {
	s.Messages = make([]Message, 0)
	s.UpdatedAt = time.Now().UTC()
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
