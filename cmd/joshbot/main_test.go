package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
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
