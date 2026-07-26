package agent

import (
	"time"
)

// ToolProgressPhase identifies whether a ToolProgressEvent marks the start
// or the completion of a tool call.
type ToolProgressPhase int

const (
	// ToolProgressStart is emitted immediately before a tool call is executed.
	ToolProgressStart ToolProgressPhase = iota
	// ToolProgressDone is emitted immediately after a tool call completes,
	// successfully or not.
	ToolProgressDone
)

// ToolProgressEvent describes a single tool-call lifecycle event, emitted to
// an optional ProgressFunc so a caller (e.g. the interactive CLI loop) can
// render "tool started" / "tool finished" indicators without the ReAct loop
// knowing anything about terminals or output formatting.
type ToolProgressEvent struct {
	// Tool is the tool name being invoked (e.g. "shell", "filesystem").
	Tool string
	// Summary is a brief, truncated, human-readable rendering of the most
	// relevant argument (e.g. the shell command, the file path).
	Summary string
	// Phase is ToolProgressStart or ToolProgressDone.
	Phase ToolProgressPhase
	// Elapsed is the tool's execution duration. Zero on ToolProgressStart.
	Elapsed time.Duration
	// Err is the tool execution error, if any. Always nil on ToolProgressStart.
	Err error
}

// ProgressFunc receives ToolProgressEvents from the ReAct loop. It must
// return promptly — it is called synchronously from the loop and a slow or
// blocking callback stalls tool execution.
type ProgressFunc func(ToolProgressEvent)

// WithProgressCallback injects an optional progress callback invoked around
// each tool call in the ReAct loop. Nil (the default, and the effect of
// never calling this option) means zero behavior change: no callback is
// ever invoked.
func WithProgressCallback(fn ProgressFunc) Option {
	return func(a *Agent) {
		a.progress = fn
	}
}

// SetProgressCallback attaches (or clears, via nil) a progress callback on
// an already-constructed Agent. This exists alongside WithProgressCallback
// because callers such as cmd/joshbot construct the Agent once (shared
// across interactive and non-interactive code paths) and only want the
// callback wired up when actually running the interactive terminal loop.
func (a *Agent) SetProgressCallback(fn ProgressFunc) {
	a.progress = fn
}

// keyArgFields lists, per tool name, the argument keys (in priority order)
// most useful to show a human as a one-line summary of what the tool is
// about to do. Tools not listed here fall back to the first argument value
// found (in map iteration order, which is fine for a best-effort summary).
var keyArgFields = map[string][]string{
	"shell":          {"command"},
	"filesystem":     {"path"},
	"web":            {"query", "url"},
	"memory_search":  {"query"},
	"skill_registry": {"name"},
	"cron":           {"description"},
	"message":        {"content"},
}

const summaryMaxLen = 64

// summarizeToolArgs picks the most relevant argument for a tool call and
// returns a brief, truncated string suitable for a single progress line
// (e.g. "shell(go test ./...)"). Returns "" if no string-like argument is
// found.
func summarizeToolArgs(tool string, args map[string]any) string {
	if len(args) == 0 {
		return ""
	}

	if fields, ok := keyArgFields[tool]; ok {
		for _, field := range fields {
			if v, ok := args[field]; ok {
				if s := stringify(v); s != "" {
					return truncateSummaryLen(s)
				}
			}
		}
	}

	// Fallback: first string-like value found. Map iteration order is
	// randomized in Go, so callers should not depend on which field wins
	// when a tool isn't in keyArgFields — this is best-effort only.
	for _, v := range args {
		if s := stringify(v); s != "" {
			return truncateSummaryLen(s)
		}
	}
	return ""
}

func stringify(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func truncateSummaryLen(s string) string {
	if len(s) <= summaryMaxLen {
		return s
	}
	return s[:summaryMaxLen] + "..."
}
