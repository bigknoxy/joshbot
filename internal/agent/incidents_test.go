package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/incidents"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// newIncidentTestLog returns a Log backed by a fresh temp file. Incident
// recording is optional wiring (a nil log is a no-op), so every test here
// wires one explicitly and asserts on what the failure paths persisted.
func newIncidentTestLog(t *testing.T) *incidents.Log {
	t.Helper()
	log, err := incidents.NewLog(t.TempDir() + "/" + incidents.FileName)
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	return log
}

// TestIncident_TurnTimeoutRecorded pins the record point on the
// DeadlineExceeded path in process: a turn the agent's own budget killed
// must leave a turn_timeout incident behind with the loop's iteration and
// tool-call counts, or the operator's only evidence of "took too long" is
// the user's screenshot.
func TestIncident_TurnTimeoutRecorded(t *testing.T) {
	incLog := newIncidentTestLog(t)

	entered := make(chan struct{})
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			close(entered)
			<-ctx.Done() // never answers; only the agent's deadline ends this
			return nil, ctx.Err()
		},
	}

	a := NewAgent(config.Defaults(), provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger(),
		WithTimeout(50*time.Millisecond),
		WithIncidentLog(incLog))

	done := make(chan string, 1)
	go func() {
		resp, _ := a.Process(context.Background(), bus.InboundMessage{
			SenderID: "user123", Content: "hello", Channel: "cli", Timestamp: time.Now(),
		})
		done <- resp
	}()

	select {
	case resp := <-done:
		if !strings.Contains(strings.ToLower(resp), "took too long") {
			t.Fatalf("timed-out turn answered %q, want the timeout reply", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Process did not return within 5s despite a 50ms timeout")
	}

	recent := incLog.Recent(10, incidents.TurnTimeout)
	if len(recent) != 1 {
		t.Fatalf("got %d turn_timeout incidents, want exactly 1", len(recent))
	}
	inc := recent[0]
	if inc.Iterations < 1 {
		t.Errorf("recorded iterations = %d, want >= 1 (the loop entered once)", inc.Iterations)
	}
	if inc.ToolCalls != 0 {
		t.Errorf("recorded toolCalls = %d, want 0 (the provider never answered)", inc.ToolCalls)
	}
	if inc.Model == "" {
		t.Error("recorded incident has no model")
	}
	if inc.Channel != "cli" {
		t.Errorf("recorded channel = %q, want cli", inc.Channel)
	}
	if inc.Session == "" {
		t.Error("recorded incident has no session id")
	}
	if time.Duration(inc.ElapsedNanos) <= 0 {
		t.Errorf("recorded elapsed = %v, want > 0", time.Duration(inc.ElapsedNanos))
	}
}

// TestIncident_LLMFailureRecorded pins the record point on the generic
// in-band error path: an LLM call that fails with a live context (401,
// network, 5xx) is a normal error to the user and an incident for the
// operator.
func TestIncident_LLMFailureRecorded(t *testing.T) {
	incLog := newIncidentTestLog(t)

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return nil, context.DeadlineExceeded
		},
	}

	a := NewAgent(config.Defaults(), provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger(),
		WithIncidentLog(incLog))

	response, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "user123", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// LLM failures are reported in band as reply text with a nil error.
	if !strings.Contains(response, "Error processing request") {
		t.Fatalf("response = %q, want the in-band error reply", response)
	}

	recent := incLog.Recent(10, incidents.LLMFailure)
	if len(recent) != 1 {
		t.Fatalf("got %d llm_failure incidents, want exactly 1", len(recent))
	}
	if recent[0].Model == "" {
		t.Error("recorded incident has no model")
	}
	if recent[0].Session == "" {
		t.Error("recorded incident has no session id")
	}
}

// TestIncident_StreamDiedMidTextRecorded pins the record points inside
// streamChat: a stream that dies mid-text (deltas were already shown) is
// both a partial answer with a visible marker and a stream_died incident.
func TestIncident_StreamDiedMidTextRecorded(t *testing.T) {
	incLog := newIncidentTestLog(t)

	provider := &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			// One text delta, then the stream closes without a finish
			// reason: the accumulator rejects it as truncated, mid-text.
			ch <- streamChunk(streamTextDelta(0, "Partial "))
			close(ch)
			return ch, nil
		},
	}

	a := NewAgent(newStreamingConfig(), provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger(),
		WithIncidentLog(incLog))

	response, err := a.Process(WithStreamSink(context.Background(), func(StreamEvent) {}), bus.InboundMessage{
		SenderID: "user123", Content: "Hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !strings.Contains(response, "stream error") {
		t.Fatalf("response = %q, want the mid-stream marker inside the partial", response)
	}

	recent := incLog.Recent(10, incidents.StreamDied)
	if len(recent) != 1 {
		t.Fatalf("got %d stream_died incidents, want exactly 1", len(recent))
	}
	if !strings.Contains(recent[0].Detail, "stream ended without finish reason") {
		t.Errorf("recorded detail = %q, want the accumulator's truncation error", recent[0].Detail)
	}
	if recent[0].Model == "" {
		t.Error("recorded incident has no model")
	}
}

// TestIncident_NilLogIsNoOp closes the guard: incident wiring is optional,
// and a nil log must neither panic nor fail the turn.
func TestIncident_NilLogIsNoOp(t *testing.T) {
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return nil, context.DeadlineExceeded
		},
	}

	a := NewAgent(config.Defaults(), provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	response, err := a.Process(context.Background(), bus.InboundMessage{
		SenderID: "user123", Content: "hello", Channel: "cli", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !strings.Contains(response, "Error processing request") {
		t.Fatalf("response = %q, want the in-band error reply", response)
	}
}

// TestIncident_CountsSurviveReload pins that the counters the timeout path
// reads off the in-memory log round-trip through the JSONL file — the
// next-restart self-heal bump is computed from the file, not from memory.
func TestIncident_CountsSurviveReload(t *testing.T) {
	path := t.TempDir() + "/" + incidents.FileName
	incLog, err := incidents.NewLog(path)
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}

	provider := &mockProvider{
		streamFn: func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 1)
			ch <- streamChunk(streamTextDelta(0, "Partial"))
			close(ch)
			return ch, nil
		},
	}

	a := NewAgent(newStreamingConfig(), provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger(),
		WithIncidentLog(incLog))
	if _, err := a.Process(WithStreamSink(context.Background(), func(StreamEvent) {}), bus.InboundMessage{
		SenderID: "user123", Content: "Hello", Channel: "cli", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("Process: %v", err)
	}

	reloaded, err := incidents.NewLog(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.CountSince(time.Minute, incidents.StreamDied); got != 1 {
		t.Fatalf("reloaded CountSince = %d, want 1 (the incident survived the restart)", got)
	}
}
