package main

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// Streaming is on by default, so this predicate runs on every inbound message
// in the gateway. Each condition it drops fails silently in a different way:
// a Discord or CLI turn reaches for a Telegram streamer that cannot address it,
// and a streamed heartbeat posts into a chat nobody asked anything in — the
// handler suppresses a quiet heartbeat's reply, so the stream would be the only
// trace of it.
func TestShouldStream(t *testing.T) {
	telegram := bus.InboundMessage{Channel: "telegram", SenderID: "u1"}

	tests := []struct {
		name         string
		streaming    bool
		haveTelegram bool
		msg          bus.InboundMessage
		want         bool
	}{
		{"a telegram turn with streaming on", true, true, telegram, true},
		{"streaming disabled in config", false, true, telegram, false},
		{"telegram not enabled, so there is no streamer to build", true, false, telegram, false},
		{"a discord turn", true, true, bus.InboundMessage{Channel: "discord", SenderID: "u1"}, false},
		{"a cli turn", true, true, bus.InboundMessage{Channel: "cli", SenderID: "u1"}, false},
		{"the heartbeat, which nobody is waiting on", true, true,
			bus.InboundMessage{Channel: "telegram", SenderID: "heartbeat"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldStream(tt.streaming, tt.haveTelegram, tt.msg); got != tt.want {
				t.Errorf("shouldStream(%v, %v, %+v) = %v, want %v",
					tt.streaming, tt.haveTelegram, tt.msg, got, tt.want)
			}
		})
	}
}
