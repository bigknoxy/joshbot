package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// --- Streaming test helpers ---

// streamChunk builds a StreamChunk with the given choice deltas.
func streamChunk(choices ...providers.StreamChoice) providers.StreamChunk {
	return providers.StreamChunk{
		ID:      "chatcmpl-test",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "test-model",
		Choices: choices,
	}
}

func streamTextDelta(index int, content string) providers.StreamChoice {
	return providers.StreamChoice{
		Index: index,
		Delta: providers.Message{Role: "assistant", Content: content},
	}
}

func streamFinishDelta(index int, reason string) providers.StreamChoice {
	return providers.StreamChoice{
		Index:        index,
		Delta:        providers.Message{},
		FinishReason: reason,
	}
}

func streamToolCallDelta(index int, tc providers.ToolCall) providers.StreamChoice {
	return providers.StreamChoice{
		Index: index,
		Delta: providers.Message{ToolCalls: []providers.ToolCall{tc}},
	}
}

// newStreamingConfig returns a config with streaming enabled.
func newStreamingConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Agents.Defaults.Streaming = true
	return cfg
}

// newNonStreamingConfig returns a config with streaming explicitly off. It must
// set the field rather than lean on the default: streaming defaults to on since
// v1.48.0, and a helper that assumed otherwise turned this test into a copy of
// the flag-on one.
func newNonStreamingConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Agents.Defaults.Streaming = false
	return cfg
}

// --- Tests ---

// TestStreaming_FlagOffIdenticalToNonStreaming verifies that when streaming
// is disabled, behavior is byte-identical to the non-streaming
// path. The mock provider's ChatStream should never be called.
func TestStreaming_FlagOffIdenticalToNonStreaming(t *testing.T) {
	cfg := newNonStreamingConfig()

	chatCalled := false
	streamCalled := false

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			chatCalled = true
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Hello from non-streaming",
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			streamCalled = true
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	}

	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	// A sink IS attached. The config flag is what must keep streaming off:
	// without the sink this test is identical in setup to the flag-on test and
	// proves nothing about the flag.
	ctx := WithStreamSink(context.Background(), func(StreamEvent) {})
	response, err := agent.Process(ctx, msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	if !chatCalled {
		t.Error("expected Chat to be called")
	}
	if streamCalled {
		t.Error("expected ChatStream to NOT be called when the streaming config flag is off")
	}
	if response != "Hello from non-streaming" {
		t.Errorf("response = %q, want %q", response, "Hello from non-streaming")
	}
}

// TestStreaming_FlagOnTextAppearsIncrementally verifies that when streaming
// is enabled and a sink is attached, text deltas are forwarded to the sink
// as they arrive.
func TestStreaming_FlagOnTextAppearsIncrementally(t *testing.T) {
	cfg := newStreamingConfig()

	provider := &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 5)
			ch <- streamChunk(streamTextDelta(0, "Hello"))
			ch <- streamChunk(streamTextDelta(0, " world"))
			ch <- streamChunk(streamTextDelta(0, "!"))
			ch <- streamChunk(streamFinishDelta(0, "stop"))
			close(ch)
			return ch, nil
		},
	}

	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	var mu sync.Mutex
	var deltas []string
	var doneCount int
	sink := func(e StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		if e.Delta != "" {
			deltas = append(deltas, e.Delta)
		}
		if e.Done {
			doneCount++
		}
	}

	ctx := WithStreamSink(context.Background(), sink)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(ctx, msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Verify deltas were received incrementally
	if len(deltas) != 3 {
		t.Fatalf("expected 3 deltas, got %d: %v", len(deltas), deltas)
	}
	if deltas[0] != "Hello" {
		t.Errorf("delta[0] = %q, want %q", deltas[0], "Hello")
	}
	if deltas[1] != " world" {
		t.Errorf("delta[1] = %q, want %q", deltas[1], " world")
	}
	if deltas[2] != "!" {
		t.Errorf("delta[2] = %q, want %q", deltas[2], "!")
	}

	// Verify Done event was sent
	if doneCount != 1 {
		t.Errorf("expected 1 Done event, got %d", doneCount)
	}

	// Verify the accumulated response is correct
	if response != "Hello world!" {
		t.Errorf("response = %q, want %q", response, "Hello world!")
	}
}

// TestStreaming_FlagOnPipedOutputIdentical verifies that when streaming is
// enabled but no sink is attached (piped/non-TTY output), the behavior is
// identical to flag-off. The non-streaming Chat path is used.
func TestStreaming_FlagOnPipedOutputIdentical(t *testing.T) {
	cfg := newStreamingConfig()

	chatCalled := false
	streamCalled := false

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			chatCalled = true
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Hello from non-streaming",
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			streamCalled = true
			ch := make(chan providers.StreamChunk)
			close(ch)
			return ch, nil
		},
	}

	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	// No sink attached — even though streaming is enabled in config,
	// the non-streaming path should be used.
	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	if !chatCalled {
		t.Error("expected Chat to be called (no sink = non-streaming)")
	}
	if streamCalled {
		t.Error("expected ChatStream to NOT be called when the streaming config flag is off")
	}
	if response != "Hello from non-streaming" {
		t.Errorf("response = %q, want %q", response, "Hello from non-streaming")
	}
}

// TestStreaming_ToolCallsExecuteCorrectly verifies that a turn with tool
// calls still executes them correctly when streaming is enabled.
func TestStreaming_ToolCallsExecuteCorrectly(t *testing.T) {
	cfg := newStreamingConfig()

	iteration := 0
	provider := &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			iteration++
			ch := make(chan providers.StreamChunk, 10)

			if iteration == 1 {
				// First call: stream a tool call
				ch <- streamChunk(streamTextDelta(0, "Let me check that."))
				ch <- streamChunk(streamToolCallDelta(0, providers.ToolCall{
					Index: 0,
					ID:    "call_1",
					Type:  "function",
					Function: providers.FunctionCall{
						Name:      "filesystem",
						Arguments: `{"operation":"read_file","path":"test.txt"}`,
					},
				}))
				ch <- streamChunk(streamFinishDelta(0, "tool_calls"))
			} else {
				// Second call: stream final response
				ch <- streamChunk(streamTextDelta(0, "The file contains: hello world"))
				ch <- streamChunk(streamFinishDelta(0, "stop"))
			}
			close(ch)
			return ch, nil
		},
	}

	tools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, args map[string]any) (string, error) {
			if name == "filesystem" {
				return "File content: hello world", nil
			}
			return "", nil
		},
		schemas: []providers.Tool{
			{
				Type: "function",
				Function: providers.FunctionDefinition{
					Name:        "filesystem",
					Description: "Read files",
				},
			},
		},
	}

	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	var mu sync.Mutex
	var deltas []string
	sink := func(e StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		if e.Delta != "" {
			deltas = append(deltas, e.Delta)
		}
	}

	ctx := WithStreamSink(context.Background(), sink)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Read test.txt",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(ctx, msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Verify deltas from both iterations were received
	if len(deltas) == 0 {
		t.Fatal("expected at least 1 delta, got 0")
	}

	// Verify the final response is correct (tool was executed)
	if response != "The file contains: hello world" {
		t.Errorf("response = %q, want %q", response, "The file contains: hello world")
	}
}

// TestStreaming_MidStreamFailure verifies that when the stream fails
// mid-way, partial text is retained and a visible error marker is appended.
func TestStreaming_MidStreamFailure(t *testing.T) {
	cfg := newStreamingConfig()

	provider := &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 5)
			ch <- streamChunk(streamTextDelta(0, "Partial response"))
			ch <- streamChunk(streamTextDelta(0, " still going"))
			// Stream closes without finish reason — truncated
			close(ch)
			return ch, nil
		},
	}

	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	var mu sync.Mutex
	var allDeltas string
	sink := func(e StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		allDeltas += e.Delta
	}

	ctx := WithStreamSink(context.Background(), sink)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(ctx, msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Verify partial text was retained
	if !strings.Contains(allDeltas, "Partial response") {
		t.Errorf("expected partial text in sink output, got %q", allDeltas)
	}
	if !strings.Contains(allDeltas, " still going") {
		t.Errorf("expected continued partial text in sink output, got %q", allDeltas)
	}

	// Verify a visible error marker was appended
	if !strings.Contains(allDeltas, "[stream error:") {
		t.Errorf("expected [stream error:] marker in sink output, got %q", allDeltas)
	}

	// Verify the response includes the error marker
	if !strings.Contains(response, "[stream error:") {
		t.Errorf("expected [stream error:] marker in response, got %q", response)
	}
	if !strings.Contains(response, "Partial response") {
		t.Errorf("expected partial text in response, got %q", response)
	}
}

// TestStreaming_MidStreamFailureWithToolCalls verifies that when the stream
// fails mid-tool-call, the partial text is retained with an error marker,
// and no partial tool calls are executed.
func TestStreaming_MidStreamFailureWithToolCalls(t *testing.T) {
	cfg := newStreamingConfig()

	provider := &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 5)
			ch <- streamChunk(streamTextDelta(0, "Let me check"))
			ch <- streamChunk(streamToolCallDelta(0, providers.ToolCall{
				Index: 0,
				ID:    "call_1",
				Type:  "function",
				Function: providers.FunctionCall{
					Name:      "filesystem",
					Arguments: `{"operation":"read`, // truncated arguments
				},
			}))
			// Stream closes without finish reason — truncated mid-tool-call
			close(ch)
			return ch, nil
		},
	}

	toolCalled := false
	tools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, args map[string]any) (string, error) {
			toolCalled = true
			return "result", nil
		},
		schemas: []providers.Tool{
			{
				Type: "function",
				Function: providers.FunctionDefinition{
					Name:        "filesystem",
					Description: "Read files",
				},
			},
		},
	}

	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	var mu sync.Mutex
	var allDeltas string
	sink := func(e StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		allDeltas += e.Delta
	}

	ctx := WithStreamSink(context.Background(), sink)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Read test.txt",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(ctx, msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Verify partial text was retained
	if !strings.Contains(allDeltas, "Let me check") {
		t.Errorf("expected partial text in sink output, got %q", allDeltas)
	}

	// Verify error marker was appended
	if !strings.Contains(allDeltas, "[stream error:") {
		t.Errorf("expected [stream error:] marker in sink output, got %q", allDeltas)
	}

	// Verify no tool was executed (truncated tool calls should not run)
	if toolCalled {
		t.Error("expected no tool execution on truncated stream")
	}

	// Verify the response includes the error marker but not tool call results
	if !strings.Contains(response, "[stream error:") {
		t.Errorf("expected [stream error:] marker in response, got %q", response)
	}
}

// TestStreaming_StreamFailsToOpen verifies that when ChatStream returns an
// error (stream fails to open), the error is handled gracefully.
func TestStreaming_StreamFailsToOpen(t *testing.T) {
	cfg := newStreamingConfig()

	provider := &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			return nil, context.DeadlineExceeded
		},
	}

	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	sink := func(e StreamEvent) {}
	ctx := WithStreamSink(context.Background(), sink)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	// Process catches errors from reactLoop and returns them as strings
	// (with nil error), so we check the response content.
	response, err := agent.Process(ctx, msg)
	if err != nil {
		t.Fatalf("process should not return error, got: %v", err)
	}
	if response == "" {
		t.Fatal("expected non-empty response")
	}
	if !strings.Contains(response, "Error") {
		t.Errorf("expected error message in response, got %q", response)
	}
}

// A stream that dies before producing any text must still say so.
//
// Suppressing the marker when nothing had been streamed yet left an empty
// response, which reactLoop replaced with "I've processed your request." — a
// confident non-answer standing in for a failure, and the failure mode is
// most likely precisely when no text has arrived.
func TestStreaming_ErrorBeforeAnyTextIsStillReported(t *testing.T) {
	cfg := newStreamingConfig()

	provider := &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			// No content and no finish reason: the accumulator rejects this
			// as a truncated stream.
			ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{}}}
			close(ch)
			return ch, nil
		},
	}

	var got strings.Builder
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	ctx := WithStreamSink(context.Background(), func(e StreamEvent) { got.WriteString(e.Delta) })

	response, err := agent.Process(ctx, bus.InboundMessage{
		SenderID: "user123", Content: "Hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if !strings.Contains(got.String(), "stream error") {
		t.Errorf("nothing was sent to the sink about the failure, got %q", got.String())
	}
	if strings.Contains(response, "I've processed your request") {
		t.Errorf("a failed stream was reported as a completed answer: %q", response)
	}
}
