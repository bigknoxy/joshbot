package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// A compressor that records that it ran and returns a fixed summary.
type stubCompressor struct {
	calls int
}

func (c *stubCompressor) CompressMessages(ctx context.Context, model string, messages []providers.Message, budget int) (string, error) {
	c.calls++
	return "summary of the earlier conversation", nil
}

// seedLongHistory runs enough ordinary turns for the session to hold a prefix
// worth compacting.
//
// Two things decide whether a compaction is possible at all. The prefix must
// start before the last compactionKeepTail messages, or there is nothing to
// summarize; and it must be worth more than a quarter of the budget, or
// summarizing it spends a provider call to recover nothing. Four turns of
// long content clears both.
func seedLongHistory(t *testing.T, mgr *mockSessionManager) {
	t.Helper()
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{Choices: []providers.Choice{{
				Message: providers.Message{Role: providers.RoleAssistant, Content: "seeded"},
			}}}, nil
		},
	}
	a := NewAgent(newNonStreamingConfig(), provider, &mockToolExecutor{}, mgr, newMockLogger())
	for i := 0; i < 4; i++ {
		// The first turn carries the weight: only the messages before the
		// compaction tail are summarized, and the machinery skips a prefix
		// that is small next to the budget.
		content := "short"
		if i == 0 {
			content = strings.Repeat("x", 200000)
		}
		if _, err := a.Process(context.Background(), bus.InboundMessage{
			SenderID: "u", Content: content, Channel: "cli", Timestamp: time.Now(),
		}); err != nil {
			t.Fatalf("seed Process: %v", err)
		}
	}
}

// TestProcess_ContextTooLongCompactsAndRetries is the behaviour the issue
// asked for: a turn the provider refused for being too long is compacted and
// run once more, and the answer of that second attempt is the answer.
//
// The answer of a successful retry used to be thrown away: the retry's nil
// error fell into the error path below it, so the user got an error report
// for a turn that had in fact succeeded.
func TestProcess_ContextTooLongCompactsAndRetries(t *testing.T) {
	mgr := newMockSessionManager()
	seedLongHistory(t, mgr)

	calls := 0
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("context_length_exceeded: this request exceeds the model's maximum context length")
			}
			return &providers.ChatResponse{Choices: []providers.Choice{{
				Message: providers.Message{Role: providers.RoleAssistant, Content: "the compacted answer"},
			}}}, nil
		},
	}

	compressor := &stubCompressor{}
	a := NewAgent(newNonStreamingConfig(), provider, &mockToolExecutor{}, mgr, newMockLogger(),
		WithBudgetManager(ctxpkg.NewBudgetManager(ctxpkg.NewRegistry(), 100)),
		WithContextCompressor(compressor))

	got, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "u", Content: "and now the real question", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if calls != 2 {
		t.Fatalf("the turn must be retried exactly once, provider calls = %d", calls)
	}
	if compressor.calls == 0 {
		t.Error("the conversation must actually be compacted, not just re-sent")
	}
	if ReplyError(got) != nil {
		t.Fatalf("a successful retry must not be reported as an error, got %q", got)
	}
	if !strings.Contains(got, "the compacted answer") {
		t.Errorf("the answer of the retry must reach the user, got %q", got)
	}
}

// TestProcess_ContextTooLongTwiceSaysSoPlainly pins the other half: when the
// conversation is still too long after the compaction, the user is told, and
// the turn is not retried forever.
func TestProcess_ContextTooLongTwiceSaysSoPlainly(t *testing.T) {
	mgr := newMockSessionManager()
	seedLongHistory(t, mgr)

	calls := 0
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			calls++
			return nil, errors.New("context_length_exceeded: this request exceeds the model's maximum context length")
		},
	}

	a := NewAgent(newNonStreamingConfig(), provider, &mockToolExecutor{}, mgr, newMockLogger(),
		WithBudgetManager(ctxpkg.NewBudgetManager(ctxpkg.NewRegistry(), 100)),
		WithContextCompressor(&stubCompressor{}))

	got, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "u", Content: "and now the real question", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if calls > 2 {
		t.Fatalf("the retry must be bounded to one attempt, provider calls = %d", calls)
	}
	if !strings.Contains(got, "/new") {
		t.Errorf("the user must be told what to do about it, got %q", got)
	}
}

// TestProcess_RetryKeepsThePersonalityOfTheTurn pins that the retry asks the
// same question. A retry built from a smaller system prompt would drop the
// conversation summary and the personality, and the answer would come back in
// a different voice than the one the user was talking to.
func TestProcess_RetryKeepsThePersonalityOfTheTurn(t *testing.T) {
	mgr := newMockSessionManager()
	seedLongHistory(t, mgr)
	for _, sess := range mgr.sessions {
		sess.Personality = "be extremely terse"
	}

	calls := 0
	var retrySystem string
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("context_length_exceeded: this request exceeds the model's maximum context length")
			}
			retrySystem = req.Messages[0].Content
			return &providers.ChatResponse{Choices: []providers.Choice{{
				Message: providers.Message{Role: providers.RoleAssistant, Content: "terse"},
			}}}, nil
		},
	}

	a := NewAgent(newNonStreamingConfig(), provider, &mockToolExecutor{}, mgr, newMockLogger(),
		WithBudgetManager(ctxpkg.NewBudgetManager(ctxpkg.NewRegistry(), 100)),
		WithContextCompressor(&stubCompressor{}))

	if _, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "u", Content: "and now the real question", Channel: "cli", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if calls < 2 {
		t.Fatalf("the turn must be retried, provider calls = %d", calls)
	}
	if !strings.Contains(retrySystem, "be extremely terse") {
		t.Errorf("the retry must carry the personality of the turn, got:\n%s", retrySystem)
	}
}
