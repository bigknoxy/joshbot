package main

import (
	"github.com/bigknoxy/joshbot/internal/config"
)

// The channel questions are asked on a plain first start now, so the
// onboarding has to be careful about which ones it asks. A channel that is
// already set up is not offered again - the operator has a working token and
// walking them through it again is noise - while the other channel stays
// reachable, so adding a second one is still one answer away.
//
// The decision lives here, not inside the wizards: the wizards are also
// reachable on their own (`joshbot configure`, a re-run of the wizard), and
// there the "keep, change or disable" question is the right one to ask.

// channelSetupOffered decides whether the channel questions are asked.
//
// The default is yes: most installs are for Telegram, so the first thing a
// new user runs used to walk them through provider setup and then leave them
// with a bot that answers nobody. --configure-channels still means the same
// thing it always did; --no-channels is the way to skip the questions.
func channelSetupOffered(configureChannels, noChannels bool) bool {
	return configureChannels || !noChannels
}

// telegramConfigured reports whether Telegram is set up.
func telegramConfigured(cfg *config.Config) bool {
	return cfg != nil && cfg.Channels.Telegram.Enabled && cfg.Channels.Telegram.Token != ""
}

// discordConfigured reports whether Discord is set up.
func discordConfigured(cfg *config.Config) bool {
	return cfg != nil && cfg.Channels.Discord.Enabled && cfg.Channels.Discord.Token != ""
}

// setupTelegramIfNeeded asks for Telegram setup only when Telegram is not
// already configured.
func setupTelegramIfNeeded(existingCfg *config.Config) *config.TelegramConfig {
	if telegramConfigured(existingCfg) {
		existing := existingCfg.Channels.Telegram
		return &existing
	}
	return setupTelegram(existingCfg)
}

// setupDiscordIfNeeded asks for Discord setup only when Discord is not
// already configured.
func setupDiscordIfNeeded(existingCfg *config.Config) *config.DiscordConfig {
	if discordConfigured(existingCfg) {
		existing := existingCfg.Channels.Discord
		return &existing
	}
	return setupDiscord(existingCfg)
}
