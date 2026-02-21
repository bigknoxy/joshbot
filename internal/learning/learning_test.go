package learning

import (
	"context"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/providers"
)

type mockProvider struct{}

func (m *mockProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{Choices: []providers.Choice{{Message: providers.Message{Content: "fact1\nfact2"}}}}, nil
}
func (m *mockProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, nil
}
func (m *mockProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", nil
}
func (m *mockProvider) Name() string             { return "mock" }
func (m *mockProvider) Config() providers.Config { return providers.DefaultConfig() }

func TestConsolidator_RunOnce(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	mem, err := memory.New(tmp)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Initialize(ctx); err != nil {
		t.Fatalf("mem.Init: %v", err)
	}

	// append some history lines
	_ = mem.AppendHistory(ctx, "Line one")
	_ = mem.AppendHistory(ctx, "Line two")

	c := NewConsolidator(mem, &mockProvider{}, 1*time.Hour)
	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce error: %v", err)
	}

	got, err := mem.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory: %v", err)
	}
	if got == "" {
		t.Fatalf("expected memory to have consolidated facts")
	}
}
