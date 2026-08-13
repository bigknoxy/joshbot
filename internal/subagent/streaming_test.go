package subagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// A streaming subagent is driven entirely through its sink: the caller sees
// nothing else. Three regressions live here and all of them look like "the
// subagent went quiet" rather than like an error — the run silently falling
// back to the non-streaming path, a tool call executed without ever being
// announced, and a completed tool that never reports back.

// streamingProvider replays a scripted sequence of chunk batches, one batch per
// call, and records the requests it was given.
type streamingProvider struct {
	mu       sync.Mutex
	batches  [][]providers.StreamChunk
	requests []providers.ChatRequest
	chatHits int
}

func (p *streamingProvider) ChatStream(_ context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	var batch []providers.StreamChunk
	if len(p.batches) > 0 {
		batch = p.batches[0]
		p.batches = p.batches[1:]
	}
	p.mu.Unlock()

	ch := make(chan providers.StreamChunk, len(batch)+1)
	for _, c := range batch {
		ch <- c
	}
	close(ch)
	return ch, nil
}

// Chat must never be reached while streaming is enabled. Counting the hits
// rather than failing here keeps the assertion in the test body, where the
// failure message can say what actually happened.
func (p *streamingProvider) Chat(context.Context, providers.ChatRequest) (*providers.ChatResponse, error) {
	p.mu.Lock()
	p.chatHits++
	p.mu.Unlock()
	return &providers.ChatResponse{Choices: []providers.Choice{{Message: providers.Message{Content: "non-streamed"}}}}, nil
}

func (p *streamingProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", errors.New("not implemented")
}
func (p *streamingProvider) Name() string             { return "streaming" }
func (p *streamingProvider) Config() providers.Config { return providers.DefaultConfig() }

func (p *streamingProvider) calls() (reqs []providers.ChatRequest, chatHits int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providers.ChatRequest(nil), p.requests...), p.chatHits
}

// recordingSink captures the sink calls in order, so a missing or reordered
// event is visible rather than merely absent from a count.
type recordingSink struct {
	mu     sync.Mutex
	events []string
}

func (s *recordingSink) add(e string) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *recordingSink) OnTextDelta(content string) { s.add("text:" + content) }
func (s *recordingSink) OnToolStart(tool string, args map[string]any) {
	s.add("start:" + tool + "(" + summarizeArgs(args) + ")")
}
func (s *recordingSink) OnToolDone(tool string, result ToolResult, _ time.Duration) {
	s.add("done:" + tool + "=" + result.Output)
}

func (s *recordingSink) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// echoExecutor returns a fixed output and records the calls it received, so a
// tool announced to the sink but never executed (or the reverse) is caught.
type echoExecutor struct {
	mu     sync.Mutex
	called []string
}

func (e *echoExecutor) GetSchemas() []providers.Tool {
	return []providers.Tool{{Type: "function", Function: providers.FunctionDefinition{Name: "lookup"}}}
}

func (e *echoExecutor) ExecuteWithContext(_ context.Context, name string, args map[string]any, _, _ string, _ func(AsyncResult)) (ToolResult, bool) {
	e.mu.Lock()
	e.called = append(e.called, name+"("+summarizeArgs(args)+")")
	e.mu.Unlock()
	return ToolResult{Output: "42"}, false
}

func (e *echoExecutor) calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.called...)
}

func chunk(msg providers.Message) providers.StreamChunk {
	return providers.StreamChunk{Choices: []providers.StreamChoice{{Delta: msg}}}
}

// stop terminates a batch: the accumulator rejects a stream that ends without
// a finish reason, because a truncated stream is not a complete answer.
func stop(reason string) providers.StreamChunk {
	return providers.StreamChunk{Choices: []providers.StreamChoice{{FinishReason: reason}}}
}

func TestStreamingSubagentReportsItsToolCallsAndTextThroughTheSink(t *testing.T) {
	prov := &streamingProvider{batches: [][]providers.StreamChunk{
		// Turn one: a tool call, arrived in fragments the way a real stream
		// delivers it.
		{
			chunk(providers.Message{ToolCalls: []providers.ToolCall{{
				Index: 0, ID: "c1", Type: "function",
				Function: providers.FunctionCall{Name: "lookup", Arguments: `{"q":`},
			}}}),
			chunk(providers.Message{ToolCalls: []providers.ToolCall{{
				Index:    0,
				Function: providers.FunctionCall{Arguments: `"meaning"}`},
			}}}),
			stop("tool_calls"),
		},
		// Turn two: the final answer.
		{chunk(providers.Message{Content: "the answer is 42"}), stop("stop")},
	}}
	sink := &recordingSink{}
	exec := &echoExecutor{}

	r := NewRunner(prov, "m", WithStreaming(sink), WithTools(exec))
	res, err := r.Run(context.Background(), "go", Config{MaxIter: 3})
	if err != nil {
		t.Fatalf("streaming run failed: %v", err)
	}
	if res.Output != "the answer is 42" {
		t.Errorf("output %q — the accumulated stream did not become the result", res.Output)
	}

	reqs, chatHits := prov.calls()
	// Falling back to Chat is the silent regression: the run still answers, so
	// only the absence of every sink event would give it away.
	if chatHits != 0 {
		t.Errorf("the non-streaming Chat path was used %d time(s) despite WithStreaming", chatHits)
	}
	if len(reqs) != 2 {
		t.Fatalf("ChatStream called %d time(s), want 2 (tool turn + answer turn)", len(reqs))
	}
	if !reqs[0].Stream {
		t.Error("the request did not set Stream, so a real provider would answer non-streamed")
	}

	// The tool must actually run, with the arguments reassembled across chunk
	// boundaries — a fragment dropped by the accumulator yields empty args and
	// a confident wrong answer.
	if got := exec.calls(); len(got) != 1 || got[0] != "lookup(q=meaning)" {
		t.Fatalf("tool executions %v, want one lookup(q=meaning)", got)
	}

	// And the sink must see start, done and the text, in that order.
	want := []string{"start:lookup(q=meaning)", "done:lookup=42", "text:the answer is 42"}
	got := sink.seen()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sink saw %v, want %v", got, want)
	}

	// The tool result has to reach the model on the next turn, or the subagent
	// answers from nothing.
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != providers.RoleTool || last.Content != "42" {
		t.Errorf("the tool result never reached the model: %+v", last)
	}
}

// A provider that refuses to stream must surface as a run failure, not as a
// silent empty answer: the caller of a subagent has no channel to notice on.
func TestStreamingSubagentSurfacesAStreamOpenFailure(t *testing.T) {
	r := NewRunner(brokenStreamProvider{&streamingProvider{}}, "m", WithStreaming(&recordingSink{}))
	if res, err := r.Run(context.Background(), "go", Config{MaxIter: 1}); err == nil {
		t.Fatalf("a failed stream reported success: %+v", res)
	}
}

type brokenStreamProvider struct{ *streamingProvider }

func (brokenStreamProvider) ChatStream(context.Context, providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, errors.New("stream refused")
}
