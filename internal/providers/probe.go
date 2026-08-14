package providers

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrCredentialRejected reports that a provider explicitly refused the
// credential (HTTP 401/403). Distinguished from an indeterminate probe
// failure so callers can say "your key was rejected" rather than "could not
// verify" — the two demand different fixes from the operator.
var ErrCredentialRejected = errors.New("credential rejected")

// ProbeCredential verifies a credential by making the cheapest possible
// *authenticated* request: a one-token chat completion. Listing models is
// not a credential check — OpenRouter's /models answers 200 to any
// Authorization header, so a typo'd key printed "✓ validated" and then
// failed on the user's first real message (the worst possible moment).
//
// Return contract:
//   - nil: the credential was accepted (2xx, or a post-auth 429 — a rate
//     limit is issued to an authenticated key)
//   - ErrCredentialRejected (wrapped, with the upstream text): 401/403
//   - any other error: indeterminate — the caller must say "could not
//     verify", never print a checkmark
func ProbeCredential(cfg Config) error {
	if cfg.APIBase == "" {
		return fmt.Errorf("no API base URL configured")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	cfg.MaxTokens = 1

	p := NewLiteLLMProvider(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	_, err := p.Chat(ctx, ChatRequest{
		Model:     cfg.Model,
		MaxTokens: 1,
		Messages:  []Message{{Role: RoleUser, Content: "ping"}},
	})
	if err == nil {
		return nil
	}

	switch extractStatusCode(err.Error()) {
	case 401, 403:
		return fmt.Errorf("%w: %v", ErrCredentialRejected, err)
	case 429:
		// A rate limit is issued to an authenticated key.
		return nil
	default:
		return fmt.Errorf("could not verify credentials: %w", err)
	}
}
