package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// TestWithTimeoutIgnoresZero is what lets cmd/joshbot pass the config value
// through unconditionally. agents.defaults.timeout is a new key with omitempty,
// so every config in the wild decodes to zero, and a zero that overwrote the
// default would drop every turn's budget to nothing (#241). The call site has
// no special case; this option is where the rule lives.
func TestWithTimeoutIgnoresZero(t *testing.T) {
	base := NewAgent(config.Defaults(), &mockProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	if base.timeout != DefaultTimeout {
		t.Fatalf("unconfigured agent has timeout %v, want DefaultTimeout %v", base.timeout, DefaultTimeout)
	}

	for name, given := range map[string]time.Duration{
		"zero":     0,
		"negative": -time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			a := NewAgent(config.Defaults(), &mockProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger(),
				WithTimeout(given))
			if a.timeout != DefaultTimeout {
				t.Fatalf("WithTimeout(%v) set timeout to %v, want DefaultTimeout %v", given, a.timeout, DefaultTimeout)
			}
		})
	}
}

// TestConfiguredTimeoutBoundsTheTurn closes the loop: the field is not just
// stored, it is the deadline Process actually runs under. A provider that never
// answers must be cut off by the configured value and reported as a timeout, not
// left to hang — that is the deployment #241 was filed for, a cold local model
// that outruns the 120s default with no knob to raise it.
func TestConfiguredTimeoutBoundsTheTurn(t *testing.T) {
	entered := make(chan struct{})
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			close(entered)
			<-ctx.Done() // never answers; only the agent's deadline ends this
			return nil, ctx.Err()
		},
	}

	a := NewAgent(config.Defaults(), provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger(),
		WithTimeout(50*time.Millisecond))

	done := make(chan string, 1)
	go func() {
		// context.Background() on purpose: the deadline under test is the
		// agent's own, not one the caller supplied.
		resp, err := a.Process(context.Background(), bus.InboundMessage{
			SenderID:  "user123",
			Content:   "hello",
			Channel:   "cli",
			Timestamp: time.Now(),
		})
		if err != nil {
			t.Errorf("process returned an error: %v", err)
		}
		done <- resp
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was never called")
	}

	select {
	case resp := <-done:
		// Process reports a timeout in band as reply text with a nil error,
		// because a chat channel has to show the user something.
		if !strings.Contains(strings.ToLower(resp), "took too long") {
			t.Fatalf("timed-out turn answered %q, want the timeout reply", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Process did not return within 5s despite a 50ms timeout")
	}
}
