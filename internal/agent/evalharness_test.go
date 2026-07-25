package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// This file is a behavioural eval harness for the ReAct loop.
//
// The distinction from the prompt_*_test.go files in this package matters:
// those score the *text* of the system prompt with substring checks and never
// invoke the agent. This harness runs the real loop — real tool dispatch, real
// session mutation, real context budgeting — against a scripted provider that
// replays a fixed sequence of model turns. No network, no API key, no clock
// dependence.
//
// What it asserts is the wire protocol the loop produces, not prose. The loop's
// actual job is to hand the model a well-formed conversation on every turn; a
// break there degrades or kills the agent in production while unit tests that
// only check the final string stay green.

// scriptedTurn is one model response to replay.
type scriptedTurn struct {
	content   string
	toolCalls []providers.ToolCall
	err       error
	// noChoices simulates a provider returning an empty Choices slice.
	noChoices bool
}

// toolCall is a terser way to declare a scripted tool call.
func toolCall(id, name, args string) providers.ToolCall {
	return providers.ToolCall{
		ID:       id,
		Type:     "function",
		Function: providers.FunctionCall{Name: name, Arguments: args},
	}
}

// scriptedProvider replays turns in order and records every request it was
// given. Recording the requests is the point: it is the only way to see what
// the loop actually sent to the model.
type scriptedProvider struct {
	mu       sync.Mutex
	turns    []scriptedTurn
	calls    int
	requests []providers.ChatRequest
}

func (p *scriptedProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Copy the message slice; the loop appends to its own backing array and
	// would otherwise mutate what we recorded.
	msgs := make([]providers.Message, len(req.Messages))
	copy(msgs, req.Messages)
	req.Messages = msgs
	p.requests = append(p.requests, req)

	idx := p.calls
	p.calls++

	// Past the end of the script the model just stops calling tools, so a
	// scenario that under-specifies its script terminates instead of hanging.
	if idx >= len(p.turns) {
		return &providers.ChatResponse{Choices: []providers.Choice{{
			Message: providers.Message{Role: providers.RoleAssistant, Content: "script exhausted"},
		}}}, nil
	}

	turn := p.turns[idx]
	if turn.err != nil {
		return nil, turn.err
	}
	if turn.noChoices {
		return &providers.ChatResponse{Choices: []providers.Choice{}}, nil
	}
	return &providers.ChatResponse{Choices: []providers.Choice{{
		Message: providers.Message{
			Role:      providers.RoleAssistant,
			Content:   turn.content,
			ToolCalls: turn.toolCalls,
		},
	}}}, nil
}

func (p *scriptedProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func (p *scriptedProvider) Transcribe(ctx context.Context, _ []byte, _ string) (string, error) {
	return "", nil
}

func (p *scriptedProvider) Name() string             { return "scripted" }
func (p *scriptedProvider) Config() providers.Config { return providers.Config{} }

// toolInvocation is one recorded call into the tool layer.
type toolInvocation struct {
	name string
	args map[string]any
}

// recordingTools implements ToolExecutor, recording invocations and returning
// scripted output per tool name.
type recordingTools struct {
	mu      sync.Mutex
	calls   []toolInvocation
	outputs map[string]string
	errs    map[string]error
	schemas []providers.Tool
}

func (r *recordingTools) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, toolInvocation{name: name, args: args})
	if err, ok := r.errs[name]; ok {
		return "", err
	}
	if out, ok := r.outputs[name]; ok {
		return out, nil
	}
	return "ok", nil
}

func (r *recordingTools) ExecuteWithContext(ctx context.Context, name string, args map[string]any, channel, channelID string, cb func(tools.AsyncResult)) (tools.ToolResult, bool) {
	out, err := r.Execute(ctx, name, args)
	if err != nil {
		return tools.ToolResult{Error: err}, false
	}
	return tools.ToolResult{Output: out}, false
}

func (r *recordingTools) GetSchemas() []providers.Tool { return r.schemas }

func (r *recordingTools) invocations() []toolInvocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]toolInvocation, len(r.calls))
	copy(out, r.calls)
	return out
}

// trajectoryScenario describes one behavioural run.
type trajectoryScenario struct {
	name  string
	turns []scriptedTurn
	// userMessage is what the user sends; defaults to "do the thing".
	userMessage string
	// history seeds the session before the run.
	history []session.Message
	// toolOutputs / toolErrors script the tool layer.
	toolOutputs map[string]string
	toolErrors  map[string]error
	// tightBudget wires a budget manager and compressor with almost no room,
	// which forces the context-reduction paths to run.
	tightBudget bool
	// memoryWindow, when non-zero, caps how many history messages are kept.
	memoryWindow  int
	maxIterations int
}

// trajectoryResult is everything observable after a run.
type trajectoryResult struct {
	requests    []providers.ChatRequest
	invocations []toolInvocation
	final       string
	err         error
	sess        *session.Session
}

// requestMessages returns the messages sent on the nth model call (0-indexed).
func (r trajectoryResult) requestMessages(n int) []providers.Message {
	if n >= len(r.requests) {
		return nil
	}
	return r.requests[n].Messages
}

// runEval executes a scenario against the real agent loop.
func runTrajectoryEval(t *testing.T, sc trajectoryScenario) trajectoryResult {
	t.Helper()

	// The prompt cache is a package-level singleton keyed on file mtimes; two
	// scenarios with empty workspaces would otherwise share a cached prompt.
	InvalidatePromptCache()

	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "small"
	cfg.Agents.Defaults.MaxTokens = 100
	if sc.memoryWindow > 0 {
		cfg.Agents.Defaults.MemoryWindow = sc.memoryWindow
	}
	if sc.maxIterations > 0 {
		cfg.Agents.Defaults.MaxToolIterations = sc.maxIterations
	}

	provider := &scriptedProvider{turns: sc.turns}
	toolLayer := &recordingTools{outputs: sc.toolOutputs, errs: sc.toolErrors}

	sessions := newMockSessionManager()
	sess, err := sessions.GetOrCreate(context.Background(), "cli:eval")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	for _, m := range sc.history {
		if m.Timestamp.IsZero() {
			m.Timestamp = time.Now()
		}
		sess.AddMessage(m)
	}

	opts := []Option{}
	if sc.tightBudget {
		// Margin 3800 against the 4096-token "small" window drives the budget
		// to its 256-token floor, so any real history exceeds it.
		opts = append(opts,
			WithBudgetManager(ctxpkg.NewBudgetManager(ctxpkg.NewRegistry(), 3800)),
			WithCompressor(&ctxpkg.Compressor{}),
		)
	}
	if sc.maxIterations > 0 {
		opts = append(opts, WithMaxIterations(sc.maxIterations))
	}

	a := NewAgent(cfg, provider, toolLayer, sessions, newMockLogger(), opts...)

	userMsg := sc.userMessage
	if userMsg == "" {
		userMsg = "do the thing"
	}

	final, err := a.Process(context.Background(), bus.InboundMessage{
		Channel:  "cli",
		SenderID: "eval",
		Content:  userMsg,
	})

	return trajectoryResult{
		requests:    provider.requests,
		invocations: toolLayer.invocations(),
		final:       final,
		err:         err,
		sess:        sess,
	}
}

// assertProtocolInvariants checks the properties that must hold on every
// request the loop sends, in every scenario. These are the rules an
// OpenAI-compatible endpoint enforces with a 400, so a violation here is an
// outage in production rather than a style issue.
//
// Applying this to every scenario is deliberate: any eval added later inherits
// the safety net without having to remember it.
func assertProtocolInvariants(t *testing.T, res trajectoryResult) {
	t.Helper()

	for reqIdx, req := range res.requests {
		msgs := req.Messages

		if len(msgs) == 0 {
			t.Errorf("request %d: no messages sent", reqIdx)
			continue
		}
		if msgs[0].Role != providers.RoleSystem {
			t.Errorf("request %d: first message role = %q, want system", reqIdx, msgs[0].Role)
		}

		// Every tool_call announced by an assistant message must be answered,
		// and every tool message must answer a call that was announced first.
		announced := map[string]int{} // id -> index announced at
		answered := map[string]bool{}

		for i, m := range msgs {
			if m.Role == "" {
				t.Errorf("request %d, message %d: empty role", reqIdx, i)
			}

			for _, tc := range m.ToolCalls {
				if m.Role != providers.RoleAssistant {
					t.Errorf("request %d, message %d: tool calls on a %q message", reqIdx, i, m.Role)
				}
				if tc.ID == "" {
					t.Errorf("request %d, message %d: assistant announced a tool call with an empty id", reqIdx, i)
					continue
				}
				announced[tc.ID] = i
			}

			if m.Role != providers.RoleTool {
				continue
			}
			if m.ToolCallID == "" {
				t.Errorf("request %d, message %d: tool message has an empty tool_call_id; "+
					"an OpenAI-compatible provider rejects this with a 400", reqIdx, i)
				continue
			}
			at, ok := announced[m.ToolCallID]
			if !ok {
				t.Errorf("request %d, message %d: tool message answers tool_call_id %q, "+
					"which no preceding assistant message announced", reqIdx, i, m.ToolCallID)
				continue
			}
			if at > i {
				t.Errorf("request %d, message %d: tool result precedes the call that announced it", reqIdx, i)
			}
			answered[m.ToolCallID] = true
		}

		for id, at := range announced {
			if !answered[id] {
				t.Errorf("request %d: tool call %q announced at message %d never received a result; "+
					"an OpenAI-compatible provider rejects this with a 400", reqIdx, id, at)
			}
		}
	}
}

// toolResultFor returns the content of the tool message answering id.
func toolResultFor(msgs []providers.Message, id string) (string, bool) {
	for _, m := range msgs {
		if m.Role == providers.RoleTool && m.ToolCallID == id {
			return m.Content, true
		}
	}
	return "", false
}

// countRole counts messages with the given role.
func countRole(msgs []providers.Message, role providers.MessageRole) int {
	n := 0
	for _, m := range msgs {
		if m.Role == role {
			n++
		}
	}
	return n
}

// padding builds filler content of a known token cost.
func padding(n int) string { return strings.Repeat("x", n) }

// toolExchange returns an assistant tool call plus its result, as session
// messages, for seeding history.
func toolExchange(id, name, result string) []session.Message {
	return []session.Message{
		{
			Role:      session.RoleAssistant,
			ToolCalls: []session.ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(`{}`)}},
		},
		{
			Role:       session.RoleTool,
			Content:    result,
			ToolCallID: id,
		},
	}
}

// chatHistory builds n user/assistant pairs of the given content size.
func chatHistory(pairs, size int) []session.Message {
	msgs := make([]session.Message, 0, pairs*2)
	for i := 0; i < pairs; i++ {
		msgs = append(msgs,
			session.Message{Role: session.RoleUser, Content: fmt.Sprintf("u%d %s", i, padding(size))},
			session.Message{Role: session.RoleAssistant, Content: fmt.Sprintf("a%d %s", i, padding(size))},
		)
	}
	return msgs
}
