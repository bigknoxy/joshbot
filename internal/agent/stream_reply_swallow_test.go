package agent

// Regression tests for issue #283: the max-iteration and timeout replies are
// returned as plain text with a nil error *after* the turn has already
// streamed narration to the sink. Every streaming consumer decides "was the
// answer shown?" by "did anything stream?" (the CLI's didStream(), the
// Telegram streamer's Finish contract), so a streamed turn silently swallows
// exactly the replies that explain why the turn stopped.
//
// The invariant under test: when a stream sink is attached, every synthesized
// final reply (max-iteration, timeout) must be emitted through the sink, so
// the consumer's delivery decision is correct.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// maxIterationStreamingProvider always answers with a tool call that never
// resolves to a real tool, so the ReAct loop runs its full iteration budget
// and exits via the max-iteration path. Narration is streamed first so the
// sink already holds content — the exact shape that swallowed the final reply.
func maxIterationStreamingProvider() *mockProvider {
	return &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 5)
			ch <- streamChunk(streamTextDelta(0, "Let me look into that."))
			ch <- streamChunk(streamToolCallDelta(0, providers.ToolCall{
				Index: 0,
				ID:    "call_1",
				Type:  "function",
				Function: providers.FunctionCall{
					Name:      "no_such_tool",
					Arguments: `{}`,
				},
			}))
			ch <- streamChunk(streamFinishDelta(0, "tool_calls"))
			close(ch)
			return ch, nil
		},
	}
}

// A max-iteration turn that has already streamed narration must still deliver
// its final "Hit the max iteration limit" reply through the sink. Without it,
// the CLI's didStream() is already true and the reply is dropped on the floor
// — the reply reached nobody.
func TestStreaming_MaxIterationsReplyReachesSink(t *testing.T) {
	cfg := newStreamingConfig()

	agent := NewAgent(cfg, maxIterationStreamingProvider(), &mockToolExecutor{}, newMockSessionManager(), newMockLogger(),
		WithMaxIterations(1))

	var sinkText strings.Builder
	ctx := WithStreamSink(context.Background(), func(e StreamEvent) { sinkText.WriteString(e.Delta) })

	response, err := agent.Process(ctx, bus.InboundMessage{
		SenderID: "user123", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if !strings.Contains(response, "Hit the max iteration limit") {
		t.Fatalf("Process() = %q, want the max-iteration reply", response)
	}
	if !strings.Contains(sinkText.String(), "Hit the max iteration limit") {
		t.Errorf("sink saw %q — the max-iteration reply was streamed away and never delivered",
			sinkText.String())
	}
}

// hangingProvider never answers: it blocks until the request context dies.
// Use via non-streaming Chat (streaming off) so the single call hangs and the
// turn ends on the agent's own deadline.
func newHangingProvider() *mockProvider {
	return &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
}

// A turn that times out after streaming (or in a headless caller that expects
// the sink to carry everything) must emit the timeout reply through the sink,
// not just return it as text that the didStream() gate then suppresses.
func TestTimeoutReplyReachesSink(t *testing.T) {
	cfg := newNonStreamingConfig()

	sessions := newMockSessionManager()
	agent := NewAgent(cfg, newHangingProvider(), &mockToolExecutor{}, sessions, newMockLogger(),
		WithTimeout(50*time.Millisecond))

	var sinkText strings.Builder
	ctx := WithStreamSink(context.Background(), func(e StreamEvent) { sinkText.WriteString(e.Delta) })

	response, err := agent.Process(ctx, bus.InboundMessage{
		SenderID: "user123", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if !strings.Contains(strings.ToLower(response), "took too long") {
		t.Fatalf("Process() = %q, want the timeout reply", response)
	}
	if !strings.Contains(strings.ToLower(sinkText.String()), "took too long") {
		t.Errorf("sink saw %q — the timeout reply was never delivered", sinkText.String())
	}
}

// The timeout path must persist the session before returning: it used to
// return before the save at the end of Process, so a turn that exhausted its
// budget vanished from the session file and the next message re-explored from
// scratch. The save must use a context that is not the spent turn context,
// because the real Manager.Save refuses one.
func TestTimeoutTurnPersistsSession(t *testing.T) {
	cfg := newNonStreamingConfig()

	sessions := newMockSessionManager()
	agent := NewAgent(cfg, newHangingProvider(), &mockToolExecutor{}, sessions, newMockLogger(),
		WithTimeout(50*time.Millisecond))

	ctx := WithStreamSink(context.Background(), func(StreamEvent) {})
	response, err := agent.Process(ctx, bus.InboundMessage{
		SenderID: "user123", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !strings.Contains(strings.ToLower(response), "took too long") {
		t.Fatalf("Process() = %q, want the timeout reply", response)
	}

	if saves := sessions.saves(); saves == 0 {
		t.Fatal("a timed-out turn saved no session — the conversation was lost")
	}
}

// A max-iteration turn that crosses the deadline mid-turn (a slow tool) must
// still land its checkpoint and session: the checkpoint is what makes /resume
// work, and the session is the whole conversation. Both writes used to be
// derived from the spent turn context and failed instantly.
func TestMaxIterationsNearDeadlinePersistsCheckpointAndSession(t *testing.T) {
	cfg := newNonStreamingConfig()

	sessions := newMockSessionManager()
	tools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, args map[string]any) (string, error) {
			// Blow the 50ms turn budget from inside the tool, so the
			// deadline fires before the loop exits onto the checkpoint path.
			time.Sleep(300 * time.Millisecond)
			return "done", nil
		},
		schemas: []providers.Tool{
			{
				Type: "function",
				Function: providers.FunctionDefinition{
					Name:        "slow_tool",
					Description: "slow tool",
				},
			},
		},
	}

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Let me check that.",
							ToolCalls: []providers.ToolCall{
								{
									ID:   "call_1",
									Type: "function",
									Function: providers.FunctionCall{
										Name:      "slow_tool",
										Arguments: `{}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					},
				},
			}, nil
		},
	}

	agent := NewAgent(cfg, provider, tools, sessions, newMockLogger(),
		WithMaxIterations(1), WithTimeout(50*time.Millisecond))

	response, err := agent.Process(context.Background(), bus.InboundMessage{
		SenderID: "user123", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	// The /resume hint is gated on the checkpoint save having succeeded, so
	// its presence is the user-visible proof that persistence survived the
	// spent deadline.
	if !strings.Contains(response, "/resume") {
		t.Errorf("reply %q — the checkpoint was not persisted, /resume is impossible", response)
	}
	if saves := sessions.saves(); saves == 0 {
		t.Fatal("no session was saved for a max-iteration turn past its deadline")
	}
}
