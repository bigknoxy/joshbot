// Package api serves an OpenAI-compatible HTTP surface in front of the joshbot
// agent, so any tool that speaks the OpenAI chat API can use joshbot as a
// backend.
//
// The semantics are agent-as-model, not proxy-as-model: a request to
// /v1/chat/completions runs the full ReAct loop with tools, memory and skills,
// and the answer is joshbot's, not a pass-through of some upstream provider.
// That is also why exactly one model id is served — see ModelID.
//
// Because a caller reaching this endpoint reaches the shell and filesystem
// tools, authentication is mandatory and the default bind address is loopback.
// There is no unauthenticated mode.
package api

import "github.com/bigknoxy/joshbot/internal/providers"

// ModelID is the single model this server advertises. joshbot chooses its own
// backing provider and model from config, so there is nothing for the caller to
// select: the assistant is the model. The `model` field of an incoming request
// is accepted and ignored rather than validated, because rejecting it would
// break every client that hardcodes "gpt-4" — being a drop-in replacement is
// the whole point of the endpoint.
const ModelID = "joshbot"

// chatRequest is the subset of the OpenAI chat-completions request joshbot acts
// on. Unknown fields are ignored rather than rejected, for the same drop-in
// reason as the model field: real clients send sampling parameters that an
// agent loop has no way to honour.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	// User identifies the caller. It becomes the sender half of joshbot's
	// session key, so two callers passing different values get separate
	// conversations and separate memory. It is attacker-controlled and is
	// validated before it reaches a session path.
	User string `json:"user"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is an OpenAI chat.completion object.
type chatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   usage        `json:"usage"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// chunk is an OpenAI chat.completion.chunk object, one SSE frame of a stream.
type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	// Usage rides the final frame only, matching OpenAI's stream_options
	// behaviour. It is omitted elsewhere so clients that sum every frame do not
	// double-count.
	Usage *usage `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int    `json:"index"`
	Delta        delta  `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

type delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func usageFrom(u providers.Usage) usage {
	return usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// modelsResponse is an OpenAI list-models object.
type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// errorEnvelope is the OpenAI error shape. Clients parse it, so joshbot's own
// errors are reported through it rather than as bare text.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}
