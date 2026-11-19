package main

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// The channel the operator came from is the one thing still unconfigured
// after a plain first start, so the wizard has to ask about it.

func TestSetupTelegramIfNeeded_AsksWhenTelegramIsNotConfigured(t *testing.T) {
	withStdinInput(t, "2\n") // skip

	got := setupTelegramIfNeeded(nil)
	if got != nil {
		t.Errorf("skipping must leave Telegram unconfigured, got %+v", got)
	}
}

// A channel that is already set up is not offered again.
func TestSetupTelegramIfNeeded_KeepsAConfiguredTelegram(t *testing.T) {
	existing := &config.Config{}
	existing.Channels.Telegram = config.TelegramConfig{
		Enabled: true,
		Token:   "existing-token",
	}

	got := setupTelegramIfNeeded(existing)
	if got == nil {
		t.Fatal("a configured Telegram must not be dropped")
	}
	if !got.Enabled || got.Token != "existing-token" {
		t.Errorf("the existing Telegram configuration was not kept: %+v", got)
	}
}

// The same for Discord.
func TestSetupDiscordIfNeeded_KeepsAConfiguredDiscord(t *testing.T) {
	existing := &config.Config{}
	existing.Channels.Discord = config.DiscordConfig{
		Enabled: true,
		Token:   "existing-token",
	}

	got := setupDiscordIfNeeded(existing)
	if got == nil {
		t.Fatal("a configured Discord must not be dropped")
	}
	if !got.Enabled || got.Token != "existing-token" {
		t.Errorf("the existing Discord configuration was not kept: %+v", got)
	}
}

// A channel that is configured is recognised as configured, and one that is
// half-configured is not - an enabled channel with no token answers nobody.
func TestChannelConfiguredNeedsBothEnabledAndAToken(t *testing.T) {
	cfg := &config.Config{}
	if telegramConfigured(cfg) {
		t.Error("an empty config has no configured Telegram")
	}
	if discordConfigured(cfg) {
		t.Error("an empty config has no configured Discord")
	}

	cfg.Channels.Telegram = config.TelegramConfig{Enabled: true}
	if telegramConfigured(cfg) {
		t.Error("an enabled Telegram with no token is not configured")
	}
	cfg.Channels.Telegram.Token = "t"
	if !telegramConfigured(cfg) {
		t.Error("an enabled Telegram with a token is configured")
	}

	cfg.Channels.Discord = config.DiscordConfig{Enabled: true, Token: "d"}
	if !discordConfigured(cfg) {
		t.Error("an enabled Discord with a token is configured")
	}
}

// The questions are asked by default now; --no-channels is the way to skip
// them, and the old flag still means what it always meant.
func TestChannelSetupIsOfferedByDefault(t *testing.T) {
	if !channelSetupOffered(false, false) {
		t.Error("channel setup must be offered by default")
	}
	if !channelSetupOffered(true, false) {
		t.Error("--configure-channels must still mean the same thing")
	}
	if channelSetupOffered(false, true) {
		t.Error("--no-channels must skip the channel questions")
	}
}
