package agent

import (
	"context"
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
// supplied via WithProgressCallback, the ReAct loop emits a start event
// before executing a tool and a done event after, with elapsed time and
// success recorded, and that the callback is never invoked when nil.
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
	agent := NewAgent(cfg, provider, tools, sessions, logger,
		WithProgressCallback(func(e ToolProgressEvent) {
			events = append(events, e)
		}),
	)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "run echo hi",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	if _, err := agent.Process(context.Background(), msg); err != nil {
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
