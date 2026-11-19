package channels

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DiscordAPIBaseURL is the public Discord REST API. The wizard validates
// against it, and so does the gateway.
const DiscordAPIBaseURL = "https://discord.com/api/v10"

const discordTokenTimeout = 10 * time.Second

var (
	// ErrDiscordTokenInvalid means Discord rejected the token.
	ErrDiscordTokenInvalid = errors.New("discord token was rejected")

	// ErrDiscordTokenUnverified means the token could not be checked.
	//
	// It is not the same thing as invalid. Discord's rate limiter answers a
	// token check with 429, and a network failure answers with nothing at
	// all; telling the operator their token is broken when it is fine sends
	// them hunting for a typo that is not there.
	ErrDiscordTokenUnverified = errors.New("discord token could not be verified")
)

// ValidateDiscordToken checks a bot token against the Discord API.
//
// The check is a real request - GET /users/@me with the bot's own token -
// because the only honest way to know a token works is to use it. A 200
// means it works, a 401 means Discord rejected it, and anything else means it
// could not be verified.
func ValidateDiscordToken(token string) error {
	return ValidateDiscordTokenAt(token, "")
}

// ValidateDiscordTokenAt validates a token against a specific API endpoint.
// An empty apiURL means the public Discord API.
func ValidateDiscordTokenAt(token, apiURL string) error {
	base := DiscordAPIBaseURL
	if apiURL != "" {
		base = strings.TrimRight(apiURL, "/")
	}
	return validateDiscordTokenWith(token, base, &http.Client{Timeout: discordTokenTimeout})
}

// validateDiscordTokenWith is the testable core: the endpoint and the client
// are parameters, so the check can be exercised without reaching Discord.
func validateDiscordTokenWith(token, baseURL string, client *http.Client) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("%w: the token is empty", ErrDiscordTokenInvalid)
	}
	// The runtime prefixes the token itself, and an operator who copied it
	// out of the Discord portal may have prefixed it too. Accept both.
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bot "))

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/users/@me", nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDiscordTokenUnverified, err)
	}
	req.Header.Set("Authorization", "Bot "+token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDiscordTokenUnverified, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: Discord returned %s", ErrDiscordTokenInvalid, resp.Status)
	default:
		// 403 can be a permission problem on a perfectly good token, and 429
		// is the rate limiter. Neither is proof of a bad token.
		return fmt.Errorf("%w: Discord returned %s", ErrDiscordTokenUnverified, resp.Status)
	}
}
