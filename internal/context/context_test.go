package contextpkg

import (
	"context"
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
	out, err := c.CompressMessages("test-model", msgs, 1000)
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
	mock := &mockProv{resp: "SUMMARY"}
	c := &Compressor{Provider: mock}
	out, err := c.CompressMessages("test-model", msgs, 10) // tiny budget forces summarization
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "SUMMARY" {
		t.Fatalf("expected provider summary, got %q", out)
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

	info := r.Lookup("unknown-model")
	if info.ContextWindow != 8192 {
		t.Fatalf("expected medium fallback 8192, got %d", info.ContextWindow)
	}
}
