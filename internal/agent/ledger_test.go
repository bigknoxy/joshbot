package agent

// Tests for the usage ledger wiring and the spend ceiling (#436). The ledger
// double records every RecordUsage into memory and answers Spend with a
// scripted rate per recorded call, so the ceiling's pre-call check can be
// exercised without a priced model or a live provider.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

type usageRecord struct {
	sessionID string
	model     string
	usage     providers.Usage
}

type fakeLedger struct {
	mu      sync.Mutex
	records []usageRecord
	usd     float64 // spend added per recorded call
}

func (l *fakeLedger) RecordUsage(sessionID, model string, u providers.Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, usageRecord{sessionID, model, u})
	l.usd += 0.60
}

func (l *fakeLedger) Spend(sessionID string) (sessionUSD, totalUSD float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.usd, l.usd
}

func (l *fakeLedger) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.records)
}

func ledgerTestAgent(t *testing.T, cfg *config.Config, prov providers.Provider, led *fakeLedger) *Agent {
	t.Helper()
	a := NewAgent(cfg, prov, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	WithUsageLedger(led)(a)
	return a
}

func ledgerTestMessage() bus.InboundMessage {
	return bus.InboundMessage{
		SenderID:  "user1",
		Content:   "hi",
		Channel:   "cli",
		Timestamp: time.Now(),
	}
}

// The ledger must be told the model the provider ANSWERED with (resp.Model),
// which under a fallback chain is not the model addressed.
func TestReactLoopRecordsUsageWithAnsweringModel(t *testing.T) {
	cfg := config.Defaults()
	const answered = "fallback-provider/some-fallback-model"
	prov := &mockProvider{
		chatFn: func(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{
				ID:    "r1",
				Model: answered,
				Choices: []providers.Choice{{
					Message: providers.Message{Role: providers.RoleAssistant, Content: "hello"},
				}},
				Usage: providers.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
			}, nil
		},
	}
	led := &fakeLedger{}
	a := ledgerTestAgent(t, cfg, prov, led)

	cfg.Agents.Defaults.Model = "addressed-provider/asked-for-model"
	response, err := a.Process(context.Background(), ledgerTestMessage())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if response == "" {
		t.Fatal("empty reply")
	}
	if led.callCount() != 1 {
		t.Fatalf("ledger records = %d, want 1", led.callCount())
	}
	got := led.records[0]
	if got.model != answered {
		t.Errorf("recorded model = %q, want the answered model %q", got.model, answered)
	}
	if got.sessionID == "" {
		t.Error("recorded session id empty")
	}
	if got.usage.TotalTokens != 110 {
		t.Errorf("recorded usage = %+v", got.usage)
	}
}

// With nil ledger usage recording and the ceiling are both off — the wiring
// must stay nil-safe the way the incident log is.
func TestNilUsageLedgerIsDisabled(t *testing.T) {
	cfg := config.Defaults()
	a := NewAgent(cfg, &mockProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	if a.usageLedger != nil {
		t.Fatal("usageLedger should default to nil")
	}
	// Nil option is a no-op.
	WithUsageLedger(nil)(a)
	if a.usageLedger != nil {
		t.Fatal("WithUsageLedger(nil) must not install anything")
	}
}

// The ceiling halts the turn before the next expensive call: recorded spend
// reaches the configured ceiling mid-loop and the LLM is never asked again.
func TestSpendCeilingHaltsBeforeNextCall(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agents.Defaults.SpendCeilingUSD = 1.00

	llmCalls := 0
	prov := &mockProvider{
		chatFn: func(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			llmCalls++
			// Every call asks for a tool, which keeps the loop running.
			return &providers.ChatResponse{
				ID:    "r1",
				Model: "test-model",
				Choices: []providers.Choice{{
					Message: providers.Message{
						Role:    providers.RoleAssistant,
						Content: "checking",
						ToolCalls: []providers.ToolCall{{
							ID:   "call_1",
							Type: "function",
							Function: providers.FunctionCall{
								Name:      "filesystem",
								Arguments: `{"operation":"read_file","path":"t.txt"}`,
							},
						}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: providers.Usage{PromptTokens: 10, CompletionTokens: 1},
			}, nil
		},
	}
	led := &fakeLedger{} // $0.60 per recorded call
	a := ledgerTestAgent(t, cfg, prov, led)

	response, err := a.Process(context.Background(), ledgerTestMessage())
	if err != nil {
		t.Fatalf("Process must return the budget reply in band, not an error: %v", err)
	}

	// Call 1 spends $0.60. The check before call 2 sees $0.60 < $1.00 and
	// continues; call 2 pushes it to $1.20. The check before call 3 must
	// refuse it — so exactly 2 LLM calls run, and the third iteration's
	// request is never built.
	if llmCalls != 2 {
		t.Errorf("LLM calls = %d, want 2 (ceiling must halt before the call that would overspend)", llmCalls)
	}
	if !strings.Contains(response, "spend ceiling") {
		t.Errorf("reply should name the ceiling, got: %s", response)
	}
	if !strings.Contains(response, "$1.00") {
		t.Errorf("reply should carry the ceiling figure, got: %s", response)
	}
	if !strings.Contains(response, "spend_ceiling_usd") {
		t.Errorf("reply should carry the config key, got: %s", response)
	}
	if !strings.Contains(response, "$1.20") {
		t.Errorf("reply should carry the recorded spend, got: %s", response)
	}
	// The in-band reply is chat text with nil error; it must NOT read as an
	// "Error processing request" turn.
	if strings.Contains(response, "Error processing request") {
		t.Errorf("ceiling stop is a configured outcome, not an error: %s", response)
	}
}

// A spent turn is still a real conversation turn: the user message (and any
// tool results the loop collected before it stopped) must be persisted.
func TestSpendCeilingStopsPersistSession(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agents.Defaults.SpendCeilingUSD = 1.00
	called := 0
	prov := &mockProvider{
		chatFn: func(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			called++
			return &providers.ChatResponse{
				ID:    "r1",
				Model: req.Model,
				Choices: []providers.Choice{{
					Message: providers.Message{Role: providers.RoleAssistant, Content: "should not happen"},
				}},
			}, nil
		},
	}
	led := &fakeLedger{usd: 5.00}
	a := ledgerTestAgent(t, cfg, prov, led)

	response, err := a.Process(context.Background(), ledgerTestMessage())
	if err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("LLM calls = %d, want 0 (spend already over the ceiling)", called)
	}
	if !strings.Contains(response, "spend ceiling") {
		t.Errorf("unexpected reply: %s", response)
	}
	if sm := a.sessions.(*mockSessionManager); sm.saves() == 0 {
		t.Error("session should have been created and saved despite the ceiling stop")
	}
}

// With no ceiling configured, spend never stops a turn.
func TestNoCeilingKeepsRunning(t *testing.T) {
	cfg := config.Defaults()
	prov := &mockProvider{
		chatFn: func(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{
				ID:    "r1",
				Model: req.Model,
				Choices: []providers.Choice{{
					Message: providers.Message{Role: providers.RoleAssistant, Content: "done"},
				}},
				Usage: providers.Usage{PromptTokens: 5, CompletionTokens: 1},
			}, nil
		},
	}
	led := &fakeLedger{usd: 9999.00}
	a := ledgerTestAgent(t, cfg, prov, led)
	if _, err := a.Process(context.Background(), ledgerTestMessage()); err != nil {
		t.Fatalf("unbounded spend must not error: %v", err)
	}
}

// spendCeilingReply is message-building only — pin its content contract
// directly.
func TestSpendCeilingReplyContent(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agents.Defaults.SpendCeilingUSD = 3.00
	led := &fakeLedger{usd: 3.40}
	a := ledgerTestAgent(t, cfg, &mockProvider{}, led)
	reply := a.spendCeilingReply(&session.Session{ID: "cli:x"})
	for _, want := range []string{"$3.40", "$3.00", "spend_ceiling_usd", "pricing table", "this session"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply missing %q:\n%s", want, reply)
		}
	}
}

// /status surfaces the ledger figures when one is wired.
func TestStatusShowsSpendWhenLedgerWired(t *testing.T) {
	cfg := config.Defaults()
	led := &fakeLedger{usd: 1.23}
	a := ledgerTestAgent(t, cfg, &mockProvider{}, led)

	msg := bus.InboundMessage{SenderID: "user1", Content: "/status", Channel: "cli", Timestamp: time.Now()}
	response, err := a.Process(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response, "Spend (this session): $1.23") ||
		!strings.Contains(response, "Spend (lifetime): $1.23") {
		t.Errorf("status should carry both spend figures, got:\n%s", response)
	}
	// The caveat must ride the line so $0.00 does not read as a free run.
	if !strings.Contains(response, "priced models only") {
		t.Errorf("status spend must say it is priced-models-only, got:\n%s", response)
	}

	// Without a ledger the lines are absent, not zero.
	a2 := ledgerTestAgent(t, cfg, &mockProvider{}, nil)
	msg2 := bus.InboundMessage{SenderID: "user1", Content: "/status", Channel: "cli", Timestamp: time.Now()}
	response2, err := a2.Process(context.Background(), msg2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response2, "Spend (") {
		t.Errorf("no ledger wired: spend lines should be absent entirely, got:\n%s", response2)
	}
}
