package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// hostedModelProvider answers only for the models it hosts, the way a real API
// does: anything else is the provider's own 404.
type hostedModelProvider struct {
	name    string
	hosts   map[string]bool
	reply   string
	openErr error
}

func (h *hostedModelProvider) answer(model string) (*providers.ChatResponse, error) {
	if h.openErr != nil {
		return nil, h.openErr
	}
	if !h.hosts[model] {
		return nil, fmt.Errorf(`API error (404): {"error":"please check the model you provided"}`)
	}
	return &providers.ChatResponse{
		Model: model,
		Choices: []providers.Choice{{
			Message: providers.Message{Role: providers.RoleAssistant, Content: h.reply},
		}},
	}, nil
}

func (h *hostedModelProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	return h.answer(req.Model)
}

func (h *hostedModelProvider) ChatStream(_ context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	resp, err := h.answer(req.Model)
	if err != nil {
		return nil, err
	}
	ch := make(chan providers.StreamChunk, 1)
	ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{
		Delta: providers.Message{Role: providers.RoleAssistant, Content: resp.Choices[0].Message.Content},
	}}}
	close(ch)
	return ch, nil
}

func (h *hostedModelProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", nil
}
func (h *hostedModelProvider) Name() string { return h.name }
func (h *hostedModelProvider) Config() providers.Config {
	return providers.Config{Model: h.name + "-default"}
}

// End-to-end replication of the reported failure: an ordinary turn on
// openrouter/z-ai/glm-5.2 dies mid-conversation with
// `404 {"error":"please check the model you provided"}` — poolside's wording,
// for a model the user never pointed at poolside. The primary was merely rate
// limited; the chain then handed poolside openrouter's model ID and reported
// the resulting 404 as the whole failure.
func TestProcess_FallbackDoesNotLeakPrimaryModel(t *testing.T) {
	openrouter := &hostedModelProvider{
		name:    "openrouter",
		hosts:   map[string]bool{"z-ai/glm-5.2": true},
		openErr: fmt.Errorf("API error (429): rate limit exceeded"),
	}
	poolside := &hostedModelProvider{
		name:  "poolside",
		hosts: map[string]bool{"poolside/laguna-s-2.1": true},
		reply: "Well hello yourself, Josh.",
	}

	mp := providers.NewMultiProvider(providers.MultiProviderConfig{DefaultProvider: "openrouter"})
	mp.Register("openrouter", openrouter, "z-ai/glm-5.2", 0, true)
	mp.Register("poolside", poolside, "poolside/laguna-s-2.1", 1, true)

	cfg := config.Defaults()
	cfg.Agents.Defaults.Model = "z-ai/glm-5.2"

	a := NewAgent(cfg, mp, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	msg := bus.InboundMessage{
		SenderID:  "josh",
		Content:   "Well hello there",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	got, err := a.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("Process() failed: %v", err)
	}
	if strings.Contains(got, "please check the model you provided") {
		t.Errorf("user saw the fallback provider's model 404: %q", got)
	}
	if !strings.Contains(got, "Well hello yourself") {
		t.Errorf("Process() = %q, want the fallback provider's answer", got)
	}
}
