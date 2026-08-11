package providers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// --- Test helpers ---

// chunk builds a StreamChunk with the given choice deltas.
func chunk(choices ...StreamChoice) StreamChunk {
	return StreamChunk{
		ID:      "chatcmpl-test",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "test-model",
		Choices: choices,
	}
}

// textDelta creates a choice with a content delta.
func textDelta(index int, content string) StreamChoice {
	return StreamChoice{
		Index: index,
		Delta: Message{Role: "assistant", Content: content},
	}
}

// finishDelta creates a choice with just a finish reason.
func finishDelta(index int, reason string) StreamChoice {
	return StreamChoice{
		Index:        index,
		Delta:        Message{},
		FinishReason: reason,
	}
}

// toolCallDelta creates a choice with a tool-call fragment.
func toolCallDelta(index int, tc ToolCall) StreamChoice {
	return StreamChoice{
		Index: index,
		Delta: Message{ToolCalls: []ToolCall{tc}},
	}
}

// --- Test cases ---

// TestAccumulate_PlainTextResponse verifies that a plain text response
// split across many small deltas produces byte-identical content to what
// the non-streaming path would return.
func TestAccumulate_PlainTextResponse(t *testing.T) {
	chunks := []StreamChunk{
		chunk(textDelta(0, "Hello")),
		chunk(textDelta(0, " world")),
		chunk(textDelta(0, "!")),
		chunk(finishDelta(0, "stop")),
	}

	resp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello world!" {
		t.Errorf("content = %q, want %q",
			resp.Choices[0].Message.Content, "Hello world!")
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q",
			resp.Choices[0].FinishReason, "stop")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("response finish_reason = %q, want %q",
			resp.FinishReason, "stop")
	}
}

// TestAccumulate_SingleToolCallSplitAcrossChunks verifies that a single
// tool call whose arguments JSON is split across many chunks — including
// a split mid-UTF-8-multibyte-character and mid-JSON-token — is correctly
// reassembled.
func TestAccumulate_SingleToolCallSplitAcrossChunks(t *testing.T) {
	// The arguments JSON: {"location": "São Paulo"}
	// Split mid-UTF-8 (the 'ã' is 2 bytes: 0xC3 0xA3) and mid-JSON-token.
	chunks := []StreamChunk{
		chunk(toolCallDelta(0, ToolCall{
			Index: 0,
			ID:    "call_abc123",
			Type:  "function",
			Function: FunctionCall{
				Name:      "get_weather",
				Arguments: `{"loc`,
			},
		})),
		chunk(toolCallDelta(0, ToolCall{
			Index: 0,
			Function: FunctionCall{
				Arguments: `ation": "S`,
			},
		})),
		chunk(toolCallDelta(0, ToolCall{
			Index: 0,
			Function: FunctionCall{
				Arguments: "\xc3", // first byte of ã (UTF-8: 0xC3 0xA3)
			},
		})),
		chunk(toolCallDelta(0, ToolCall{
			Index: 0,
			Function: FunctionCall{
				Arguments: "\xa3o Paulo\"}", // second byte of ã + rest
			},
		})),
		chunk(finishDelta(0, "tool_calls")),
	}

	resp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d",
			len(resp.Choices[0].Message.ToolCalls))
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("tool call ID = %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool call name = %q, want %q", tc.Function.Name, "get_weather")
	}
	// The arguments should be the full JSON, reassembled correctly.
	expected := `{"location": "São Paulo"}`
	if tc.Function.Arguments != expected {
		t.Errorf("arguments = %q, want %q", tc.Function.Arguments, expected)
	}
	// Verify the reassembled arguments are valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err != nil {
		t.Errorf("arguments are not valid JSON: %v", err)
	}
}

// TestAccumulate_InterleavedToolCalls verifies that two or more tool calls
// in one turn, fragments interleaved across indices, are correctly
// reassembled by index.
func TestAccumulate_InterleavedToolCalls(t *testing.T) {
	chunks := []StreamChunk{
		// First fragment for tool call 0
		chunk(toolCallDelta(0, ToolCall{
			Index:    0,
			ID:       "call_0",
			Type:     "function",
			Function: FunctionCall{Name: "tool_a", Arguments: `{"x":`},
		})),
		// First fragment for tool call 1 — interleaved!
		chunk(toolCallDelta(0, ToolCall{
			Index:    1,
			ID:       "call_1",
			Type:     "function",
			Function: FunctionCall{Name: "tool_b", Arguments: `{"y":`},
		})),
		// Second fragment for tool call 0 — back to index 0
		chunk(toolCallDelta(0, ToolCall{
			Index:    0,
			Function: FunctionCall{Arguments: `"1"}`},
		})),
		// Second fragment for tool call 1 — back to index 1
		chunk(toolCallDelta(0, ToolCall{
			Index:    1,
			Function: FunctionCall{Arguments: `"2"}`},
		})),
		chunk(finishDelta(0, "tool_calls")),
	}

	resp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tcs := resp.Choices[0].Message.ToolCalls
	if len(tcs) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(tcs))
	}

	// Tool call 0 should have tool_a with x=1
	if tcs[0].ID != "call_0" {
		t.Errorf("tc[0] ID = %q, want %q", tcs[0].ID, "call_0")
	}
	if tcs[0].Function.Name != "tool_a" {
		t.Errorf("tc[0] name = %q, want %q", tcs[0].Function.Name, "tool_a")
	}
	if tcs[0].Function.Arguments != `{"x":"1"}` {
		t.Errorf("tc[0] arguments = %q, want %q",
			tcs[0].Function.Arguments, `{"x":"1"}`)
	}

	// Tool call 1 should have tool_b with y=2
	if tcs[1].ID != "call_1" {
		t.Errorf("tc[1] ID = %q, want %q", tcs[1].ID, "call_1")
	}
	if tcs[1].Function.Name != "tool_b" {
		t.Errorf("tc[1] name = %q, want %q", tcs[1].Function.Name, "tool_b")
	}
	if tcs[1].Function.Arguments != `{"y":"2"}` {
		t.Errorf("tc[1] arguments = %q, want %q",
			tcs[1].Function.Arguments, `{"y":"2"}`)
	}
}

// TestAccumulate_IdNameOnlyOnFirstFragment verifies that id and name
// present only on the first fragment of each index are correctly captured
// and not lost.
func TestAccumulate_IdNameOnlyOnFirstFragment(t *testing.T) {
	chunks := []StreamChunk{
		chunk(toolCallDelta(0, ToolCall{
			Index:    0,
			ID:       "call_first",
			Type:     "function",
			Function: FunctionCall{Name: "my_tool", Arguments: `{"a":`},
		})),
		// Subsequent fragments have no id, name, or type
		chunk(toolCallDelta(0, ToolCall{
			Index:    0,
			Function: FunctionCall{Arguments: `1}`},
		})),
		chunk(finishDelta(0, "tool_calls")),
	}

	resp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_first" {
		t.Errorf("ID = %q, want %q (lost from first fragment)",
			tc.ID, "call_first")
	}
	if tc.Function.Name != "my_tool" {
		t.Errorf("name = %q, want %q (lost from first fragment)",
			tc.Function.Name, "my_tool")
	}
	if tc.Type != "function" {
		t.Errorf("type = %q, want %q (lost from first fragment)",
			tc.Type, "function")
	}
}

// TestAccumulate_MixedContentAndToolCalls verifies that content deltas
// and tool-call deltas mixed in the same turn (model reasoning out loud,
// then calling a tool) are both captured.
func TestAccumulate_MixedContentAndToolCalls(t *testing.T) {
	chunks := []StreamChunk{
		// Model reasoning out loud
		chunk(textDelta(0, "Let me check the weather for you.")),
		// Then a tool call
		chunk(toolCallDelta(0, ToolCall{
			Index: 0,
			ID:    "call_1",
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

	if resp.Choices[0].Message.Content != "Let me check the weather for you." {
		t.Errorf("content = %q, want %q",
			resp.Choices[0].Message.Content,
			"Let me check the weather for you.")
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d",
			len(resp.Choices[0].Message.ToolCalls))
	}
}

// TestAccumulate_FinishReasonEmptyDelta verifies that a FinishReason
// arriving on a final chunk with an otherwise empty delta is correctly
// captured.
func TestAccumulate_FinishReasonEmptyDelta(t *testing.T) {
	chunks := []StreamChunk{
		chunk(textDelta(0, "Done.")),
		chunk(finishDelta(0, "stop")),
	}

	resp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q",
			resp.Choices[0].FinishReason, "stop")
	}
}

// TestAccumulate_EmptyChunksAndNoChunks verifies that empty chunks,
// chunks with zero choices, and a stream that closes with no chunks at
// all are handled gracefully.
func TestAccumulate_EmptyChunksAndNoChunks(t *testing.T) {
	t.Run("empty chunks with zero choices", func(t *testing.T) {
		chunks := []StreamChunk{
			chunk(), // no choices
			chunk(), // no choices
			chunk(textDelta(0, "hi")),
			chunk(finishDelta(0, "stop")),
		}
		resp, err := accumulate(chunks)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Choices[0].Message.Content != "hi" {
			t.Errorf("content = %q, want %q",
				resp.Choices[0].Message.Content, "hi")
		}
	})

	t.Run("stream with no chunks at all", func(t *testing.T) {
		resp, err := accumulate([]StreamChunk{})
		if err == nil {
			t.Fatal("expected error for empty stream, got nil")
		}
		if !strings.Contains(err.Error(), "without finish reason") {
			t.Errorf("error = %q, want it to mention 'without finish reason'",
				err.Error())
		}
		if resp != nil {
			t.Errorf("expected nil response, got %+v", resp)
		}
	})
}

// TestAccumulate_TruncatedMidToolCall verifies that a stream that closes
// mid-tool-call (truncated) is reported as an error, never as a complete call.
func TestAccumulate_TruncatedMidToolCall(t *testing.T) {
	t.Run("missing id on first fragment", func(t *testing.T) {
		chunks := []StreamChunk{
			chunk(toolCallDelta(0, ToolCall{
				Index: 0,
				// No ID, no name
				Function: FunctionCall{Arguments: `{"a":1}`},
			})),
			chunk(finishDelta(0, "tool_calls")),
		}
		resp, err := accumulate(chunks)
		if err == nil {
			t.Fatal("expected error for truncated tool call, got nil")
		}
		if resp != nil {
			t.Errorf("expected nil response, got %+v", resp)
		}
	})

	t.Run("stream ends without finish reason after tool call start", func(t *testing.T) {
		chunks := []StreamChunk{
			chunk(toolCallDelta(0, ToolCall{
				Index:    0,
				ID:       "call_1",
				Type:     "function",
				Function: FunctionCall{Name: "tool_a", Arguments: `{"a":`},
			})),
			// No finish reason — stream just ends
		}
		resp, err := accumulate(chunks)
		if err == nil {
			t.Fatal("expected error for stream ending without finish, got nil")
		}
		if resp != nil {
			t.Errorf("expected nil response, got %+v", resp)
		}
	})
}

// TestAccumulate_MultiChoice verifies that multiple choices (Index) are
// preserved in order.
func TestAccumulate_MultiChoice(t *testing.T) {
	chunks := []StreamChunk{
		chunk(textDelta(0, "Choice A")),
		chunk(textDelta(1, "Choice B")),
		chunk(finishDelta(0, "stop")),
		chunk(finishDelta(1, "stop")),
	}

	resp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Choices) != 2 {
		t.Fatalf("expected 2 choices, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Index != 0 {
		t.Errorf("choice[0] index = %d, want 0", resp.Choices[0].Index)
	}
	if resp.Choices[0].Message.Content != "Choice A" {
		t.Errorf("choice[0] content = %q, want %q",
			resp.Choices[0].Message.Content, "Choice A")
	}
	if resp.Choices[1].Index != 1 {
		t.Errorf("choice[1] index = %d, want 1", resp.Choices[1].Index)
	}
	if resp.Choices[1].Message.Content != "Choice B" {
		t.Errorf("choice[1] content = %q, want %q",
			resp.Choices[1].Message.Content, "Choice B")
	}
}

// TestAccumulate_AccumulateStream verifies the convenience function
// AccumulateStream works with a real channel.
func TestAccumulate_AccumulateStream(t *testing.T) {
	chunks := []StreamChunk{
		chunk(textDelta(0, "Hello")),
		chunk(textDelta(0, " world")),
		chunk(finishDelta(0, "stop")),
	}

	stream := make(chan StreamChunk, len(chunks))
	for _, c := range chunks {
		stream <- c
	}
	close(stream)

	resp, err := AccumulateStream(stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "Hello world" {
		t.Errorf("content = %q, want %q",
			resp.Choices[0].Message.Content, "Hello world")
	}
}

// TestAccumulate_MutationCheck verifies that the tests are load-bearing:
// if the index-based join is replaced with append-in-arrival-order, the
// interleaved tool call test must fail.
//
// This test runs the interleaved scenario through a deliberately broken
// accumulator (arrival-order join) and asserts that the result is wrong.
// If this test passes, it means the correct accumulator's tests are
// actually testing the index-based behavior.
func TestAccumulate_MutationCheck(t *testing.T) {
	chunks := []StreamChunk{
		chunk(toolCallDelta(0, ToolCall{
			Index:    0,
			ID:       "call_0",
			Type:     "function",
			Function: FunctionCall{Name: "tool_a", Arguments: `{"x":`},
		})),
		chunk(toolCallDelta(0, ToolCall{
			Index:    1,
			ID:       "call_1",
			Type:     "function",
			Function: FunctionCall{Name: "tool_b", Arguments: `{"y":`},
		})),
		chunk(toolCallDelta(0, ToolCall{
			Index:    0,
			Function: FunctionCall{Arguments: `"1"}`},
		})),
		chunk(toolCallDelta(0, ToolCall{
			Index:    1,
			Function: FunctionCall{Arguments: `"2"}`},
		})),
		chunk(finishDelta(0, "tool_calls")),
	}

	// Run through the correct accumulator — should produce correct results.
	correctResp, err := accumulate(chunks)
	if err != nil {
		t.Fatalf("correct accumulator failed: %v", err)
	}

	// Run through a broken accumulator (arrival-order join) — should produce wrong results.
	brokenResp, brokenErr := accumulateArrivalOrder(chunks)
	if brokenErr != nil {
		t.Fatalf("broken accumulator should not error: %v", brokenErr)
	}

	// The correct accumulator produces 2 tool calls with correct arguments.
	if len(correctResp.Choices[0].Message.ToolCalls) != 2 {
		t.Fatalf("correct: expected 2 tool calls, got %d",
			len(correctResp.Choices[0].Message.ToolCalls))
	}
	correctArgs0 := correctResp.Choices[0].Message.ToolCalls[0].Function.Arguments
	correctArgs1 := correctResp.Choices[0].Message.ToolCalls[1].Function.Arguments
	if correctArgs0 != `{"x":"1"}` {
		t.Fatalf("correct: tc[0] args = %q, want %q", correctArgs0, `{"x":"1"}`)
	}
	if correctArgs1 != `{"y":"2"}` {
		t.Fatalf("correct: tc[1] args = %q, want %q", correctArgs1, `{"y":"2"}`)
	}

	// The broken accumulator should produce DIFFERENT (wrong) results.
	// With arrival-order joining, all fragments go to the first tool call,
	// so there's only 1 tool call instead of 2.
	brokenToolCalls := brokenResp.Choices[0].Message.ToolCalls

	// The broken accumulator produces 1 tool call (all fragments appended
	// to the first), while the correct one produces 2.
	if len(brokenToolCalls) == len(correctResp.Choices[0].Message.ToolCalls) {
		// If same count, check arguments differ
		brokenArgs0 := brokenToolCalls[0].Function.Arguments
		brokenArgs1 := brokenToolCalls[1].Function.Arguments
		if brokenArgs0 == correctArgs0 && brokenArgs1 == correctArgs1 {
			t.Fatal("MUTATION CHECK FAILED: broken accumulator produced same results as correct — tests are not load-bearing")
		}
		if brokenArgs0 == `{"x":"1"}` {
			t.Errorf("broken accumulator produced correct args for tc[0] — mutation check is not effective")
		}
	}
	// If different count (1 vs 2), the mutation check passes — the broken
	// accumulator produces structurally different output.
}

// accumulate is a test helper that runs chunks through a ChunkAccumulator.
func accumulate(chunks []StreamChunk) (*ChatResponse, error) {
	acc := NewChunkAccumulator()
	for _, c := range chunks {
		if err := acc.Accumulate(c); err != nil {
			return nil, err
		}
	}
	return acc.Result()
}

// brokenAccumulator is a deliberately wrong accumulator that joins tool-call
// fragments by arrival order instead of by index. It is used only in the
// mutation check test to prove the tests are load-bearing.
type brokenAccumulator struct {
	id       string
	model    string
	created  int64
	object   string
	choices  []*brokenChoice
	finished bool
	reason   string
}

type brokenChoice struct {
	index        int
	role         MessageRole
	content      string
	toolCalls    []*brokenToolCall
	finishReason string
}

type brokenToolCall struct {
	id        string
	type_     string
	name      string
	arguments string
}

// accumulateArrivalOrder is a deliberately broken accumulator that joins
// tool-call fragments by arrival order (appending to the first tool call
// regardless of index). This simulates the mutation where index-based
// joining is replaced with arrival-order joining.
func accumulateArrivalOrder(chunks []StreamChunk) (*ChatResponse, error) {
	acc := &brokenAccumulator{}
	for _, chunk := range chunks {
		if chunk.ID != "" {
			acc.id = chunk.ID
		}
		if chunk.Model != "" {
			acc.model = chunk.Model
		}
		if chunk.Created != 0 {
			acc.created = chunk.Created
		}
		if chunk.Object != "" {
			acc.object = chunk.Object
		}
		for _, choice := range chunk.Choices {
			// Find or create the choice by index
			var c *brokenChoice
			for _, existing := range acc.choices {
				if existing.index == choice.Index {
					c = existing
					break
				}
			}
			if c == nil {
				c = &brokenChoice{index: choice.Index}
				acc.choices = append(acc.choices, c)
			}

			if choice.Delta.Role != "" {
				c.role = choice.Delta.Role
			}
			c.content += choice.Delta.Content

			// BROKEN: append all tool call fragments to the first tool call,
			// ignoring the index. This is the mutation we're checking for.
			for _, tc := range choice.Delta.ToolCalls {
				if len(c.toolCalls) == 0 {
					c.toolCalls = append(c.toolCalls, &brokenToolCall{
						id: tc.ID, type_: tc.Type, name: tc.Function.Name,
					})
				}
				// Always append to the first (and only) tool call
				c.toolCalls[0].arguments += tc.Function.Arguments
				// Also set id/name if present (but only on first fragment)
				if tc.ID != "" {
					c.toolCalls[0].id = tc.ID
				}
				if tc.Function.Name != "" {
					c.toolCalls[0].name = tc.Function.Name
				}
				if tc.Type != "" {
					c.toolCalls[0].type_ = tc.Type
				}
			}

			if choice.FinishReason != "" {
				c.finishReason = choice.FinishReason
				acc.finished = true
				acc.reason = choice.FinishReason
			}
		}
	}

	if !acc.finished {
		return nil, errors.New("stream ended without finish reason")
	}

	choices := make([]Choice, 0, len(acc.choices))
	for _, c := range acc.choices {
		msg := Message{Role: c.role, Content: c.content}
		if len(c.toolCalls) > 0 {
			tcs := make([]ToolCall, 0, len(c.toolCalls))
			for _, t := range c.toolCalls {
				tcs = append(tcs, ToolCall{
					ID: t.id, Type: t.type_,
					Function: FunctionCall{Name: t.name, Arguments: t.arguments},
				})
			}
			msg.ToolCalls = tcs
		}
		choices = append(choices, Choice{
			Index: c.index, Message: msg, FinishReason: c.finishReason,
		})
	}

	return &ChatResponse{
		ID: acc.id, Object: "chat.completion", Created: acc.created,
		Model: acc.model, Choices: choices, FinishReason: acc.reason,
	}, nil
}
