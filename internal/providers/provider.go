package providers

import (
	"context"
	"encoding/json"
	"time"
)

// MessageRole represents the role of a message in a conversation.
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// Content represents the content of a message.
type Content struct {
	// Type is the type of content (text, image_url, etc.)
	Type string `json:"type"`
	// Text is the text content (for text type)
	Text string `json:"text,omitempty"`
	// ImageURL is the image URL (for image_url type)
	ImageURL *ImageURL `json:"image_url,omitempty"`
	// File is the attached file (for file type)
	File *FileContent `json:"file,omitempty"`
}

// FileContent represents a file attached to message content.
//
// The shape is OpenAI's Chat Completions "file" content part: a `file` object
// carrying `filename` and `file_data`, where `file_data` is a data: URL. See
// https://developers.openai.com/api/docs/api-reference/chat/create. Every
// provider joshbot talks to is dialled through this one OpenAI-compatible
// serialization point, Anthropic included, so there is deliberately no
// per-provider document path.
type FileContent struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"`
}

// ImageURL represents an image URL in message content.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// Message represents a message in a conversation.
type Message struct {
	// Role is the role of the message sender
	Role MessageRole `json:"role"`
	// Content is the content of the message
	Content string `json:"content"`
	// Name is the name of the sender (for tool messages)
	Name string `json:"name,omitempty"`
	// ToolCallID is the ID of the tool call this message responds to
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCalls is the list of tool calls made by the assistant
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Images are attachments carried alongside Content. The field is excluded
	// from the struct tags and handled by MarshalJSON instead, because the wire
	// format is not "a field next to content" — an image turns content itself
	// from a string into an array of parts.
	Images []Image `json:"-"`
	// Documents are non-image attachments carried alongside Content, excluded
	// from the struct tags for exactly the same reason as Images: a document
	// turns content itself from a string into an array of parts.
	Documents []Document `json:"-"`
}

// MarshalJSON serialises a message in the OpenAI-compatible shape.
//
// A message with no attachments must produce byte-identical JSON to what it produced
// before images existed: every provider joshbot talks to receives this, and a
// stray field or a content array where a string was expected is a 400 on every
// request rather than a visible failure here. That is why the no-image path is
// the zero-work path — it marshals the plain struct through an alias and adds
// nothing.
//
// With attachments, content becomes an array of parts: the text first (omitted
// entirely when empty, since a part with an empty string is not valid), then
// one image_url part per image carrying a data: URL, then one file part per
// document carrying the same.
func (m Message) MarshalJSON() ([]byte, error) {
	type wireMessage Message // alias: no MarshalJSON, so no recursion
	if len(m.Images) == 0 && len(m.Documents) == 0 {
		return json.Marshal(wireMessage(m))
	}
	parts := make([]Content, 0, len(m.Images)+len(m.Documents)+1)
	if m.Content != "" {
		parts = append(parts, Content{Type: "text", Text: m.Content})
	}
	for _, im := range m.Images {
		parts = append(parts, Content{Type: "image_url", ImageURL: &ImageURL{URL: im.DataURL()}})
	}
	for _, d := range m.Documents {
		parts = append(parts, Content{Type: "file", File: &FileContent{Filename: d.Filename(), FileData: d.DataURL()}})
	}
	return json.Marshal(struct {
		wireMessage
		Content []Content `json:"content"`
	}{wireMessage: wireMessage(m), Content: parts})
}

// ToolCall represents a tool call made by the model.
type ToolCall struct {
	// Index identifies which tool call this fragment belongs to in a
	// streaming response. Present only in StreamChoice.Delta.ToolCalls;
	// zero in non-streaming responses.
	Index int `json:"index,omitempty"`
	// ID is the unique identifier for this tool call
	ID string `json:"id,omitempty"`
	// Type is the type of tool call (function)
	Type string `json:"type,omitempty"`
	// Function is the function call details
	Function FunctionCall `json:"function"`
}

// FunctionCall represents a function call within a tool call.
type FunctionCall struct {
	// Name is the name of the function to call
	Name string `json:"name,omitempty"`
	// Arguments is the JSON string of arguments to pass to the function
	Arguments string `json:"arguments,omitempty"`
}

// Tool represents a tool that can be called by the model.
type Tool struct {
	// Type is the type of tool (function)
	Type string `json:"type"`
	// Function is the function definition
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition represents the definition of a function tool.
type FunctionDefinition struct {
	// Name is the name of the function
	Name string `json:"name"`
	// Description is a description of what the function does
	Description string `json:"description"`
	// Parameters is the JSON schema for the function parameters
	Parameters *json.RawMessage `json:"parameters"`
}

// ChatRequest represents a request to the chat endpoint.
type ChatRequest struct {
	// Model is the model to use
	Model string `json:"model"`
	// Messages is the list of messages in the conversation
	Messages []Message `json:"messages"`
	// Temperature is the sampling temperature (0-2)
	Temperature float64 `json:"temperature,omitempty"`
	// MaxTokens is the maximum number of tokens to generate
	MaxTokens int `json:"max_tokens,omitempty"`
	// TopP is the nucleus sampling parameter
	TopP float64 `json:"top_p,omitempty"`
	// Tools is the list of tools available to the model
	Tools []Tool `json:"tools,omitempty"`
	// ToolChoice controls which tool is called (auto, none, or specific)
	ToolChoice any `json:"tool_choice,omitempty"`
	// Stream enables streaming responses
	Stream bool `json:"stream,omitempty"`
	// Stop is a list of stop sequences
	Stop []string `json:"stop,omitempty"`
	// PresencePenalty penalizes new tokens based on their presence
	PresencePenalty float64 `json:"presence_penalty,omitempty"`
	// FrequencyPenalty penalizes new tokens based on their frequency
	FrequencyPenalty float64 `json:"frequency_penalty,omitempty"`
	// User is a user identifier for tracking
	User string `json:"user,omitempty"`
}

// ChatResponse represents a response from the chat endpoint.
type ChatResponse struct {
	// ID is the unique identifier for this response
	ID string `json:"id"`
	// Object is the object type (chat.completion)
	Object string `json:"object"`
	// Created is the Unix timestamp of when the response was created
	Created int64 `json:"created"`
	// Model is the model used
	Model string `json:"model"`
	// Choices is the list of choices (alternatives)
	Choices []Choice `json:"choices"`
	// Usage is the token usage information
	Usage Usage `json:"usage"`
	// FinishReason is why the response finished (stop, length, content_filter, etc.)
	FinishReason string `json:"finish_reason,omitempty"`
}

// Choice represents one of the choices in a chat response.
type Choice struct {
	// Index is the index of this choice in the list
	Index int `json:"index"`
	// Message is the assistant's message
	Message Message `json:"message"`
	// Delta is the streaming message delta (for streaming responses)
	Delta *Message `json:"delta,omitempty"`
	// FinishReason is why this choice finished
	FinishReason string `json:"finish_reason,omitempty"`
}

// Usage represents token usage information.
type Usage struct {
	// PromptTokens is the number of tokens in the prompt
	PromptTokens int `json:"prompt_tokens"`
	// CompletionTokens is the number of tokens in the completion
	CompletionTokens int `json:"completion_tokens"`
	// TotalTokens is the total number of tokens
	TotalTokens int `json:"total_tokens"`
}

// StreamChunk represents a chunk of a streaming response.
type StreamChunk struct {
	// ID is the unique identifier for this response
	ID string `json:"id"`
	// Object is the object type (chat.completion.chunk)
	Object string `json:"object"`
	// Created is the Unix timestamp
	Created int64 `json:"created"`
	// Model is the model used
	Model string `json:"model"`
	// Choices is the list of choice deltas
	Choices []StreamChoice `json:"choices"`
}

// StreamChoice represents a streaming choice delta.
type StreamChoice struct {
	// Index is the index of this choice
	Index int `json:"index"`
	// Delta is the message delta
	Delta Message `json:"delta"`
	// FinishReason is why this chunk finished (if applicable)
	FinishReason string `json:"finish_reason,omitempty"`
}

// Config holds configuration for a provider.
type Config struct {
	// APIKey is the API key for authentication
	APIKey string
	// APIBase is the base URL for the API
	APIBase string
	// ExtraHeaders are additional headers to include in requests
	ExtraHeaders map[string]string
	// ExtraBody are additional JSON body fields merged into requests
	ExtraBody map[string]any
	// Timeout is the request timeout (default 120 seconds)
	Timeout time.Duration
	// Model is the default model to use
	Model string
	// MaxTokens is the default max tokens
	MaxTokens int
	// Temperature is the default temperature
	Temperature float64
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() Config {
	return Config{
		Timeout:     120 * time.Second,
		MaxTokens:   8192,
		Temperature: 0.7,
	}
}

// Provider is the interface for LLM providers.
type Provider interface {
	// Chat sends a chat request and returns a chat response.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// ChatStream sends a chat request and returns a channel of stream chunks.
	// The channel will be closed when the stream is complete.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)

	// Transcribe transcribes audio data and returns the transcription.
	Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error)

	// Name returns the name of the provider.
	Name() string

	// Config returns the current provider configuration.
	Config() Config
}

// StreamHandler is a callback for handling streaming chunks.
type StreamHandler func(chunk StreamChunk) error
