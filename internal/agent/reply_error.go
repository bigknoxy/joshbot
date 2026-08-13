package agent

import (
	"errors"
	"strings"
)

// ReplyPrefix is the in-band failure report the ReAct loop returns as reply
// text when a turn could not be completed. Process answers a chat channel, so a
// provider failure has to show the user something and cannot simply be an
// error return.
const ReplyPrefix = "Error processing request: "

// ReplyError turns that in-band report back into a real error, and returns nil
// for an ordinary answer.
//
// Every non-interactive caller of Process needs this. `joshbot agent -m` must
// not exit 0 over an unreachable provider; the Telegram streamer must not
// suppress the bus fallback for one; and the HTTP API must not answer 200 with
// an error string in the assistant's mouth. Treating the text as a normal
// answer is the bug, which is why the prefix is defined once here rather than
// copied into each caller.
func ReplyError(response string) error {
	trimmed := strings.TrimSpace(response)
	if !strings.HasPrefix(trimmed, ReplyPrefix) {
		return nil
	}
	return errors.New(strings.TrimSpace(strings.TrimPrefix(trimmed, ReplyPrefix)))
}
