package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// An empty reply is a provider failure, not an answer. It used to become
// "I've processed your request." with a nil error — exit 0 for scripts, a 200
// for the HTTP API — so the contract here is that it reaches Process's
// in-band error path, which every non-interactive caller already translates.
func TestProcess_EmptyContentIsAnError(t *testing.T) {
	cfg := newNonStreamingConfig()
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{Choices: []providers.Choice{{
				Message: providers.Message{Role: providers.RoleAssistant, Content: ""},
			}}}, nil
		},
	}
	a := NewAgent(cfg, provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	got, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "u", Content: "hi", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ReplyError(got) == nil {
		t.Fatalf("empty content must be an in-band error, got %q", got)
	}
	if strings.Contains(got, "I've processed your request") {
		t.Errorf("the confident non-answer is back: %q", got)
	}
}

func TestProcess_NoChoicesIsAnError(t *testing.T) {
	cfg := newNonStreamingConfig()
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{}, nil
		},
	}
	a := NewAgent(cfg, provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	got, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "u", Content: "hi", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ReplyError(got) == nil {
		t.Fatalf("a choiceless response must be an in-band error, got %q", got)
	}
}
