package providers

import (
	"errors"
	"fmt"
)

// ChunkAccumulator accumulates a sequence of StreamChunk values into a
// ChatResponse that matches the shape the non-streaming Chat path returns.
//
// Tool-call fragments are joined by index (the Index field on each
// ToolCall), not by arrival order. This is critical: a turn can have
// several tool calls whose argument fragments interleave across chunks,
// and joining by arrival order produces corrupted JSON that then gets
// executed as a tool call.
//
// Usage:
//
//	acc := NewChunkAccumulator()
//	for chunk := range stream {
//	    if err := acc.Accumulate(chunk); err != nil {
//	        return err
//	    }
//	}
//	resp, err := acc.Result()
//
// The accumulator is not safe for concurrent use — a single goroutine
// should own the stream and call Accumulate sequentially.
type ChunkAccumulator struct {
	id           string
	object       string
	created      int64
	model        string
	choices      map[int]*accumulatedChoice
	order        []int // preserves first-seen order of choice indices
	finished     bool
	finishReason string
	err          error
}

// accumulatedChoice holds the accumulated state for one choice index.
type accumulatedChoice struct {
	index        int
	role         MessageRole
	content      string
	toolCalls    map[int]*accumulatedToolCall
	toolOrder    []int // preserves first-seen order of tool-call indices
	finishReason string
}

// accumulatedToolCall holds the accumulated state for one tool-call index.
type accumulatedToolCall struct {
	index     int
	id        string
	type_     string
	name      string
	arguments string
}

// NewChunkAccumulator creates a fresh accumulator.
func NewChunkAccumulator() *ChunkAccumulator {
	return &ChunkAccumulator{
		choices: make(map[int]*accumulatedChoice),
	}
}

// Accumulate processes one streaming chunk. It is safe to call with chunks
// that have zero choices, empty deltas, or no finish reason — these are
// no-ops. Once the stream has finished (a chunk with a FinishReason was
// seen), further calls are no-ops.
func (a *ChunkAccumulator) Accumulate(chunk StreamChunk) error {
	if a.err != nil {
		return a.err
	}
	if a.finished {
		return nil
	}

	// Top-level fields: only update when non-zero (first chunk wins).
	if chunk.ID != "" {
		a.id = chunk.ID
	}
	if chunk.Object != "" {
		a.object = chunk.Object
	}
	if chunk.Created != 0 {
		a.created = chunk.Created
	}
	if chunk.Model != "" {
		a.model = chunk.Model
	}

	for _, choice := range chunk.Choices {
		if err := a.accumulateChoice(choice); err != nil {
			a.err = err
			return err
		}
	}

	return nil
}

// accumulateChoice merges one StreamChoice delta into the accumulator.
func (a *ChunkAccumulator) accumulateChoice(choice StreamChoice) error {
	idx := choice.Index
	c, ok := a.choices[idx]
	if !ok {
		c = &accumulatedChoice{
			index:     idx,
			toolCalls: make(map[int]*accumulatedToolCall),
		}
		a.choices[idx] = c
		a.order = append(a.order, idx)
	}

	delta := choice.Delta

	// Role is typically only on the first chunk.
	if delta.Role != "" {
		c.role = delta.Role
	}

	// Content deltas are always appended.
	if delta.Content != "" {
		c.content += delta.Content
	}

	// Tool-call fragments: join by index.
	for _, tc := range delta.ToolCalls {
		if err := c.accumulateToolCall(tc); err != nil {
			return err
		}
	}

	// FinishReason on a choice marks that choice as done.
	if choice.FinishReason != "" {
		c.finishReason = choice.FinishReason
		a.finished = true
		a.finishReason = choice.FinishReason
	}

	return nil
}

// accumulateToolCall merges one tool-call fragment into the choice.
// Only non-empty fields are written, so subsequent fragments (which omit
// id/name/type) do not clobber values from the first fragment.
func (c *accumulatedChoice) accumulateToolCall(tc ToolCall) error {
	tcIdx := tc.Index
	t, ok := c.toolCalls[tcIdx]
	if !ok {
		t = &accumulatedToolCall{index: tcIdx}
		c.toolCalls[tcIdx] = t
		c.toolOrder = append(c.toolOrder, tcIdx)
	}

	// id, type, and name typically appear only on the first fragment.
	if tc.ID != "" {
		t.id = tc.ID
	}
	if tc.Type != "" {
		t.type_ = tc.Type
	}
	if tc.Function.Name != "" {
		t.name = tc.Function.Name
	}

	// Arguments are fragmented — always append, even if empty.
	t.arguments += tc.Function.Arguments

	return nil
}

// Result produces the final ChatResponse from the accumulated chunks.
// Returns an error if:
//   - The stream ended without a finish reason (truncated).
//   - A tool call is missing its id or name (truncated mid-tool-call).
func (a *ChunkAccumulator) Result() (*ChatResponse, error) {
	if a.err != nil {
		return nil, a.err
	}

	if !a.finished {
		return nil, errors.New("stream ended without finish reason")
	}

	// Validate that all tool calls are complete.
	for _, c := range a.choices {
		for _, tIdx := range c.toolOrder {
			t := c.toolCalls[tIdx]
			if t.id == "" || t.name == "" {
				return nil, fmt.Errorf(
					"stream truncated: tool call at index %d missing id or name",
					tIdx,
				)
			}
		}
	}

	choices := make([]Choice, 0, len(a.order))
	for _, idx := range a.order {
		c := a.choices[idx]
		msg := Message{
			Role:    c.role,
			Content: c.content,
		}
		if len(c.toolOrder) > 0 {
			toolCalls := make([]ToolCall, 0, len(c.toolOrder))
			for _, tIdx := range c.toolOrder {
				t := c.toolCalls[tIdx]
				toolCalls = append(toolCalls, ToolCall{
					Index: t.index,
					ID:    t.id,
					Type:  t.type_,
					Function: FunctionCall{
						Name:      t.name,
						Arguments: t.arguments,
					},
				})
			}
			msg.ToolCalls = toolCalls
		}
		choices = append(choices, Choice{
			Index:        idx,
			Message:      msg,
			FinishReason: c.finishReason,
		})
	}

	return &ChatResponse{
		ID:           a.id,
		Object:       "chat.completion",
		Created:      a.created,
		Model:        a.model,
		Choices:      choices,
		FinishReason: a.finishReason,
	}, nil
}

// AccumulateStream is a convenience function that drains a stream channel
// into a ChunkAccumulator and returns the result.
func AccumulateStream(stream <-chan StreamChunk) (*ChatResponse, error) {
	acc := NewChunkAccumulator()
	for chunk := range stream {
		if err := acc.Accumulate(chunk); err != nil {
			return nil, err
		}
	}
	return acc.Result()
}
