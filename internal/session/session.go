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

	// Images records the attachments that arrived with this message.
	//
	// It stores descriptors, never the image bytes. The whole session file is
	// rewritten on every turn, so persisting the bytes would rewrite megabytes
	// per turn for the rest of the conversation — the same growth issue #125
	// addressed for summaries — and would put user images on disk in a file the
	// redactor deliberately does not rewrite. The consequence is deliberate and
	// worth knowing: a reloaded session remembers that an image was sent and
	// what it was, but cannot re-send it to the model.
	Images []ImageRef `json:"images,omitempty"`

	// Documents records the document attachments that arrived with this
	// message, on exactly the terms Images does: descriptors, never the bytes.
	// A PDF is more sensitive than a photo, not less — session JSONL is
	// deliberately exempt from redaction and relies on its 0600 mode — so the
	// rule is if anything stricter here.
	Documents []DocumentRef `json:"documents,omitempty"`
}

// DocumentRef is the persisted record of a document attachment: enough to show
// what was sent and to reason about it later, without the bytes. It is the
// sibling of ImageRef and carries the same fields for the same reasons.
type DocumentRef struct {
	// Label is what the sender called it — a filename, usually. It is
	// untrusted text and never used to open anything.
	Label string `json:"label,omitempty"`
	// MIME is the type detected by sniffing the content when it arrived.
	MIME string `json:"mime"`
	// Bytes is the decoded size, so the record stays meaningful for limits.
	Bytes int `json:"bytes"`
	// SHA256 identifies the document across turns without storing it.
	SHA256 string `json:"sha256,omitempty"`
}

// ImageRef is the persisted record of an image attachment: enough to show what
// was sent and to reason about it later, without the bytes.
type ImageRef struct {
	// Label is what the sender called it — a filename, or the channel's own
	// description. It is untrusted text and never used to open anything.
	Label string `json:"label,omitempty"`
	// MIME is the type detected by sniffing the content when it arrived.
	MIME string `json:"mime"`
	// Bytes is the decoded size, so the record stays meaningful for limits.
	Bytes int `json:"bytes"`
	// SHA256 identifies the image across turns without storing it.
	SHA256 string `json:"sha256,omitempty"`
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
	// Checkpoint is set when the agent hits its iteration limit, recording
	// enough state for /resume to pick up where the run stopped.
	// Cleared when the user sends a new message (not /resume) or /new.
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
}

// Checkpoint records where a ReAct loop was interrupted by the iteration
// limit, so /resume can restart from the accumulated session state.
type Checkpoint struct {
	// Iteration is the iteration count at which the loop stopped.
	Iteration int `json:"iteration"`
	// MaxIterations is the limit that was hit.
	MaxIterations int `json:"max_iterations"`
	// CreatedAt is when the checkpoint was saved.
	CreatedAt time.Time `json:"created_at"`
	// UserMessage is the original user message that drove the run,
	// preserved so /resume can re-attach context if needed.
	UserMessage string `json:"user_message,omitempty"`
	// RemainingTokens is the token budget left at checkpoint time
	// (informational, not enforced).
	RemainingTokens int `json:"remaining_tokens,omitempty"`
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
	s.ConversationTopic = aux.ConversationTopic
	s.ConversationContext = aux.ConversationContext
	s.ModelOverride = aux.ModelOverride
	s.Personality = aux.Personality
	s.Checkpoint = aux.Checkpoint
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
