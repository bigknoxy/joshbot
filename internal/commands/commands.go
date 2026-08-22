// Package commands is the single source of truth for joshbot's slash
// commands: the name, the argument hint and the one-line description of
// each, and the help text rendered from them.
//
// It exists because the same list used to live in four places — the agent's
// /help, the Telegram menu and its local /help, the Discord list, and the
// CLI's Tab completion — and they drifted: Discord knew two commands while
// the agent answered eight, and a user reading /help on one channel was told
// about commands another channel refused. Every channel now forwards every
// command here to the agent, and every listing renders from this table, so
// adding a command is adding one entry.
//
// The package has no imports from the rest of joshbot on purpose: agent,
// channels and cmd all depend on it.
package commands

import (
	"fmt"
	"strings"
)

// Command is one slash command the agent answers.
type Command struct {
	Name        string
	Args        string // argument hint for help text, "" for none
	Description string
}

// All lists the commands the agent answers, in the order help shows them.
// Every channel forwards all of them to the agent; none is answered locally.
var All = []Command{
	{Name: "start", Description: "Show what this bot can do"},
	{Name: "new", Description: "Start a fresh conversation (clears the session's model and personality)"},
	{Name: "status", Description: "Show the current model, tools and memory window"},
	{Name: "model", Args: "[name]", Description: "Switch model for this session (--global for all sessions)"},
	{Name: "personality", Args: "[name]", Description: "Set a personality, or none to clear it"},
	{Name: "compact", Description: "Summarize older context now"},
	{Name: "resume", Description: "Continue after hitting the iteration limit"},
	{Name: "help", Description: "Show this help"},
}

// Names lists the command names, in order, without the slash.
func Names() []string {
	out := make([]string, len(All))
	for i, c := range All {
		out[i] = c.Name
	}
	return out
}

// Is reports whether name (without the slash) is a command.
func Is(name string) bool {
	for _, c := range All {
		if c.Name == name {
			return true
		}
	}
	return false
}

// HelpText is the /help (and /start) reply, identical on every channel.
func HelpText() string {
	var b strings.Builder
	b.WriteString("Available commands:")
	for _, c := range All {
		b.WriteString("\n")
		b.WriteString(c.usage())
		b.WriteString(" - ")
		b.WriteString(c.Description)
	}
	b.WriteString("\n\nJust type normally to chat with me!")
	return b.String()
}

// UnknownText is the reply to a slash command that is not one of All.
func UnknownText(name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unknown command: /%s\n\nAvailable commands:", name)
	for _, c := range All {
		fmt.Fprintf(&b, "\n%s - %s", c.usage(), c.Description)
	}
	b.WriteString("\n\nOr just send me a message.")
	return b.String()
}

func (c Command) usage() string {
	if c.Args == "" {
		return "/" + c.Name
	}
	return "/" + c.Name + " " + c.Args
}
