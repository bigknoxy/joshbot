package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

type mockProvider struct {
	resp  string
	delay time.Duration
}

func (m *mockProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if m.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.delay):
		}
	}
	return &providers.ChatResponse{
		Choices: []providers.Choice{{Message: providers.Message{Content: m.resp}}},
	}, nil
}

func (m *mockProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, nil
}

func (m *mockProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", nil
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Config() providers.Config { return providers.DefaultConfig() }

func TestRunner_Run_Success(t *testing.T) {
	ctx := context.Background()
	mock := &mockProvider{resp: "hello from subagent", delay: 0}
	r := NewRunner(mock, "test-model", 256, 0.0, 2*time.Second)

	got, err := r.Run(ctx, "Say hi")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "hello from subagent" {
		t.Fatalf("unexpected response: %q", got)
	}
}

func TestRunner_Run_Timeout(t *testing.T) {
	ctx := context.Background()
	// provider will sleep longer than runner timeout
	mock := &mockProvider{resp: "late", delay: 200 * time.Millisecond}
	r := NewRunner(mock, "test-model", 256, 0.0, 50*time.Millisecond)

	_, err := r.Run(ctx, "This will timeout")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}
