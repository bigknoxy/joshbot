package channels

import (
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
)

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
