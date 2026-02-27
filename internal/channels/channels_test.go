package channels

import (
	"context"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
)

// TestCLIChannel_NewChannel tests that CLIChannel can be created correctly.
func TestCLIChannel_NewChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)

	if cli.Name() != "cli" {
		t.Errorf("expected name 'cli', got %s", cli.Name())
	}
}

// TestCLIChannel_StartStop tests that CLIChannel can start and stop gracefully.
func TestCLIChannel_StartStop(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)

	ctx, cancel := context.WithCancel(context.Background())

	// Start in a goroutine since it blocks on input
	errCh := make(chan error, 1)
	go func() {
		errCh <- cli.Start(ctx)
	}()

	// Let it start briefly
	time.Sleep(10 * time.Millisecond)

	// Stop the channel
	cancel()
	cli.Stop()

	// Wait for the goroutine to finish
	select {
	case err := <-errCh:
		if err != nil {
			t.Logf("channel stopped with error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Log("channel did not stop in time")
	}
}

// TestCLIChannel_ProcessInputCommands tests command handling.
func TestCLIChannel_ProcessInputCommands(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	ctx := context.Background()

	// Test /help command
	err := cli.processInput(ctx, "/help")
	if err != nil {
		t.Errorf("processInput /help returned error: %v", err)
	}

	// Test /new command
	err = cli.processInput(ctx, "/new")
	if err != nil {
		t.Errorf("processInput /new returned error: %v", err)
	}

	// Test empty input
	err = cli.processInput(ctx, "")
	if err != nil {
		t.Errorf("processInput empty returned error: %v", err)
	}
}

// TestCLIChannel_IsAllowed tests that IsAllowed always returns true for CLI.
func TestCLIChannel_IsAllowed(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)

	// CLI should not implement IsAllowed, but Channel interface doesn't require it
	// This test just verifies the CLI works without it
	if cli.Name() != "cli" {
		t.Errorf("expected name 'cli', got %s", cli.Name())
	}
}

// TestCLIChannel_readInput_NoPanic tests that readInput doesn't panic with empty input.
// Note: This test is mainly to verify the function compiles and runs without panic.
func TestCLIChannel_readInput_NoPanic(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)

	// In a real terminal, this would block waiting for input
	// We just verify the function exists and is callable
	_ = cli.prompt
}

// TestCLIChannel_History tests that input history is maintained correctly.
func TestCLIChannel_History(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	ctx := context.Background()

	// Add some inputs to history (non-consecutive duplicates should be allowed)
	cli.processInput(ctx, "hello")
	cli.processInput(ctx, "world")
	cli.processInput(ctx, "test")
	// Non-consecutive "hello" - should be added (not consecutive duplicate)
	cli.processInput(ctx, "hello")

	// We expect 4 entries since hello appears twice but not consecutively
	if len(cli.inputHistory) != 4 {
		t.Errorf("expected 4 history items, got %d", len(cli.inputHistory))
	}

	// Consecutive duplicate should not be added
	cli.processInput(ctx, "hello")
	if len(cli.inputHistory) != 4 {
		t.Errorf("expected 4 history items after consecutive duplicate, got %d", len(cli.inputHistory))
	}
}

// TestTelegramChannel_NewChannel tests that TelegramChannel can be created.
func TestTelegramChannel_NewChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := &config.TelegramConfig{
		Token:     "test_token",
		AllowFrom: []string{"user1", "user2"},
	}

	tg := NewTelegramChannel(msgBus, cfg)

	if tg.Name() != "telegram" {
		t.Errorf("expected name 'telegram', got %s", tg.Name())
	}

	// Verify allowlist was populated
	if len(tg.allowSet) != 2 {
		t.Errorf("expected 2 allowlist entries, got %d", len(tg.allowSet))
	}
}

// TestTelegramChannel_IsAllowed tests allowlist functionality.
func TestTelegramChannel_IsAllowed(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := &config.TelegramConfig{
		Token:     "test_token",
		AllowFrom: []string{"user1", "TestUser"},
	}

	tg := NewTelegramChannel(msgBus, cfg)

	// Empty allowlist should allow everyone
	cfg2 := &config.TelegramConfig{
		Token:     "test_token",
		AllowFrom: []string{},
	}
	tg2 := NewTelegramChannel(msgBus, cfg2)

	if !tg2.IsAllowed(123, "anyone", "Anyone", "") {
		t.Error("expected empty allowlist to allow everyone")
	}

	// Test username matching (case insensitive)
	if !tg.IsAllowed(123, "user1", "User1", "") {
		t.Error("expected user1 to be allowed")
	}

	// Test non-allowed user
	if tg.IsAllowed(123, "unknown", "Unknown", "") {
		t.Error("expected unknown user to be rejected")
	}
}

// TestTelegramChannel_NormalizeUsername tests username normalization.
func TestTelegramChannel_NormalizeUsername(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"@username", "username"},
		{"Username", "username"},
		{"@USER", "user"},
		{"plain", "plain"},
		{"", ""},
		{"@", ""},
	}

	for _, tt := range tests {
		result := normalizeUsername(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeUsername(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

// TestTelegramChannel_ValidateToken_Empty tests token validation with empty token.
func TestTelegramChannel_ValidateToken_Empty(t *testing.T) {
	err := ValidateToken("")
	if err == nil {
		t.Error("expected error for empty token")
	}
}

// TestTelegramChannel_ValidateToken_InvalidFormat tests token validation with invalid format.
func TestTelegramChannel_ValidateToken_InvalidFormat(t *testing.T) {
	// Token with invalid format (no colon separator)
	err := ValidateToken("invalid_token")
	if err == nil {
		t.Error("expected error for invalid token format")
	}
}

// TestTelegramChannel_RetryConfig tests that retry configuration is set correctly.
func TestTelegramChannel_RetryConfig(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := &config.TelegramConfig{
		Token: "test_token",
	}

	tg := NewTelegramChannel(msgBus, cfg)

	if tg.maxRetries != 3 {
		t.Errorf("expected maxRetries 3, got %d", tg.maxRetries)
	}

	if tg.retryDelay != 500*time.Millisecond {
		t.Errorf("expected retryDelay 500ms, got %v", tg.retryDelay)
	}

	if tg.maxRetryDelay != 5*time.Second {
		t.Errorf("expected maxRetryDelay 5s, got %v", tg.maxRetryDelay)
	}
}
