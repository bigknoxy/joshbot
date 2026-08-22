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
	// script, when set, is answered in order ahead of reply — one entry per
	// LLM call — so a test can stage a tool-call iteration before the answer.
	script []providers.Message
	calls  int
}

func (h *hostedModelProvider) answer(model string) (*providers.ChatResponse, error) {
	if h.openErr != nil {
		return nil, h.openErr
	}
	if !h.hosts[model] {
		return nil, fmt.Errorf(`API error (404): {"error":"please check the model you provided"}`)
	}
	if h.calls < len(h.script) {
		msg := h.script[h.calls]
		h.calls++
		msg.Role = providers.RoleAssistant
		return &providers.ChatResponse{Model: model, Choices: []providers.Choice{{Message: msg}}}, nil
	}
	h.calls++
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
	ch := make(chan providers.StreamChunk, 2)
	msg := resp.Choices[0].Message
	for i := range msg.ToolCalls {
		msg.ToolCalls[i].Index = i
	}
	ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{
		Delta: providers.Message{Role: providers.RoleAssistant, Content: msg.Content, ToolCalls: msg.ToolCalls},
	}}}
	finish := "stop"
	if len(msg.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{FinishReason: finish}}}
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

// The user is told when a fallback answered: the reply opens with a one-line
// notice naming the failed provider, the reason, and who answered.
func TestProcess_FallbackNoticePrependedToReply(t *testing.T) {
	openrouter := &hostedModelProvider{
		name:    "openrouter",
		hosts:   map[string]bool{"z-ai/glm-5.2": true},
		openErr: fmt.Errorf("API error (429): rate limit exceeded"),
	}
	poolside := &hostedModelProvider{
		name:  "poolside",
		hosts: map[string]bool{"poolside/laguna-s-2.1": true},
		reply: "Answer from the backup.",
	}

	mp := providers.NewMultiProvider(providers.MultiProviderConfig{DefaultProvider: "openrouter"})
	mp.Register("openrouter", openrouter, "z-ai/glm-5.2", 0, true)
	mp.Register("poolside", poolside, "poolside/laguna-s-2.1", 1, true)

	cfg := config.Defaults()
	cfg.Agents.Defaults.Model = "z-ai/glm-5.2"

	a := NewAgent(cfg, mp, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	got, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "josh", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process() failed: %v", err)
	}
	for _, want := range []string{"openrouter unavailable", "rate_limit", "answered by poolside", "Answer from the backup."} {
		if !strings.Contains(got, want) {
			t.Errorf("reply missing %q: %q", want, got)
		}
	}
	if !strings.HasPrefix(got, "⚠️ openrouter unavailable") {
		t.Errorf("notice should open the reply, got %q", got)
	}
}

// quiet_fallback suppresses the notice, nothing else.
func TestProcess_QuietFallbackSuppressesNotice(t *testing.T) {
	openrouter := &hostedModelProvider{
		name:    "openrouter",
		hosts:   map[string]bool{"z-ai/glm-5.2": true},
		openErr: fmt.Errorf("API error (429): rate limit exceeded"),
	}
	poolside := &hostedModelProvider{
		name:  "poolside",
		hosts: map[string]bool{"poolside/laguna-s-2.1": true},
		reply: "Answer from the backup.",
	}

	mp := providers.NewMultiProvider(providers.MultiProviderConfig{DefaultProvider: "openrouter"})
	mp.Register("openrouter", openrouter, "z-ai/glm-5.2", 0, true)
	mp.Register("poolside", poolside, "poolside/laguna-s-2.1", 1, true)

	cfg := config.Defaults()
	cfg.Agents.Defaults.Model = "z-ai/glm-5.2"
	cfg.Agents.Defaults.QuietFallback = true

	a := NewAgent(cfg, mp, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	got, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "josh", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process() failed: %v", err)
	}
	if strings.Contains(got, "unavailable") {
		t.Errorf("quiet_fallback should suppress the notice: %q", got)
	}
	if !strings.Contains(got, "Answer from the backup.") {
		t.Errorf("reply lost: %q", got)
	}
}

// Streaming: the notice is delivered through the sink before the content, and
// the returned reply text matches what the sink saw.
func TestProcess_FallbackNoticeStreamsBeforeContent(t *testing.T) {
	openrouter := &hostedModelProvider{
		name:    "openrouter",
		hosts:   map[string]bool{"z-ai/glm-5.2": true},
		openErr: fmt.Errorf("API error (503): down"),
	}
	poolside := &hostedModelProvider{
		name:  "poolside",
		hosts: map[string]bool{"poolside/laguna-s-2.1": true},
		reply: "Streamed answer.",
	}

	mp := providers.NewMultiProvider(providers.MultiProviderConfig{DefaultProvider: "openrouter"})
	mp.Register("openrouter", openrouter, "z-ai/glm-5.2", 0, true)
	mp.Register("poolside", poolside, "poolside/laguna-s-2.1", 1, true)

	cfg := config.Defaults()
	cfg.Agents.Defaults.Model = "z-ai/glm-5.2"
	cfg.Agents.Defaults.Streaming = true

	a := NewAgent(cfg, mp, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	var streamed strings.Builder
	ctx := WithStreamSink(context.Background(), func(ev StreamEvent) { streamed.WriteString(ev.Delta) })

	got, err := a.Process(ctx, bus.InboundMessage{
		SenderID: "josh", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process() failed: %v", err)
	}
	if !strings.HasPrefix(streamed.String(), "⚠️ openrouter unavailable") {
		t.Errorf("sink should see the notice first, got %q", streamed.String())
	}
	if got != streamed.String() {
		t.Errorf("reply text %q diverges from streamed text %q", got, streamed.String())
	}
}

// A fallback turn that calls a tool makes two LLM calls, and the notice used
// to be captured per call: it reached the chat twice at the top of a reply
// and a third time glued onto the narration before the tool call
// ("...traveler.⚠️ nvidia unavailable..."), while the stored tool-call message
// carried the warning as the model's own words for it to echo on the next
// turn. The notice is once per turn, the narration and the answer are
// separated by a paragraph break, and the stored tool-call content is clean.
func TestProcess_FallbackNoticeOncePerTurnAcrossToolCalls(t *testing.T) {
	openrouter := &hostedModelProvider{
		name:    "openrouter",
		hosts:   map[string]bool{"z-ai/glm-5.2": true},
		openErr: fmt.Errorf("API error (503): down"),
	}
	poolside := &hostedModelProvider{
		name:  "poolside",
		hosts: map[string]bool{"poolside/laguna-s-2.1": true},
		script: []providers.Message{{
			Content: "Let me check that.",
			ToolCalls: []providers.ToolCall{{ID: "call_1", Type: "function",
				Function: providers.FunctionCall{Name: "lookup", Arguments: `{}`}}},
		}},
		reply: "Sunny and 80°F.",
	}

	mp := providers.NewMultiProvider(providers.MultiProviderConfig{DefaultProvider: "openrouter"})
	mp.Register("openrouter", openrouter, "z-ai/glm-5.2", 0, true)
	mp.Register("poolside", poolside, "poolside/laguna-s-2.1", 1, true)

	for _, streaming := range []bool{true, false} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			poolside.calls = 0
			cfg := config.Defaults()
			cfg.Agents.Defaults.Model = "z-ai/glm-5.2"
			cfg.Agents.Defaults.Streaming = streaming

			sessions := newMockSessionManager()
			a := NewAgent(cfg, mp, &mockToolExecutor{}, sessions, newMockLogger())

			var streamed strings.Builder
			ctx := context.Background()
			if streaming {
				ctx = WithStreamSink(ctx, func(ev StreamEvent) { streamed.WriteString(ev.Delta) })
			}
			got, err := a.Process(ctx, bus.InboundMessage{
				SenderID: "josh", Content: "weather?", Channel: "cli", Timestamp: time.Now(),
			})
			if err != nil {
				t.Fatalf("Process() failed: %v", err)
			}
			if n := strings.Count(got, "⚠️ openrouter unavailable"); n != 1 {
				t.Errorf("reply carries the notice %d times, want 1: %q", n, got)
			}
			if !strings.HasPrefix(got, "⚠️ openrouter unavailable") || !strings.HasSuffix(got, "Sunny and 80°F.") {
				t.Errorf("reply should be notice + answer: %q", got)
			}
			if streaming {
				s := streamed.String()
				if n := strings.Count(s, "⚠️ openrouter unavailable"); n != 1 {
					t.Errorf("sink saw the notice %d times, want 1: %q", n, s)
				}
				if !strings.HasPrefix(s, "⚠️ openrouter unavailable") {
					t.Errorf("sink should see the notice first: %q", s)
				}
				if !strings.Contains(s, "Let me check that.\n\nSunny") {
					t.Errorf("narration and answer should be separated by a paragraph break: %q", s)
				}
			}
			sess := sessions.sessions["cli:josh"]
			for _, m := range sess.Messages {
				if len(m.ToolCalls) > 0 && strings.Contains(m.Content, "unavailable") {
					t.Errorf("stored tool-call message carries the notice as the model's words: %q", m.Content)
				}
			}
		})
	}
}
