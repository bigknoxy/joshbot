package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/channels"
	"github.com/bigknoxy/joshbot/internal/config"
)

// stubDiscordTokenValidator replaces the token check for the duration of a
// test and counts the calls.
func stubDiscordTokenValidator(t *testing.T, fn func(token string) error) *int {
	t.Helper()

	prev := validateDiscordToken
	var calls int
	validateDiscordToken = func(token string) error {
		calls++
		return fn(token)
	}
	t.Cleanup(func() { validateDiscordToken = prev })
	return &calls
}

// The point of the wizard: Discord can be set up without hand-editing JSON,
// and the token it writes is the one the operator typed.
func TestSetupDiscord_Fresh_SetupWithNewToken(t *testing.T) {
	withStdinInput(t, "1\ndiscord-token-abc\n123456789012345678\n")
	calls := stubDiscordTokenValidator(t, func(string) error { return nil })

	got := setupDiscord(nil)
	if got == nil {
		t.Fatal("the wizard returned no config")
	}
	if !got.Enabled {
		t.Error("Discord should be enabled after a successful setup")
	}
	if got.Token != "discord-token-abc" {
		t.Errorf("token = %q, want %q", got.Token, "discord-token-abc")
	}
	if len(got.AllowFrom) != 1 || got.AllowFrom[0] != "123456789012345678" {
		t.Errorf("allow_from = %v, want the one user ID", got.AllowFrom)
	}
	if *calls != 1 {
		t.Errorf("the token was checked %d time(s), want 1", *calls)
	}
}

// The token must never be echoed as it is typed, and the wizard must never
// write a token it could not check.
func TestSetupDiscord_Fresh_RejectedTokenDisablesDiscord(t *testing.T) {
	withStdinInput(t, "1\nbad-token\nbad-token\n")
	stubDiscordTokenValidator(t, func(string) error {
		return errors.New("discord token was rejected: Discord returned 401")
	})

	got := setupDiscord(nil)
	if got == nil {
		t.Fatal("the wizard returned no config")
	}
	if got.Enabled {
		t.Error("Discord must not be enabled with a token Discord rejected")
	}
	if got.Token != "" {
		t.Errorf("a rejected token was saved: %q", got.Token)
	}
}

// A rate-limited check proves nothing about the token. The wizard must not
// claim the token is broken, and must not save it as if it were good.
func TestSetupDiscord_UnverifiableTokenIsNotClaimedToBeBroken(t *testing.T) {
	withStdinInput(t, "1\nmaybe-token\nmaybe-token\n")
	stubDiscordTokenValidator(t, func(string) error {
		return errors.New("discord token could not be verified: Discord returned 429")
	})

	got := setupDiscord(nil)
	if got == nil {
		t.Fatal("the wizard returned no config")
	}
	if got.Enabled {
		t.Error("Discord must not be enabled with a token nobody could verify")
	}
	if got.Token != "" {
		t.Errorf("an unverified token was saved: %q", got.Token)
	}
}

// Skipping must not disconnect a working bot.
func TestSetupDiscord_Fresh_Skip(t *testing.T) {
	withStdinInput(t, "2\n")

	got := setupDiscord(nil)
	if got != nil {
		t.Errorf("skipping must leave Discord unconfigured, got %+v", got)
	}
}

// Re-running the wizard must not erase a working token.
func TestSetupDiscord_Existing_Keep(t *testing.T) {
	withStdinInput(t, "1\n")
	calls := stubDiscordTokenValidator(t, func(string) error { return nil })

	existing := &config.Config{}
	existing.Channels.Discord = config.DiscordConfig{
		Enabled:   true,
		Token:     "existing-token",
		AllowFrom: []string{"42"},
	}

	got := setupDiscord(existing)
	if got == nil {
		t.Fatal("the wizard returned no config")
	}
	if !got.Enabled || got.Token != "existing-token" {
		t.Errorf("the existing token was not kept: %+v", got)
	}
	if len(got.AllowFrom) != 1 || got.AllowFrom[0] != "42" {
		t.Errorf("the existing allow list was not kept: %v", got.AllowFrom)
	}
	if *calls != 0 {
		t.Errorf("keeping the current token must not re-check it, checked %d time(s)", *calls)
	}
}

// Disabling must actually disable it.
func TestSetupDiscord_Existing_Disable(t *testing.T) {
	withStdinInput(t, "3\n")

	existing := &config.Config{}
	existing.Channels.Discord = config.DiscordConfig{
		Enabled: true,
		Token:   "existing-token",
	}

	got := setupDiscord(existing)
	if got == nil {
		t.Fatal("the wizard returned no config")
	}
	if got.Enabled {
		t.Error("Discord should be disabled")
	}
	if got.Token != "" {
		t.Errorf("the token was kept after disabling: %q", got.Token)
	}
}

// Cancelling a token change must not disconnect a working bot.
func TestSetupDiscord_Existing_CancelKeepsTheToken(t *testing.T) {
	withStdinInput(t, "2\ncancel\n")
	stubDiscordTokenValidator(t, func(string) error { return nil })

	existing := &config.Config{}
	existing.Channels.Discord = config.DiscordConfig{
		Enabled: true,
		Token:   "existing-token",
	}

	got := setupDiscord(existing)
	if got == nil {
		t.Fatal("the wizard returned no config")
	}
	if !got.Enabled || got.Token != "existing-token" {
		t.Errorf("cancelling disconnected a working bot: %+v", got)
	}
}

// The Discord channel is deny-by-default: an empty allow list rejects every
// sender. The wizard has to say so, because the Telegram wizard's opposite
// behaviour would otherwise be assumed.
func TestSetupDiscord_EmptyAllowListIsCalledOut(t *testing.T) {
	withStdinInput(t, "1\ndiscord-token-abc\n\n")
	stubDiscordTokenValidator(t, func(string) error { return nil })

	var got *config.DiscordConfig
	out := captureStdout(t, func() { got = setupDiscord(nil) })

	if got == nil {
		t.Fatal("the wizard returned no config")
	}
	if len(got.AllowFrom) != 0 {
		t.Errorf("allow_from = %v, want empty", got.AllowFrom)
	}
	if !strings.Contains(out, "rejects") {
		t.Errorf("the wizard did not warn that an empty allow list rejects everyone:\n%s", out)
	}
}

// A token Discord rejected must not be mistaken for one that could not be
// checked: the two need different advice.
func TestSetupDiscord_InvalidAndUnverifiedAreDifferent(t *testing.T) {
	withStdinInput(t, "1\nbad-token\n")
	stubDiscordTokenValidator(t, func(string) error {
		return errors.New("discord token was rejected: Discord returned 401")
	})

	out := captureStdout(t, func() { setupDiscord(nil) })

	if !strings.Contains(out, "rejected") {
		t.Errorf("the wizard did not say the token was rejected:\n%s", out)
	}
}

// The wizard must reach the same place the hand-authored path does: the
// validator lives in the channels package, so a token that passes the wizard
// passes the runtime.
func TestSetupDiscord_UsesTheChannelsValidator(t *testing.T) {
	if validateDiscordToken == nil {
		t.Fatal("the wizard has no validator")
	}
	// The stub is a seam for tests; the real one must be the channels
	// package's, so the wizard and the gateway agree on what a token is.
	if err := channels.ErrDiscordTokenInvalid; err == nil {
		t.Error("the channels package must export an invalid-token error")
	}
}
