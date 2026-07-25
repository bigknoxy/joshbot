package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/tools"
)

type mockAgent struct {
	calls []string
}

func (m *mockAgent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	m.calls = append(m.calls, msg.Content)
	return "reply: " + msg.Content, nil
}

func TestRunAgentLoopProcessesInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &mockAgent{}
	// messageSender is nil in tests - chat ID won't be set but that's fine for unit tests
	if err := runAgentLoop(ctx, cancel, done, input, &output, mock, nil); err != nil {
		t.Fatalf("runAgentLoop error = %v", err)
	}

	if ctx.Err() != context.Canceled {
		t.Fatalf("expected context canceled, got %v", ctx.Err())
	}

	if len(mock.calls) != 1 || mock.calls[0] != "hello" {
		t.Fatalf("expected one call with 'hello', got %v", mock.calls)
	}

	if !strings.Contains(output.String(), "reply: hello") {
		t.Fatalf("missing response in output: %q", output.String())
	}
}

func TestRunAgentLoopExitsOnEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output bytes.Buffer
	input := bytes.NewBufferString("")

	mock := &mockAgent{}
	// messageSender is nil in tests - chat ID won't be set but that's fine for unit tests
	if err := runAgentLoop(ctx, cancel, done, input, &output, mock, nil); err != nil {
		t.Fatalf("runAgentLoop error = %v", err)
	}

	if len(mock.calls) != 0 {
		t.Fatalf("expected no agent calls, got %v", mock.calls)
	}
}

func TestRunAgentLoopSetsChatID(t *testing.T) {
	// Create a real BusMessageSender to verify SetChatID is called
	msgBus := bus.NewMessageBus()
	sender := tools.NewBusMessageSender(msgBus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var output bytes.Buffer
	input := bytes.NewBufferString("hello\nexit\n")

	mock := &mockAgent{}
	if err := runAgentLoop(ctx, cancel, done, input, &output, mock, sender); err != nil {
		t.Fatalf("runAgentLoop error = %v", err)
	}

	// Verify chat ID was set for CLI channel
	chatID, ok := sender.GetChatID("cli")
	if !ok {
		t.Fatalf("expected chat ID to be set for cli channel")
	}
	if chatID != "cli_user" {
		t.Fatalf("expected chat ID 'cli_user', got %q", chatID)
	}
}

// TestFormatProviderStatus covers the `status` provider line for issue #71:
// a provider missing "enabled": true (or, where applicable, an api_key) must
// be visibly flagged rather than listed as if it were configured and ready.
func TestFormatProviderStatus(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string]config.ProviderConfig
		want      string
	}{
		{
			name:      "no providers",
			providers: map[string]config.ProviderConfig{},
			want:      "none",
		},
		{
			name: "api key set but enabled absent",
			providers: map[string]config.ProviderConfig{
				"openrouter": {APIKey: "sk-or-v1-anything"},
			},
			want: `openrouter (disabled — set "enabled": true)`,
		},
		{
			name: "api key set and enabled true",
			providers: map[string]config.ProviderConfig{
				"openrouter": {APIKey: "sk-or-v1-anything", Enabled: true},
			},
			want: "openrouter",
		},
		{
			name: "enabled true but no api key",
			providers: map[string]config.ProviderConfig{
				"nvidia": {Enabled: true},
			},
			want: `nvidia (disabled — missing "api_key")`,
		},
		{
			name: "ollama does not require an api key",
			providers: map[string]config.ProviderConfig{
				"ollama": {Enabled: true},
			},
			want: "ollama",
		},
		{
			name: "mixed providers sorted and each flagged independently",
			providers: map[string]config.ProviderConfig{
				"openrouter": {APIKey: "sk-or-v1-anything"},
				"groq":       {APIKey: "gsk_anything", Enabled: true},
			},
			want: `groq, openrouter (disabled — set "enabled": true)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatProviderStatus(tt.providers)
			if got != tt.want {
				t.Fatalf("formatProviderStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNoProvidersRegisteredError covers issue #71 part 2: the error raised
// when the legacy provider map yields zero registered providers must say
// whether the config had no providers at all, or providers present but none
// enabled -- and the latter must name the "enabled" field so a hand-editing
// user has somewhere to look.
func TestNoProvidersRegisteredError(t *testing.T) {
	t.Run("empty config", func(t *testing.T) {
		err := noProvidersRegisteredError(map[string]config.ProviderConfig{})
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if strings.Contains(err.Error(), "enabled") {
			t.Fatalf("error for an empty config should not talk about the enabled field: %q", err.Error())
		}
	})

	t.Run("present but none enabled", func(t *testing.T) {
		err := noProvidersRegisteredError(map[string]config.ProviderConfig{
			"openrouter": {APIKey: "sk-or-v1-anything"},
		})
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if !strings.Contains(err.Error(), "enabled") {
			t.Fatalf("error should name the enabled field, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), "openrouter") {
			t.Fatalf("error should name the configured-but-inert provider, got %q", err.Error())
		}
	})
}
