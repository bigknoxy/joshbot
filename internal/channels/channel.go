// Package channels provides chat channel implementations.
package channels

import (
	"context"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// Channel is the interface that all chat channels must implement.
//
// This lived in cli.go until that file was deleted as dead code. The
// interactive CLI is runAgentLoop in cmd/joshbot/main.go and does not go
// through this interface; TelegramChannel is the only implementation.
type Channel interface {
	// Name returns the unique identifier for this channel.
	Name() string

	// Start begins the channel's operation.
	// The channel should run until the context is cancelled or Stop is called.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the channel.
	Stop() error

	// Send delivers an outbound message to the channel for display.
	Send(msg bus.OutboundMessage) error
}
