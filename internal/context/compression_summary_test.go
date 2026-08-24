package contextpkg

// This file targets GitHub issue #74: CompressMessages returned a provider's
// summary verbatim once a conversation exceeded 50 messages, without checking
// that the summary had any content. A choice with empty (or whitespace-only,
// or implausibly short) Content made the function return ("", nil) — an empty
// compressed context, no error — and skipped the deterministic
// newest-backwards fallback below it because the early return had already
// fired.
//
// Following the scripted-provider convention from
// internal/agent/evalharness_test.go: no network, no API key, no clock
// dependence. scriptedProvider below replays a single configured response (or
// error) and records the context.Context it was called with, which is what
// lets TestCompressMessages_ManyMessages_ContextPropagates prove the
// caller-supplied context.Context reaches the provider instead of an
// internal context.Background().

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// scriptedProvider is a minimal providers.Provider that returns a fixed
// response or error and records the context.Context it was invoked with.
type scriptedProvider struct {
	resp        string
	err         error
	capturedCtx context.Context
	calls       int
}

func (s *scriptedProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	s.calls++
	s.capturedCtx = ctx
	if s.err != nil {
		return nil, s.err
	}
	return &providers.ChatResponse{
		Choices: []providers.Choice{{Message: providers.Message{Content: s.resp}}},
	}, nil
}

func (s *scriptedProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, nil
}

func (s *scriptedProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", nil
}

func (s *scriptedProvider) Name() string             { return "scripted" }
func (s *scriptedProvider) Config() providers.Config { return providers.DefaultConfig() }

// manyMessages returns n distinct, non-empty messages so a real deterministic
// join is always possible as a fallback. The content includes the message's
// index so tests can assert the fallback actually contains real conversation
// content rather than just being "non-empty".
func manyMessages(n int) []providers.Message {
	msgs := make([]providers.Message, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, providers.Message{
			Role:    providers.RoleUser,
			Content: fmt.Sprintf("message number %d with enough content to be joined", i),
		})
	}
	return msgs
}

// marker key used to prove a caller-supplied context.Context value reaches
// the provider call, rather than the function substituting its own
// context.Background().
type ctxMarkerKey struct{}

func TestCompressMessages_ManyMessages_NormalSummaryReturned(t *testing.T) {
	msgs := manyMessages(60)
	mock := &scriptedProvider{resp: "The user and assistant discussed the project roadmap and agreed on next steps."}
	c := &Compressor{Provider: mock}

	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatalf("CompressMessages returned (\"\", nil) for a normal, non-degenerate summary")
	}
	if out != mock.resp {
		t.Fatalf("expected the provider's plausible summary verbatim, got %q", out)
	}
}

func TestCompressMessages_ManyMessages_EmptyChoiceFallsThrough(t *testing.T) {
	msgs := manyMessages(60)
	mock := &scriptedProvider{resp: ""} // len(resp.Choices) > 0 but Content is empty
	c := &Compressor{Provider: mock}

	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatalf("regression of issue #74: CompressMessages returned (\"\", nil) for a choice with empty Content")
	}
	if !strings.Contains(out, "message number 59") {
		t.Fatalf("expected the deterministic newest-backwards fallback (containing the newest message), got %q", out)
	}
}

func TestCompressMessages_ManyMessages_WhitespaceOnlyChoiceFallsThrough(t *testing.T) {
	msgs := manyMessages(60)
	mock := &scriptedProvider{resp: "   \n\t  \n  "} // non-empty string, but no real content
	c := &Compressor{Provider: mock}

	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("CompressMessages returned a whitespace-only compressed context")
	}
	if !strings.Contains(out, "message number 59") {
		t.Fatalf("expected the deterministic newest-backwards fallback, got %q", out)
	}
}

func TestCompressMessages_ManyMessages_ImplausiblyShortSummaryFallsThrough(t *testing.T) {
	msgs := manyMessages(60)
	mock := &scriptedProvider{resp: "ok"} // non-empty, non-whitespace, but not a plausible summary of 60 messages
	c := &Compressor{Provider: mock}

	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatalf("CompressMessages returned (\"\", nil) for an implausibly short summary")
	}
	if out == mock.resp {
		t.Fatalf("expected the implausibly short summary %q to be rejected in favor of the deterministic fallback, got it back verbatim", mock.resp)
	}
	if !strings.Contains(out, "message number 59") {
		t.Fatalf("expected the deterministic newest-backwards fallback, got %q", out)
	}
}

func TestCompressMessages_ManyMessages_ProviderErrorFallsThrough(t *testing.T) {
	msgs := manyMessages(60)
	mock := &scriptedProvider{err: errors.New("upstream 500")}
	c := &Compressor{Provider: mock}

	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatalf("CompressMessages returned (\"\", nil) after a provider error")
	}
	if !strings.Contains(out, "message number 59") {
		t.Fatalf("expected the deterministic newest-backwards fallback, got %q", out)
	}
}

func TestCompressMessages_ManyMessages_ContextPropagates(t *testing.T) {
	msgs := manyMessages(60)
	mock := &scriptedProvider{resp: "A sufficiently long and plausible summary of the conversation history."}
	c := &Compressor{Provider: mock}

	ctx := context.WithValue(context.Background(), ctxMarkerKey{}, "issue-74")
	_, err := c.CompressMessages(ctx, "test-model", msgs, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.calls == 0 {
		t.Fatalf("expected the provider to be called")
	}
	if mock.capturedCtx == nil || mock.capturedCtx.Value(ctxMarkerKey{}) != "issue-74" {
		t.Fatalf("expected the caller-supplied context to reach Provider.Chat, got %v", mock.capturedCtx)
	}
}

func TestCompressMessages_ManyMessages_CanceledContextFallsThrough(t *testing.T) {
	msgs := manyMessages(60)
	mock := &scriptedProvider{resp: "should never be used because the context is already canceled before the call"}
	c := &Compressor{Provider: mock}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if mock.capturedCtx != nil {
		t.Fatalf("sanity check failed: provider called before CompressMessages ran")
	}
	out, err := c.CompressMessages(ctx, "test-model", msgs, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.capturedCtx == nil {
		t.Fatalf("expected the provider to still be invoked with the canceled context (propagation is the caller's responsibility to check, not CompressMessages' to skip)")
	}
	if mock.capturedCtx.Err() == nil {
		t.Fatalf("expected the provider to observe a canceled context")
	}
	if out == "" {
		t.Fatalf("expected a non-empty result even when the propagated context is canceled")
	}
}

// A provider is asked to summarize the whole conversation, not a join already
// cut down to the budget. With a 179-token budget the old code handed the
// model one message and called the answer a summary (#346).
func TestCompressMessages_ProviderSeesTheWholeConversation(t *testing.T) {
	var got string
	prov := &capturingProvider{scriptedProvider: scriptedProvider{resp: "the user and the assistant discussed twelve numbered messages"}}
	prov.onChat = func(req providers.ChatRequest) { got = req.Messages[len(req.Messages)-1].Content }
	c := &Compressor{Provider: prov}
	msgs := manyMessages(12)
	out, err := c.CompressMessages(context.Background(), "m", msgs, 10)
	if err != nil {
		t.Fatalf("CompressMessages: %v", err)
	}
	if out != prov.resp {
		t.Fatalf("expected the provider summary, got %q", out)
	}
	for i := 0; i < 12; i++ {
		if !strings.Contains(got, fmt.Sprintf("message number %d ", i)) {
			t.Errorf("message %d was not sent to the summarizer; the provider saw a budget-truncated join", i)
		}
	}
	if req := prov.lastReq; req.MaxTokens != summaryMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", req.MaxTokens, summaryMaxTokens)
	}
}

// capturingProvider records the request it was asked to summarize.
type capturingProvider struct {
	scriptedProvider
	onChat  func(providers.ChatRequest)
	lastReq providers.ChatRequest
}

func (c *capturingProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	c.lastReq = req
	if c.onChat != nil {
		c.onChat(req)
	}
	return c.scriptedProvider.Chat(ctx, req)
}
