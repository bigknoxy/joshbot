// Package demo runs a scripted, provider-free demonstration of joshbot.
//
// The point of the demo is that it needs nothing: no API key, no network,
// no provider. It runs the real agent — the real ReAct loop, the real
// tools, the real session log — against a provider that replays a short
// script, so what the viewer sees is the binary actually working rather
// than a transcript of somebody else's.
//
// It is also a self-test with a human-shaped output: if the tools or the
// agent loop break, the demo stops making sense.
package demo

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// Prompt is what the demo asks the agent to do.
const Prompt = "Write a greeting into hello.txt, then read it back to me."

// ScriptedProvider replays a fixed sequence of assistant turns.
//
// It is a providers.Provider like any other, so the agent cannot tell the
// difference: the same code path, the same request building, the same
// tool-call handling. Only the answers are pre-written.
type ScriptedProvider struct {
	steps []providers.Message
	n     int
	out   io.Writer
}

// NewScriptedProvider returns a provider that replays steps.
func NewScriptedProvider(out io.Writer, steps ...providers.Message) *ScriptedProvider {
	return &ScriptedProvider{steps: steps, out: out}
}

// Name returns the provider name.
func (p *ScriptedProvider) Name() string { return "demo" }

// Config returns an empty provider configuration; the demo has no
// credential to describe.
func (p *ScriptedProvider) Config() providers.Config { return providers.Config{} }

// Chat returns the next scripted turn.
func (p *ScriptedProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if p.n >= len(p.steps) {
		return nil, fmt.Errorf("demo: the script ran out after %d turns", p.n)
	}
	msg := p.steps[p.n]
	p.n++
	p.narrate(msg)
	return &providers.ChatResponse{
		ID:      fmt.Sprintf("demo-%d", p.n),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []providers.Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: "stop",
		}},
	}, nil
}

// ChatStream streams the next scripted turn as a single chunk, so a caller
// that streams gets the same answer as one that does not.
func (p *ScriptedProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{
		ID:      resp.ID,
		Object:  "chat.completion.chunk",
		Created: resp.Created,
		Model:   resp.Model,
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.Message{
				Role:      providers.RoleAssistant,
				Content:   resp.Choices[0].Message.Content,
				ToolCalls: resp.Choices[0].Message.ToolCalls,
			},
		}},
	}
	close(ch)
	return ch, nil
}

// Transcribe is not part of the demo.
func (p *ScriptedProvider) Transcribe(_ context.Context, _ []byte, _ string) (string, error) {
	return "", fmt.Errorf("demo: transcription is not part of the demo")
}

// narrate prints what the model just said, so the transcript reads as a
// conversation rather than a log.
//
// The model's final answer is not narrated: the demo prints it once, as
// joshbot's reply, and a transcript that says the same thing twice is
// noise.
func (p *ScriptedProvider) narrate(msg providers.Message) {
	if p.out == nil {
		return
	}
	for _, tc := range msg.ToolCalls {
		fmt.Fprintf(p.out, "  %-8s tool call   %s(%s)\n", "model", tc.Function.Name, compactArgs(tc.Function.Arguments))
	}
}

// compactArgs renders the tool arguments on one line, with the bulky parts
// trimmed: a demo transcript is not the place for a whole file's contents.
func compactArgs(args string) string {
	const max = 60
	s := strings.Join(strings.Fields(args), " ")
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// narratingExecutor prints every tool call and its result as it happens.
type narratingExecutor struct {
	inner *tools.Registry
	out   io.Writer
}

// Execute runs a tool, narrating it.
func (n *narratingExecutor) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	res, err := n.inner.Execute(ctx, name, args)
	n.narrate(err, res)
	return res, err
}

// ExecuteWithContext runs a tool with the turn's channel context, narrating
// it.
func (n *narratingExecutor) ExecuteWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	channel, channelID string,
	callback func(tools.AsyncResult),
) (tools.ToolResult, bool) {
	res, ok := n.inner.ExecuteWithContext(ctx, name, args, channel, channelID, callback)
	n.narrate(res.Error, res.Output)
	return res, ok
}

// narrate prints a tool's outcome.
func (n *narratingExecutor) narrate(err error, output string) {
	if n.out == nil {
		return
	}
	switch {
	case err != nil:
		fmt.Fprintf(n.out, "  %-8s error       %v\n", "tool", err)
	case strings.TrimSpace(output) == "":
		fmt.Fprintf(n.out, "  %-8s done\n", "tool")
	default:
		fmt.Fprintf(n.out, "  %-8s %s\n", "tool", brief(output))
	}
}

// GetSchemas returns the tool schemas.
func (n *narratingExecutor) GetSchemas() []providers.Tool { return n.inner.GetSchemas() }

// GetSpillConfig returns the registry's spill configuration.
func (n *narratingExecutor) GetSpillConfig() tools.SpillConfig { return n.inner.GetSpillConfig() }

// brief renders a tool result for the transcript: the first few lines,
// trimmed, with the rest elided.
//
// A file read comes back as a header and then the contents, so the contents
// are the interesting part — the first line alone would show the path and
// nothing else, which is exactly the "a path is not an answer" failure the
// spill work fixed.
func brief(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	const maxLines = 3
	shown := lines
	elided := false
	if len(shown) > maxLines {
		shown = shown[:maxLines]
		elided = true
	}
	out := strings.Join(shown, "\n           ")
	const max = 120
	if len(out) > max {
		out = out[:max-1] + "…"
	}
	if elided {
		out += " …"
	}
	return out
}

// Script returns the assistant turns the demo replays.
//
// The script exercises the loop the way a real turn does: a tool call, the
// tool's result, another tool call, and then an answer that uses what the
// tools returned.
func Script() []providers.Message {
	return []providers.Message{
		{
			Role: providers.RoleAssistant,
			ToolCalls: []providers.ToolCall{{
				ID:   "demo-call-1",
				Type: "function",
				Function: providers.FunctionCall{
					Name:      "write_file",
					Arguments: `{"path":"hello.txt","content":"Hello from joshbot!\n"}`,
				},
			}},
		},
		{
			Role: providers.RoleAssistant,
			ToolCalls: []providers.ToolCall{{
				ID:   "demo-call-2",
				Type: "function",
				Function: providers.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"hello.txt"}`,
				},
			}},
		},
		{
			Role:    providers.RoleAssistant,
			Content: "It says: Hello from joshbot! — written to hello.txt and read back.",
		},
	}
}

// memorySessions is an in-memory session store.
//
// The demo does not keep history — a demonstration has nothing to remember
// — and a real session write fsyncs, which on a loaded disk can take
// seconds. The pause would land in the middle of the demo's own transcript,
// which is the one place where a pause is most visible.
//
// The agent's own behaviour is untouched: the same session object, the same
// messages, the same code path. Only the durability is dropped.
type memorySessions struct {
	mu   sync.Mutex
	seen map[string]*session.Session
}

// GetOrCreate returns the session for key, creating it on first use.
func (m *memorySessions) GetOrCreate(_ context.Context, key string) (*session.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]*session.Session{}
	}
	if s, ok := m.seen[key]; ok {
		return s, nil
	}
	s := session.NewSession(key)
	m.seen[key] = s
	return s, nil
}

// Save keeps the session in memory.
func (m *memorySessions) Save(_ context.Context, s *session.Session) error {
	if s == nil {
		return fmt.Errorf("demo: session is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]*session.Session{}
	}
	m.seen[s.ID] = s
	return nil
}

// Delete drops the session for key.
func (m *memorySessions) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.seen, key)
	return nil
}

// Run performs the demo turn in workspace and writes the transcript to out.
//
// It returns the agent's final answer.
func Run(ctx context.Context, workspace string, out io.Writer) (string, error) {
	// The demo's own transcript is the output. The agent's logs would land
	// in the middle of it and turn a two-line conversation into a log dump,
	// so they are raised to the level where only trouble is printed.
	//
	// The level is restored on the way out. It is a process-wide setting,
	// and a demo that leaves the log quiet behind it changes the behaviour
	// of whatever runs next — which, in this repository, is the rest of the
	// test suite.
	//
	// JOSHBOT_DEMO_DEBUG=1 turns the logs back on, which is how a slow demo
	// gets diagnosed.
	previousLevel := log.GetLevel()
	defer log.SetLevel(previousLevel)
	if os.Getenv("JOSHBOT_DEMO_DEBUG") != "" {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.ErrorLevel)
	}

	cfg := config.Defaults()

	registry := tools.RegistryWithDefaults(
		workspace,
		true, // restrict to the workspace
		30,   // shell timeout
		30,   // web timeout
		nil,  // no message sender
		nil,  // no shell allow list
		nil,  // no extra filesystem paths
		nil,  // no skill loader
	)

	sessions := &memorySessions{}

	provider := NewScriptedProvider(out, Script()...)
	executor := &narratingExecutor{inner: registry, out: out}

	ag := agent.NewAgent(cfg, provider, executor, sessions, nil, agent.WithTimeout(60*time.Second))

	if out != nil {
		fmt.Fprintf(out, "  %-8s %s\n", "you", Prompt)
	}

	answer, err := ag.Process(ctx, bus.InboundMessage{
		SenderID:  "demo",
		Content:   Prompt,
		Channel:   "demo",
		Timestamp: time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("demo: the turn failed: %w", err)
	}

	// The answer is scripted, so it can claim something that did not happen.
	// Check it: the demo is also a self-test, and a demonstration that
	// prints "it wrote the file" when the file was never written is worse
	// than no demonstration at all.
	if err := checkWritten(workspace); err != nil {
		return "", err
	}
	return answer, nil
}

// checkWritten verifies that the demo's file exists with the content the
// script wrote.
func checkWritten(workspace string) error {
	path := filepath.Join(workspace, "hello.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("demo: the turn claimed to write %s but it is not there: %w", path, err)
	}
	if !strings.Contains(string(data), "Hello from joshbot!") {
		return fmt.Errorf("demo: %s does not hold the greeting: %q", path, data)
	}
	return nil
}
