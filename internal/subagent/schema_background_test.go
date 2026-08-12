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

// Output schemas and background runs are the two subagent boundaries that fail
// silently when they regress: an unvalidated output is prose handed to a caller
// that asked for fields, and a background run that swallows a cancellation or a
// panic reports success over work that never happened.

// scriptedProvider replies with a canned sequence, recording every request so a
// test can assert what the model was actually offered.
type scriptedProvider struct {
	mu       sync.Mutex
	replies  []providers.Message
	requests []providers.ChatRequest
	block    chan struct{} // when non-nil, Chat blocks until closed or ctx dies
}

func (p *scriptedProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	var msg providers.Message
	if len(p.replies) > 0 {
		msg = p.replies[0]
		p.replies = p.replies[1:]
	} else {
		msg = providers.Message{Content: "done"}
	}
	p.mu.Unlock()
	return &providers.ChatResponse{Choices: []providers.Choice{{Message: msg}}}, nil
}

func (p *scriptedProvider) calls() []providers.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providers.ChatRequest(nil), p.requests...)
}

func (p *scriptedProvider) ChatStream(context.Context, providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, errors.New("not implemented")
}
func (p *scriptedProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", errors.New("not implemented")
}
func (p *scriptedProvider) Name() string             { return "scripted" }
func (p *scriptedProvider) Config() providers.Config { return providers.DefaultConfig() }

type panicProvider struct{}

func (panicProvider) Chat(context.Context, providers.ChatRequest) (*providers.ChatResponse, error) {
	panic("boom")
}
func (panicProvider) ChatStream(context.Context, providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, errors.New("no")
}
func (panicProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", errors.New("no")
}
func (panicProvider) Name() string             { return "panic" }
func (panicProvider) Config() providers.Config { return providers.DefaultConfig() }

func TestOutputSchemaRepairsOnceThenFailsRatherThanReturningProse(t *testing.T) {
	schema := &OutputSchema{Required: []string{"summary", "count"}, Types: map[string]string{"count": "number"}}

	// One bad answer, then a good one: the run repairs and succeeds. MaxIter
	// is 1 on purpose — the repair must not spend the caller's iteration.
	prov := &scriptedProvider{replies: []providers.Message{
		{Content: "I looked at three files."},
		{Content: "```json\n{\"summary\":\"ok\",\"count\":3}\n```"},
	}}
	res, err := NewRunner(prov, "m").Run(context.Background(), "go", Config{MaxIter: 1, OutputSchema: schema})
	if err != nil {
		t.Fatalf("a repairable answer failed: %v", err)
	}
	if !strings.Contains(res.Output, "\"count\"") {
		t.Errorf("output lost the JSON: %q", res.Output)
	}
	if n := len(prov.calls()); n != 2 {
		t.Errorf("provider called %d time(s), want 2 — the repair attempt is missing", n)
	}

	// Two bad answers: an error, not a success carrying prose. Reporting
	// success here hands the caller something that does not parse.
	bad := &scriptedProvider{replies: []providers.Message{
		{Content: "still prose"},
		{Content: "{\"summary\":\"ok\"}"},
	}}
	if res, err := NewRunner(bad, "m").Run(context.Background(), "go", Config{MaxIter: 1, OutputSchema: schema}); err == nil {
		t.Fatalf("unschema'd output was returned as success: %q", res.Output)
	}
	if n := len(bad.calls()); n != 2 {
		t.Errorf("provider called %d time(s), want 2 — repair must not loop", n)
	}

	// A wrong type is a violation too, not just a missing key.
	if err := schema.Validate(`{"summary":"ok","count":"three"}`); err == nil {
		t.Error("a string in a number field validated")
	}
	// And no schema means no constraint — plain prose still passes.
	var none *OutputSchema
	if err := none.Validate("prose"); err != nil {
		t.Errorf("a nil schema rejected output: %v", err)
	}
}

func TestSchemaInstructionsReachTheModel(t *testing.T) {
	prov := &scriptedProvider{replies: []providers.Message{{Content: `{"summary":"x"}`}}}
	r := NewRunner(prov, "m")
	if _, err := r.Run(context.Background(), "go", Config{OutputSchema: &OutputSchema{Required: []string{"summary"}}}); err != nil {
		t.Fatal(err)
	}
	// The schema instruction rides the user turn, next to the task it applies
	// to. A model never told the shape fails the first attempt by definition.
	user := prov.calls()[0].Messages[1].Content
	if !strings.Contains(user, "summary") {
		t.Errorf("the required keys never reached the prompt:\n%s", user)
	}
}

func TestBackgroundRunNotifiesOnceAndSettlesTheHandleFirst(t *testing.T) {
	prov := &scriptedProvider{replies: []providers.Message{{Content: "background answer"}}}
	r := NewRunner(prov, "m")

	var calls int
	seen := make(chan string, 4)
	var h *Handle
	h = r.RunBackground(context.Background(), "go", Config{}, func(res *SubResult, err error) {
		calls++
		// The callback must see a settled handle: a notify that fires before
		// the result is stored makes every callback-driven caller race.
		if _, _, ok := h.Result(); !ok {
			seen <- "handle not settled when notify fired"
			return
		}
		if err != nil {
			seen <- "err: " + err.Error()
			return
		}
		seen <- res.Output
	})

	select {
	case got := <-seen:
		if got != "background answer" {
			t.Fatalf("notify got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notify never fired")
	}

	res, err := h.Wait(context.Background())
	if err != nil || res.Output != "background answer" {
		t.Fatalf("Wait after completion: %v / %+v", err, res)
	}
	if calls != 1 {
		t.Errorf("notify fired %d times, want 1", calls)
	}
}

func TestBackgroundRunIsCancellableAndReportsItRatherThanHanging(t *testing.T) {
	prov := &scriptedProvider{block: make(chan struct{})}
	r := NewRunner(prov, "m")

	h := r.RunBackground(context.Background(), "go", Config{}, nil)
	if _, _, ok := h.Result(); ok {
		t.Fatal("Result reported done while the run was still blocked")
	}
	h.Cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.Wait(ctx); err == nil {
		t.Fatal("a cancelled run reported success")
	}
}

func TestBackgroundRunTurnsAPanicIntoAnErrorInsteadOfKillingTheProcess(t *testing.T) {
	r := NewRunner(panicProvider{}, "m")
	h := r.RunBackground(context.Background(), "go", Config{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := h.Wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("panic in a background run surfaced as %v", err)
	}
}
