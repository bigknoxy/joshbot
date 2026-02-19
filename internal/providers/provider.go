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
}

// ToolCall represents a tool call made by the model.
type ToolCall struct {
	// ID is the unique identifier for this tool call
	ID string `json:"id"`
	// Type is the type of tool call (function)
	Type string `json:"type"`
	// Function is the function call details
	Function FunctionCall `json:"function"`
}

// FunctionCall represents a function call within a tool call.
type FunctionCall struct {
	// Name is the name of the function to call
	Name string `json:"name"`
	// Arguments is the JSON string of arguments to pass to the function
	Arguments string `json:"arguments"`
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

// ToolCallRequest represents a request to execute a tool call.
type ToolCallRequest struct {
	// ToolCallID is the ID of the tool call to execute
	ToolCallID string `json:"tool_call_id"`
	// FunctionName is the name of the function to call
	FunctionName string `json:"function_name"`
	// Arguments is the arguments to pass to the function
	Arguments map[string]any `json:"arguments"`
}

// ToolCallResponse represents the result of executing a tool call.
type ToolCallResponse struct {
	// ToolCallID is the ID of the tool call this response is for
	ToolCallID string `json:"tool_call_id"`
	// Content is the result of the tool call (or error message)
	Content string `json:"content"`
	// Error is an error message if the tool call failed
	Error string `json:"error,omitempty"`
	// IsError indicates if this is an error response
	IsError bool `json:"is_error,omitempty"`
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

// ChatWithTools is a helper that executes tool calls in a loop until
// the model returns a final response without tool calls.
type ChatWithTools interface {
	// ExecuteTool executes a tool call and returns the result.
	ExecuteTool(ctx context.Context, req ToolCallRequest) (*ToolCallResponse, error)
}
