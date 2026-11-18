package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bigknoxy/joshbot/internal/config"
)

// The interactive CLI's chosen session.
//
// The interactive loop always talked in cli:cli_user, so a user with three
// conversations had three ids in `joshbot sessions list` and no way back to
// any of them. `joshbot sessions switch` and `joshbot sessions resume` now
// record the choice here, and the interactive CLI picks it up on its next
// start.
//
// The choice is a file rather than a flag because it has to outlive the
// process that made it: the command that switches is a different invocation
// from the conversation it switches to.

const currentSessionFile = "current-session"

// defaultCLIKey is the session the interactive CLI uses when nothing has been
// chosen. It is the historical behaviour, and changing it must not be a
// side effect of using the new commands.
const defaultCLIKey = "cli:cli_user"

// currentSessionPath is where the choice is kept.
func currentSessionPath(home string) string {
	return filepath.Join(home, currentSessionFile)
}

// cliHomeDir is where the recorded choice is kept. It is a variable so the
// tests can point it somewhere of their own instead of the real home.
var cliHomeDir = func() string { return config.DefaultHome }

// readCurrentSession returns the recorded session key ("channel:senderID"),
// or "" when nothing has been chosen or the file cannot be read. A file that
// cannot be read is not an error at startup: the CLI falls back to its
// default session rather than refusing to start.
func readCurrentSession(home string) string {
	if home == "" {
		return ""
	}
	b, err := os.ReadFile(currentSessionPath(home))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeCurrentSession records the session key the interactive CLI should use.
func writeCurrentSession(home, key string) error {
	if strings.TrimSpace(home) == "" {
		return fmt.Errorf("no joshbot home directory")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", home, err)
	}
	if err := os.WriteFile(currentSessionPath(home), []byte(strings.TrimSpace(key)+"\n"), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", currentSessionPath(home), err)
	}
	return nil
}

// clearCurrentSession returns the interactive CLI to its default session.
func clearCurrentSession(home string) error {
	if err := os.Remove(currentSessionPath(home)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove %s: %w", currentSessionPath(home), err)
	}
	return nil
}

// cliSessionKey resolves the session key the interactive CLI should talk in.
//
// An empty key means the default. A key of "channel:senderID" names both; a
// bare name is a sender in the cli channel, which is how a session id that
// has no channel in it is interpreted.
func cliSessionKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return defaultCLIKey
	}
	if i := strings.IndexByte(key, ':'); i >= 0 {
		ch, sn := key[:i], key[i+1:]
		if ch == "" || sn == "" {
			return defaultCLIKey
		}
		return ch + ":" + sn
	}
	return "cli:" + key
}

// currentCLISessionKey is the session the interactive CLI talks in right now.
//
// It reads the recorded choice from the joshbot home. The home is a fixed
// location, so this needs no plumbing through the call chain, and a file that
// cannot be read simply leaves the default in place.
func currentCLISessionKey() string {
	return cliSessionKey(readCurrentSession(cliHomeDir()))
}
