package channels

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
)

// A numeric allowlist entry is a Telegram user ID and must never be satisfied
// by a free-form display name. The allowlist used to be a single set matched
// against the ID, the username and the first/last name alike, so a stranger who
// set their Telegram first name to the operator's user ID authenticated as the
// operator — display names are attacker-chosen, not unique, and not validated.
func TestTelegramIsAllowed_NumericEntryDoesNotMatchDisplayName(t *testing.T) {
	tg := NewTelegramChannel(bus.NewMessageBus(), &config.TelegramConfig{
		Token:     "test_token",
		AllowFrom: []string{"123456789"},
	})

	if !tg.IsAllowed(123456789, "", "", "") {
		t.Fatalf("the operator's own numeric ID must be allowed")
	}

	attacker := int64(999)
	if tg.IsAllowed(attacker, "", "123456789", "") {
		t.Errorf("first name spoofing the operator's user ID was allowed")
	}
	if tg.IsAllowed(attacker, "123456789", "", "") {
		t.Errorf("username spoofing the operator's user ID was allowed")
	}
	if tg.IsAllowed(attacker, "", "", "123456789") {
		t.Errorf("last name spoofing the operator's user ID was allowed")
	}
	// The full display name is first+last joined, so cover that form too.
	if tg.IsAllowed(attacker, "", "1234", "56789") {
		t.Errorf("a first+last display name spoofing the operator's user ID was allowed")
	}
}

// A name entry still matches a username or a display name, and never an ID.
func TestTelegramIsAllowed_NameEntryMatchesNamesOnly(t *testing.T) {
	tg := NewTelegramChannel(bus.NewMessageBus(), &config.TelegramConfig{
		Token:     "test_token",
		AllowFrom: []string{"@Operator", "First Last"},
	})

	if !tg.IsAllowed(1, "operator", "", "") {
		t.Errorf("username entry did not match")
	}
	if !tg.IsAllowed(1, "", "First", "Last") {
		t.Errorf("display name entry did not match")
	}
	if tg.IsAllowed(1, "stranger", "", "") {
		t.Errorf("an unlisted user was allowed")
	}
}
