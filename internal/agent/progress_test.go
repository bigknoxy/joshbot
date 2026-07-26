package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// TestSummarizeToolArgs verifies the brief, truncated key-argument summary
// used in tool-call progress lines picks a sensible field per tool and
// truncates long values.
func TestSummarizeToolArgs(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{
			name: "shell command",
			tool: "shell",
			args: map[string]any{"command": "go test ./..."},
			want: "go test ./...",
		},
		{
			name: "filesystem path",
			tool: "filesystem",
			args: map[string]any{"operation": "read_file", "path": "/tmp/foo.txt"},
			want: "/tmp/foo.txt",
		},
		{
			name: "web query",
			tool: "web",
			args: map[string]any{"query": "golang isatty"},
			want: "golang isatty",
		},
		{
			name: "no recognized key falls back to first value",
			tool: "mystery_tool",
			args: map[string]any{"foo": "bar"},
			want: "bar",
		},
		{
			name: "no args",
			tool: "noop",
			args: map[string]any{},
			want: "",
		},
		{
			name: "long value is truncated",
			tool: "shell",
			args: map[string]any{"command": "this is a very very very very very very very very long shell command that exceeds the limit"},
			want: "this is a very very very very very very very very long shell com...",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeToolArgs(tc.tool, tc.args)
			if got != tc.want {
				t.Errorf("summarizeToolArgs(%q, %v) = %q, want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}

// TestAgentProgressCallback verifies that when a progress callback is
// supplied via WithSink (context-carried), the ReAct loop emits a start
// event before executing a tool and a done event after, with elapsed time
// and success recorded, and that the callback is never invoked when nil.
func TestAgentProgressCallback(t *testing.T) {
	cfg := config.Defaults()
	iteration := 0

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			iteration++
			if iteration == 1 {
				return &providers.ChatResponse{
					ID:    "test-id",
					Model: req.Model,
					Choices: []providers.Choice{
						{
							Message: providers.Message{
								Role: providers.RoleAssistant,
								ToolCalls: []providers.ToolCall{
									{
										ID:   "call_1",
										Type: "function",
										Function: providers.FunctionCall{
											Name:      "shell",
											Arguments: `{"command":"echo hi"}`,
										},
									},
								},
							},
							FinishReason: "tool_calls",
						},
					},
				}, nil
			}
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message:      providers.Message{Role: providers.RoleAssistant, Content: "done"},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, args map[string]any) (string, error) {
			return "ok", nil
		},
		schemas: []providers.Tool{{Type: "function", Function: providers.FunctionDefinition{Name: "shell"}}},
	}

	sessions := newMockSessionManager()
	logger := newMockLogger()

	var events []ToolProgressEvent
	agent := NewAgent(cfg, provider, tools, sessions, logger)

	ctx := WithSink(context.Background(), func(e ToolProgressEvent) {
		events = append(events, e)
	})

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "run echo hi",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	if _, err := agent.Process(ctx, msg); err != nil {
		t.Fatalf("process failed: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 progress events (start, done), got %d: %+v", len(events), events)
	}
	if events[0].Phase != ToolProgressStart {
		t.Errorf("first event phase = %v, want ToolProgressStart", events[0].Phase)
	}
	if events[0].Tool != "shell" {
		t.Errorf("first event tool = %q, want shell", events[0].Tool)
	}
	if events[0].Summary != "echo hi" {
		t.Errorf("first event summary = %q, want %q", events[0].Summary, "echo hi")
	}
	if events[1].Phase != ToolProgressDone {
		t.Errorf("second event phase = %v, want ToolProgressDone", events[1].Phase)
	}
	if events[1].Err != nil {
		t.Errorf("second event err = %v, want nil", events[1].Err)
	}
	if events[1].Elapsed < 0 {
		t.Errorf("second event elapsed = %v, want >= 0", events[1].Elapsed)
	}
}

// TestAgentProgressCallbackNilIsNoOp verifies that not supplying a progress
// callback (the default, nil) causes no behavior change — no panic, no
// output differences — matching "zero behavior change for other callers".
func TestAgentProgressCallbackNilIsNoOp(t *testing.T) {
	cfg := config.Defaults()
	iteration := 0

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			iteration++
			if iteration == 1 {
				return &providers.ChatResponse{
					ID:    "test-id",
					Model: req.Model,
					Choices: []providers.Choice{
						{
							Message: providers.Message{
								Role: providers.RoleAssistant,
								ToolCalls: []providers.ToolCall{
									{
										ID:       "call_1",
										Type:     "function",
										Function: providers.FunctionCall{Name: "shell", Arguments: `{"command":"echo hi"}`},
									},
								},
							},
							FinishReason: "tool_calls",
						},
					},
				}, nil
			}
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message:      providers.Message{Role: providers.RoleAssistant, Content: "done"},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, args map[string]any) (string, error) { return "ok", nil },
		schemas:   []providers.Tool{{Type: "function", Function: providers.FunctionDefinition{Name: "shell"}}},
	}

	agent := NewAgent(cfg, provider, tools, newMockSessionManager(), newMockLogger())

	msg := bus.InboundMessage{SenderID: "user123", Content: "run echo hi", Channel: "cli", Timestamp: time.Now()}
	if _, err := agent.Process(context.Background(), msg); err != nil {
		t.Fatalf("process failed with nil progress callback: %v", err)
	}
}

// callStateKey is a context key for per-call iteration tracking in the
// concurrent test, so each Process call gets its own tool-call-then-final
// sequence even when sharing one mockProvider.
type callStateKey struct{}

type callState struct {
	mu        sync.Mutex
	iteration int
}

// TestConcurrentProcessNoCrossDelivery is the central test for issue #115.
// It spawns two concurrent Process calls on the same Agent, each with a
// distinct sink attached via the context. It asserts that each sink receives
// exactly the events from its own call — no cross-delivery between chats.
//
// This test would fail against the old design (Agent.progress as a shared
// struct field): with two concurrent Process calls, the second
// SetProgressCallback would overwrite the first, and one sink would get all
// events while the other got none.
func TestConcurrentProcessNoCrossDelivery(t *testing.T) {
	cfg := config.Defaults()

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			state, _ := ctx.Value(callStateKey{}).(*callState)
			if state == nil {
				// No per-call state — shouldn't happen in this test.
				return &providers.ChatResponse{
					Choices: []providers.Choice{{
						Message:      providers.Message{Role: providers.RoleAssistant, Content: "done"},
						FinishReason: "stop",
					}},
				}, nil
			}
			state.mu.Lock()
			state.iteration++
			iter := state.iteration
			state.mu.Unlock()

			if iter == 1 {
				return &providers.ChatResponse{
					ID:    "test-id",
					Model: req.Model,
					Choices: []providers.Choice{
						{
							Message: providers.Message{
								Role: providers.RoleAssistant,
								ToolCalls: []providers.ToolCall{
									{
										ID:   "call_1",
										Type: "function",
										Function: providers.FunctionCall{
											Name:      "shell",
											Arguments: `{"command":"echo hi"}`,
										},
									},
								},
							},
							FinishReason: "tool_calls",
						},
					},
				}, nil
			}
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message:      providers.Message{Role: providers.RoleAssistant, Content: "done"},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, args map[string]any) (string, error) {
			return "ok", nil
		},
		schemas: []providers.Tool{{Type: "function", Function: providers.FunctionDefinition{Name: "shell"}}},
	}

	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	// Two distinct sinks, each collecting into its own buffered channel.
	var mu sync.Mutex
	eventsA := make(chan ToolProgressEvent, 10)
	eventsB := make(chan ToolProgressEvent, 10)

	stateA := &callState{}
	stateB := &callState{}

	ctxA := WithSink(context.WithValue(context.Background(), callStateKey{}, stateA), func(e ToolProgressEvent) {
		mu.Lock()
		eventsA <- e
		mu.Unlock()
	})
	ctxB := WithSink(context.WithValue(context.Background(), callStateKey{}, stateB), func(e ToolProgressEvent) {
		mu.Lock()
		eventsB <- e
		mu.Unlock()
	})

	// Different SenderID so each call gets its own session.
	msgA := bus.InboundMessage{
		SenderID:  "userA",
		Content:   "run echo hi",
		Channel:   "cli",
		Timestamp: time.Now(),
	}
	msgB := bus.InboundMessage{
		SenderID:  "userB",
		Content:   "run echo hi",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	// Run both Process calls concurrently.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		agent.Process(ctxA, msgA)
	}()
	go func() {
		defer wg.Done()
		agent.Process(ctxB, msgB)
	}()
	wg.Wait()

	close(eventsA)
	close(eventsB)

	var gotA, gotB []ToolProgressEvent
	for e := range eventsA {
		gotA = append(gotA, e)
	}
	for e := range eventsB {
		gotB = append(gotB, e)
	}

	// Each sink must have received exactly 2 events (start + done) from its
	// own call. If the sink were shared mutable state on Agent, one sink
	// would get 0 and the other 4 (or some other split), proving cross-talk.
	if len(gotA) != 2 {
		t.Errorf("sink A: expected 2 events, got %d: %+v", len(gotA), gotA)
	}
	if len(gotB) != 2 {
		t.Errorf("sink B: expected 2 events, got %d: %+v", len(gotB), gotB)
	}

	// Verify event ordering within each sink.
	for _, sink := range []struct {
		name   string
		events []ToolProgressEvent
	}{
		{"A", gotA},
		{"B", gotB},
	} {
		if len(sink.events) < 2 {
			continue
		}
		if sink.events[0].Phase != ToolProgressStart {
			t.Errorf("sink %s: first event phase = %v, want ToolProgressStart", sink.name, sink.events[0].Phase)
		}
		if sink.events[1].Phase != ToolProgressDone {
			t.Errorf("sink %s: second event phase = %v, want ToolProgressDone", sink.name, sink.events[1].Phase)
		}
	}
}

// TestWithSinkReplacesProgress verifies that calling WithSink on a context
// that already carries a sink replaces the progress callback rather than
// stacking duplicates.
func TestWithSinkReplacesProgress(t *testing.T) {
	var calls int
	first := func(ToolProgressEvent) { calls++ }
	second := func(ToolProgressEvent) { calls++ }

	ctx := WithSink(context.Background(), first)
	// Replace with a new callback.
	ctx = WithSink(ctx, second)

	// Only the second callback should be active.
	progress := progressFromContext(ctx)
	if progress == nil {
		t.Fatal("expected non-nil progress callback")
	}
	progress(ToolProgressEvent{})
	if calls != 1 {
		t.Errorf("expected 1 call (only the replacement callback), got %d", calls)
	}
}

// TestProgressFromContextNil verifies that a context with no sink returns a
// nil ProgressFunc — a complete no-op for callers that guard with a nil
// check.
func TestProgressFromContextNil(t *testing.T) {
	if p := progressFromContext(context.Background()); p != nil {
		t.Errorf("expected nil ProgressFunc for plain context, got %v", p)
	}
}
