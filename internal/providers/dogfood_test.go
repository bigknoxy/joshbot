package providers

import (
	"encoding/json"
	"testing"
)

// TestDogfood_ByteIdenticalToNonStreaming verifies that the accumulator
// produces a ChatResponse that is byte-identical to what the non-streaming
// Chat path would return for the same content.
func TestDogfood_ByteIdenticalToNonStreaming(t *testing.T) {
	// Simulate a non-streaming ChatResponse (what the API would return)
	nonStreaming := &ChatResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "test-model",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "Hello world! I'll check the weather.",
					ToolCalls: []ToolCall{
						{
							Index: 0,
							ID:    "call_abc123",
							Type:  "function",
							Function: FunctionCall{
								Name:      "get_weather",
								Arguments: `{"location": "NYC"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		FinishReason: "tool_calls",
	}

	// Simulate the same response as streaming chunks
	chunks := []StreamChunk{
		chunk(textDelta(0, "Hello world! I'll check the weather.")),
		chunk(toolCallDelta(0, ToolCall{
			Index: 0,
			ID:    "call_abc123",
			Type:  "function",
			Function: FunctionCall{
				Name:      "get_weather",
				Arguments: `{"location": "NYC"}`,
			},
		})),
		chunk(finishDelta(0, "tool_calls")),
	}

	resp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Compare the two responses by JSON serialization (byte-identical check)
	nsJSON, err := json.Marshal(nonStreaming)
	if err != nil {
		t.Fatalf("failed to marshal non-streaming response: %v", err)
	}
	streamJSON, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal streaming response: %v", err)
	}

	if string(nsJSON) != string(streamJSON) {
		t.Errorf("responses are not byte-identical:\nnon-streaming: %s\nstreaming:     %s",
			nsJSON, streamJSON)
	}
}

// TestDogfood_RealSSEPattern verifies the accumulator against a realistic
// SSE chunk sequence that matches what OpenAI-compatible providers send.
func TestDogfood_RealSSEPattern(t *testing.T) {
	// This simulates a real OpenAI streaming response with:
	// 1. Role on first chunk
	// 2. Content deltas
	// 3. Tool call with fragmented arguments
	// 4. Finish reason on final chunk
	chunks := []StreamChunk{
		// First chunk: role + first content delta
		{
			ID:      "chatcmpl-123",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "gpt-4",
			Choices: []StreamChoice{
				{
					Index: 0,
					Delta: Message{
						Role:    "assistant",
						Content: "I'll help you with that.",
					},
				},
			},
		},
		// Content continuation
		{
			ID:      "chatcmpl-123",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "gpt-4",
			Choices: []StreamChoice{
				{
					Index: 0,
					Delta: Message{Content: " Let me check."},
				},
			},
		},
		// Tool call start
		{
			ID:      "chatcmpl-123",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "gpt-4",
			Choices: []StreamChoice{
				{
					Index: 0,
					Delta: Message{
						ToolCalls: []ToolCall{
							{
								Index: 0,
								ID:    "call_abc123",
								Type:  "function",
								Function: FunctionCall{
									Name:      "search_web",
									Arguments: "",
								},
							},
						},
					},
				},
			},
		},
		// Tool call arguments fragment 1
		{
			ID:      "chatcmpl-123",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "gpt-4",
			Choices: []StreamChoice{
				{
					Index: 0,
					Delta: Message{
						ToolCalls: []ToolCall{
							{
								Index: 0,
								Function: FunctionCall{
									Arguments: `{"query": "latest news"}`,
								},
							},
						},
					},
				},
			},
		},
		// Finish
		{
			ID:      "chatcmpl-123",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "gpt-4",
			Choices: []StreamChoice{
				{
					Index:        0,
					Delta:        Message{},
					FinishReason: "tool_calls",
				},
			},
		},
	}

	resp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "chatcmpl-123" {
		t.Errorf("ID = %q, want %q", resp.ID, "chatcmpl-123")
	}
	if resp.Object != "chat.completion" {
		t.Errorf("Object = %q, want %q", resp.Object, "chat.completion")
	}
	if resp.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", resp.Model, "gpt-4")
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "tool_calls")
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	choice := resp.Choices[0]
	if choice.Message.Role != "assistant" {
		t.Errorf("role = %q, want %q", choice.Message.Role, "assistant")
	}
	if choice.Message.Content != "I'll help you with that. Let me check." {
		t.Errorf("content = %q, want %q",
			choice.Message.Content, "I'll help you with that. Let me check.")
	}
	if choice.FinishReason != "tool_calls" {
		t.Errorf("choice finish_reason = %q, want %q",
			choice.FinishReason, "tool_calls")
	}

	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d",
			len(choice.Message.ToolCalls))
	}
	tc := choice.Message.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("tool call ID = %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Function.Name != "search_web" {
		t.Errorf("tool call name = %q, want %q", tc.Function.Name, "search_web")
	}
	if tc.Function.Arguments != `{"query": "latest news"}` {
		t.Errorf("tool call arguments = %q, want %q",
			tc.Function.Arguments, `{"query": "latest news"}`)
	}

	// Verify arguments are valid JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err != nil {
		t.Errorf("arguments are not valid JSON: %v", err)
	}
}
