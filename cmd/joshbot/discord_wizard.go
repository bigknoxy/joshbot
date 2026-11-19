package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bigknoxy/joshbot/internal/channels"
	"github.com/bigknoxy/joshbot/internal/config"
)

// validateDiscordToken is the token check the wizard uses. It is a variable so
// a test can run the wizard without reaching Discord - the same seam the
// Telegram wizard has.
var validateDiscordToken = channels.ValidateDiscordToken

// setupDiscord asks for a Discord bot token and returns the Discord config.
//
// Same shape as the Telegram wizard, because an operator who has set up one
// channel should not have to learn a second shape:
//
//   - The existing config is the base, so a re-run does not erase fields the
//     wizard does not collect.
//   - The token is read with readSecret, so it is never echoed as it is typed.
//   - The token is checked with a real request to Discord, and a token that
//     could not be checked is reported as unverified rather than as broken.
//   - The result is the same fields the hand-authored path writes. There is no
//     second configuration format.
func setupDiscord(existingCfg *config.Config) *config.DiscordConfig {
	fmt.Println("\n[Step 5b] Discord Setup")

	var base config.DiscordConfig
	if existingCfg != nil {
		base = existingCfg.Channels.Discord
	}
	existingEnabled := base.Enabled
	existingToken := base.Token
	existingAllowFrom := base.AllowFrom

	if existingEnabled && existingToken != "" {
		fmt.Println("Discord is currently configured.")
		fmt.Printf("Current bot token: %s\n", maskToken(existingToken))
		fmt.Println()
		fmt.Println("  1. Keep current token")
		fmt.Println("  2. Change token")
		fmt.Println("  3. Disable Discord")
		fmt.Println()
		fmt.Printf("Choice [1]: ")

		var choice string
		fmt.Scanln(&choice)
		fmt.Println()

		if choice == "3" {
			fmt.Println("Discord disabled.")
			return discordConfigFrom(base, false, "", nil)
		}
		if choice == "1" || choice == "" {
			fmt.Println("Keeping current Discord configuration.")
			return discordConfigFrom(base, true, existingToken, existingAllowFrom)
		}
	} else {
		fmt.Println("Would you like to set up Discord for joshbot?")
		fmt.Println("This allows you to chat with joshbot on Discord.")
		fmt.Println()
		fmt.Println("  1. Yes, set up Discord")
		fmt.Println("  2. No, skip for now")
		fmt.Println()
		fmt.Printf("Choice [2]: ")

		var choice string
		fmt.Scanln(&choice)

		if choice != "1" {
			fmt.Println("\nSkipping Discord setup. You can configure it later by editing:")
			fmt.Printf("  %s\n", filepath.Join(config.DefaultHome, "config.json"))
			return nil
		}
	}

	fmt.Println("\n" + strings.Repeat("─", 45))
	fmt.Println("Discord Bot Setup")
	fmt.Println(strings.Repeat("─", 45))
	fmt.Println()
	fmt.Println("To create a Discord bot:")
	fmt.Println()
	fmt.Println("  1. Open the Discord Developer Portal: https://discord.com/developers/applications")
	fmt.Println("  2. Click 'New Application' and give it a name")
	fmt.Println("  3. Open 'Bot' on the left and click 'Reset Token'")
	fmt.Println("  4. Turn on 'MESSAGE CONTENT INTENT' under Privileged Gateway Intents")
	fmt.Println("  5. Copy the token (keep it secret!)")
	fmt.Println()
	fmt.Println("Enter your bot token when ready.")
	fmt.Println("(Type 'cancel' to abort)")
	fmt.Println()

	for prompt := 1; prompt <= 2; prompt++ {
		fmt.Printf("Bot token: ")

		token, err := readSecret()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Warning: failed to read token, skipping Discord setup")
			return nil
		}

		if token == "cancel" || token == "" {
			if existingEnabled && existingToken != "" {
				fmt.Println("\nDiscord setup cancelled. Keeping the existing Discord configuration.")
				return discordConfigFrom(base, true, existingToken, existingAllowFrom)
			}
			fmt.Println("\nDiscord setup cancelled.")
			return nil
		}

		token = strings.TrimSpace(sanitizeToken(token))

		fmt.Println("\nValidating token...")
		if err := validateDiscordToken(token); err != nil {
			if errors.Is(err, channels.ErrDiscordTokenInvalid) {
				fmt.Printf("Token validation failed: %v\n", err)
				fmt.Println("Discord rejected the token. Double-check it was copied in full,")
				fmt.Println("and that it is the bot token rather than the client secret.")
			} else {
				// The token could not be checked. That is not the same as
				// broken, and saying so is the difference between a useful
				// message and a false alarm.
				fmt.Printf("Could not verify the token: %v\n", err)
				fmt.Println("This does not mean the token is wrong - Discord could not be reached,")
				fmt.Println("or it rate-limited the check. The token was not saved.")
			}
			if prompt == 2 {
				return discordValidationFailed(base)
			}
			fmt.Println("Please check your token and try again.")
			fmt.Println()
			continue
		}
		fmt.Println("Token validated successfully!")

		fmt.Println("\nAllowed users (optional)")
		fmt.Println("Restrict bot access to specific Discord user IDs or usernames.")
		fmt.Println("Leave empty to allow anyone to use the bot.")
		fmt.Println()

		defaultUsers := strings.Join(existingAllowFrom, ", ")
		fmt.Printf("User IDs or usernames (comma-separated) [current: %s]: ", defaultUsers)

		usersRaw := readLine()

		var allowFrom []string
		if usersRaw == "" && len(existingAllowFrom) > 0 {
			allowFrom = existingAllowFrom
		} else if usersRaw != "" {
			for _, u := range strings.Split(usersRaw, ",") {
				u = strings.TrimSpace(u)
				if u != "" {
					allowFrom = append(allowFrom, u)
				}
			}
		}

		if len(allowFrom) == 0 {
			// The Discord channel is deny-by-default: an empty allow list
			// rejects every sender, including the operator's own. The
			// Telegram wizard says the opposite because the Telegram
			// channel is open by default, and the difference has to be
			// stated, not assumed.
			fmt.Println()
			fmt.Println("Note: with no allowed users listed, the Discord channel rejects")
			fmt.Println("everyone - including you. Add your own user ID to use it.")
		}

		fmt.Println("\nDiscord configured!")

		return discordConfigFrom(base, true, token, allowFrom)
	}
	return discordValidationFailed(base)
}

// discordConfigFrom derives the wizard's result from the existing config, so
// a re-run preserves the fields the wizard does not collect.
func discordConfigFrom(base config.DiscordConfig, enabled bool, token string, allowFrom []string) *config.DiscordConfig {
	base.Enabled = enabled
	base.Token = token
	if allowFrom == nil {
		allowFrom = []string{}
	}
	base.AllowFrom = allowFrom
	return &base
}

// discordValidationFailed is what the wizard leaves behind when the token
// never validated: a working existing token is kept, and a fresh install
// ends with Discord off rather than on with a token nobody checked.
func discordValidationFailed(base config.DiscordConfig) *config.DiscordConfig {
	if base.Enabled && base.Token != "" {
		fmt.Println("\nKeeping the existing Discord configuration - it was working before.")
		return discordConfigFrom(base, true, base.Token, base.AllowFrom)
	}
	return discordConfigFrom(base, false, "", nil)
}
