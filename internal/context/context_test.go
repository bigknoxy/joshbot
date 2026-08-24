package contextpkg

import (
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

type mockProv struct {
	resp string
}

func (m *mockProv) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{Choices: []providers.Choice{{Message: providers.Message{Content: m.resp}}}}, nil
}
func (m *mockProv) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, nil
}
func (m *mockProv) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", nil
}
func (m *mockProv) Name() string             { return "mock" }
func (m *mockProv) Config() providers.Config { return providers.DefaultConfig() }

func TestCompressMessages_NoProvider_UnderBudget(t *testing.T) {
	msgs := []providers.Message{
		{Role: providers.RoleUser, Content: "hello"},
		{Role: providers.RoleAssistant, Content: "world"},
	}
	c := &Compressor{Provider: nil}
	// generous budget
	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty output")
	}
}

func TestCompressMessages_WithProvider_ExceedsBudget(t *testing.T) {
	msgs := []providers.Message{}
	// create many messages to exceed small budget
	for i := 0; i < 20; i++ {
		msgs = append(msgs, providers.Message{Role: providers.RoleUser, Content: "this is a longer message to consume tokens"})
	}
	mock := &mockProv{resp: "SUMMARY: the user sent many long messages"}
	c := &Compressor{Provider: mock}
	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 10) // tiny budget forces summarization
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "SUMMARY: the user sent many long messages" {
		t.Fatalf("expected provider summary, got %q", out)
	}
}

func TestCompressMessages_SingleMessageExceedsBudget(t *testing.T) {
	msgs := []providers.Message{
		{Role: providers.RoleTool, Content: strings.Repeat("x", 2000)},
	}
	c := &Compressor{Provider: nil}
	// tiny budget that the single message exceeds
	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty output even when message exceeds budget, got empty string")
	}
	// Must return at least part of the content, not empty
	if !strings.Contains(out, "x") {
		t.Fatalf("expected output to contain message content, got empty/truncated content")
	}
}

func TestCompressMessages_ProviderReturnsEmpty(t *testing.T) {
	msgs := []providers.Message{
		{Role: providers.RoleUser, Content: strings.Repeat("a", 500)},
	}
	mock := &mockProv{resp: ""}
	c := &Compressor{Provider: mock}
	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty fallback when provider returns empty, got empty string")
	}
}

func TestCompressMessages_AllEmptyContent(t *testing.T) {
	msgs := []providers.Message{
		{Role: providers.RoleUser, Content: ""},
		{Role: providers.RoleAssistant, Content: ""},
	}
	c := &Compressor{Provider: nil}
	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 100)
	if err == nil {
		t.Fatalf("expected error for all-empty messages, got nil, out=%q", out)
	}
}

func TestCompressMessages_ToolResultTriggersCompaction(t *testing.T) {
	msgs := []providers.Message{
		{Role: providers.RoleUser, Content: "did the royals win"},
		{Role: providers.RoleAssistant, Content: `[{"name": "web_search", "arguments": "{\"query\": \"Kansas City Royals score\"}"}]`},
		{Role: providers.RoleTool, Content: strings.Repeat("The Royals played a game and here is a very long tool result with details ", 80)},
		{Role: providers.RoleAssistant, Content: "The Royals won 5-3."},
	}
	c := &Compressor{Provider: nil}
	out, err := c.CompressMessages(context.Background(), "test-model", msgs, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty output as fallback even with tiny budget, got empty string")
	}
}

func TestRegistryLookup_Override(t *testing.T) {
	r := NewRegistry()
	r.SetOverride("custom/small-model", 16384)

	info := r.Lookup("custom/small-model")
	if info.ContextWindow != 16384 {
		t.Fatalf("expected override context window 16384, got %d", info.ContextWindow)
	}
}

func TestRegistryLookup_DefaultHeuristic(t *testing.T) {
	r := NewRegistry()

	// An unrecognised model gets a modern window, not the 4096 "small" class:
	// with max_tokens 8192 the small default drove the budget to 256 tokens
	// and compaction wiped the conversation on every tool call (#346).
	info := r.Lookup("unknown-model")
	if info.ContextWindow != DefaultContextWindow {
		t.Fatalf("expected DefaultContextWindow %d for an unknown model, got %d", DefaultContextWindow, info.ContextWindow)
	}
	for _, m := range []string{"poolside/laguna-s-2.1", "deepseek-ai/deepseek-v4-flash-0731"} {
		if got := r.Lookup(m).ContextWindow; got != DefaultContextWindow {
			t.Errorf("Lookup(%q) = %d, want %d", m, got, DefaultContextWindow)
		}
	}
	if got := r.Lookup("small-model").ContextWindow; got != 4096 {
		t.Errorf("a model that calls itself small keeps the 4096 class, got %d", got)
	}
}

func TestRegistryLookup_NvidiaModel(t *testing.T) {
	r := NewRegistry()

	info := r.Lookup("nvidia/stepfun-ai/step-3.5-flash")
	if info.ContextWindow != 131072 {
		t.Fatalf("expected 131072 for nvidia/stepfun-ai/step-3.5-flash, got %d", info.ContextWindow)
	}
}
