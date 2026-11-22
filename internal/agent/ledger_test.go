package agent

// Tests for the usage ledger wiring and the spend ceiling (#436, #457). The
// ledger double records every call into memory and models the reservation
// the real ledger holds: a call is admitted only when its bound fits in
// what is left of the ceiling, and the bound is released when the call is
// recorded.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/incidents"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/usage"
)

type usageRecord struct {
	sessionID string
	model     string
	usage     providers.Usage
}

// fakeCallUSD is what the fake charges one recorded call.
const fakeCallUSD = 0.60

type fakeLedger struct {
	mu           sync.Mutex
	records      []usageRecord
	usd          float64            // recorded spend
	bound        float64            // what one call holds against the ceiling
	held         float64            // what the calls in flight hold
	reservations map[string]float64 // reservation id -> what it holds
	next         int
}

func (l *fakeLedger) RecordUsage(sessionID, model string, u providers.Usage) {
	l.record("", sessionID, model, u)
}

func (l *fakeLedger) Reserve(model string, ceilingUSD float64, promptTokens, maxCompletionTokens int) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	bound := l.bound
	if bound == 0 {
		bound = fakeCallUSD
	}
	if ceilingUSD > 0 {
		held := l.usd + l.held
		remaining := ceilingUSD - held
		if remaining < 0 {
			remaining = 0
		}
		if remaining <= 0 || held+bound > ceilingUSD {
			return "", &fakeCeilingError{remaining: remaining, bound: bound}
		}
	}
	l.next++
	id := fmt.Sprintf("r%d", l.next)
	if l.reservations == nil {
		l.reservations = map[string]float64{}
	}
	l.reservations[id] = bound
	l.held += bound
	return id, nil
}

func (l *fakeLedger) Commit(reservationID, sessionID, model string, u providers.Usage) {
	l.record(reservationID, sessionID, model, u)
}

func (l *fakeLedger) Release(reservationID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if held, ok := l.reservations[reservationID]; ok {
		delete(l.reservations, reservationID)
		l.held -= held
	}
}

func (l *fakeLedger) record(reservationID, sessionID, model string, u providers.Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if held, ok := l.reservations[reservationID]; ok {
		delete(l.reservations, reservationID)
		l.held -= held
	}
	l.records = append(l.records, usageRecord{sessionID, model, u})
	l.usd += fakeCallUSD
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

// fakeCeilingError carries the figures the real ledger's refusal carries.
type fakeCeilingError struct {
	remaining float64
	bound     float64
}

func (e *fakeCeilingError) Error() string {
	return fmt.Sprintf("fake: spend ceiling reached: $%.2f left, call could cost $%.2f", e.remaining, e.bound)
}

func (e *fakeCeilingError) Remaining() float64 { return e.remaining }
func (e *fakeCeilingError) Bound() float64     { return e.bound }

func ledgerTestAgent(t *testing.T, cfg *config.Config, prov providers.Provider, led UsageLedger) *Agent {
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

// The ceiling halts the turn before the next expensive call: the recorded
// spend plus the bound the call in flight holds must fit in the ceiling, so
// the LLM is never asked again once the remaining budget cannot cover it.
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
	led := &fakeLedger{} // $0.60 per recorded call, and $0.60 held per call in flight
	a := ledgerTestAgent(t, cfg, prov, led)

	response, err := a.Process(context.Background(), ledgerTestMessage())
	if err != nil {
		t.Fatalf("Process must return the budget reply in band, not an error: %v", err)
	}

	// Call 1 spends $0.60. The check before call 2 sees $0.60 recorded and
	// $0.60 of budget held, so the next call cannot be paid for — exactly
	// one LLM call runs, and the second iteration's request is never sent.
	if llmCalls != 1 {
		t.Errorf("LLM calls = %d, want 1 (ceiling must halt before the call that would overspend)", llmCalls)
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
	if !strings.Contains(response, "$0.60") {
		t.Errorf("reply should carry the recorded spend, got: %s", response)
	}
	// The reply must say why the turn stopped below the ceiling: the
	// remaining budget against what the next call could have cost.
	if !strings.Contains(response, "$0.40 of the ceiling was left") {
		t.Errorf("reply should carry what was left of the ceiling, got: %s", response)
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

// A burst of parallel turns must not spend past the ceiling (#457). The
// ceiling is enforced here against the REAL ledger, not a double: the
// atomicity lives in the ledger, and a double would only prove the double.
//
// The provider holds every call at a gate until the whole burst has settled
// — every turn has either reached the provider or been refused by the
// ceiling. Without the gate the test would depend on how the turns happened
// to interleave, and the pre-fix code — where every turn checked the ceiling
// before any of them recorded — would sometimes pass by luck. With the gate,
// the whole burst has passed the check before the first call returns, which
// is exactly the window the fix closes.
func TestSpendCeilingHoldsUnderConcurrentTurns(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agents.Defaults.Model = "claude-haiku-4"
	cfg.Agents.Defaults.SpendCeilingUSD = 1.00

	const turns = 200
	var seenMu sync.Mutex
	seen := map[string]bool{}
	gate := make(chan struct{})
	var releaseOnce sync.Once
	markSeen := func(marker string) {
		seenMu.Lock()
		seen[marker] = true
		full := len(seen) >= turns
		seenMu.Unlock()
		if full {
			releaseOnce.Do(func() { close(gate) })
		}
	}

	prov := &mockProvider{
		chatFn: func(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			if marker := turnMarker(req); marker != "" {
				markSeen(marker)
			}
			select {
			case <-gate:
			case <-time.After(30 * time.Second):
			}
			return &providers.ChatResponse{
				ID:    "r1",
				Model: req.Model,
				Choices: []providers.Choice{{
					Message: providers.Message{Role: providers.RoleAssistant, Content: "done"},
				}},
				// $0.006 a call on claude-haiku-4: 1000 input tokens at
				// $1/M and 1000 output tokens at $5/M.
				Usage: providers.Usage{PromptTokens: 1000, CompletionTokens: 1000},
			}, nil
		},
	}
	led, err := usage.NewLedger(filepath.Join(t.TempDir(), usage.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a := ledgerTestAgent(t, cfg, prov, led)

	var wg sync.WaitGroup
	for i := 0; i < turns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := fmt.Sprintf("turn-%d", i)
			msg := bus.InboundMessage{
				SenderID:  fmt.Sprintf("user%d", i),
				Content:   marker,
				Channel:   "cli",
				Timestamp: time.Now(),
			}
			reply, err := a.Process(context.Background(), msg)
			if err != nil {
				t.Errorf("Process: %v", err)
				markSeen(marker)
				return
			}
			// A turn the ceiling refused never reached the provider, so it
			// marks itself here.
			if strings.Contains(reply, "Stopped before overspending") {
				markSeen(marker)
			}
		}(i)
	}
	wg.Wait()

	_, total := led.Spend("")
	if total > cfg.Agents.Defaults.SpendCeilingUSD {
		t.Errorf("recorded spend = $%.4f, want at most the $%.2f ceiling: a burst of parallel turns must not spend past the limit",
			total, cfg.Agents.Defaults.SpendCeilingUSD)
	}
}

// turnMarker finds the per-turn marker the concurrency test puts in the
// user's message.
func turnMarker(req providers.ChatRequest) string {
	for _, m := range req.Messages {
		if strings.HasPrefix(m.Content, "turn-") {
			return m.Content
		}
	}
	return ""
}

// A turn the ceiling stopped must say so where the user can see it, and
// leave a record: an automated turn (a cron job, a heartbeat) has nobody to
// read the reply, so without the incident a scheduled job would stop
// quietly and the operator would only notice a job that no longer runs
// (#457).
func TestSpendCeilingStopIsRecordedAsAnIncident(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agents.Defaults.SpendCeilingUSD = 1.00

	incLog, err := incidents.NewLog(filepath.Join(t.TempDir(), incidents.FileName))
	if err != nil {
		t.Fatal(err)
	}
	led := &fakeLedger{usd: 5.00}
	a := ledgerTestAgent(t, cfg, &mockProvider{}, led)
	WithIncidentLog(incLog)(a)

	response, err := a.Process(context.Background(), ledgerTestMessage())
	if err != nil {
		t.Fatal(err)
	}
	// The user sees what happened, what the limit was, and what to change.
	for _, want := range []string{"$5.00", "$1.00", "spend_ceiling_usd"} {
		if !strings.Contains(response, want) {
			t.Errorf("reply missing %q:\n%s", want, response)
		}
	}

	recent := incLog.Recent(10, incidents.SpendCeiling)
	if len(recent) != 1 {
		t.Fatalf("spend_ceiling incidents = %d, want 1", len(recent))
	}
	if recent[0].Session == "" {
		t.Error("incident carries no session")
	}
	if recent[0].Detail == "" {
		t.Error("incident carries no detail")
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
	reply := a.spendCeilingReply(&session.Session{ID: "cli:x"}, nil)
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
