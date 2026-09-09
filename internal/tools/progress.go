package tools

import "context"

// ProgressFunc receives a self-authored, human-readable checkpoint note from
// inside a tool's Execute call — e.g. "trying exa-cli", "exa-cli slow/failed,
// falling back to Exa MCP", "received 5 results". It is fire-and-forget: a
// dropped or slow-to-drain note is cosmetic, never correctness-affecting,
// which is exactly the opposite failure mode of Approver (see approval.go's
// top-of-file comment for why the two context-carried callback patterns in
// this file and that one deliberately differ).
//
// This exists because internal/agent owns the only tool-progress sink today
// (agent.WithSink / agent.ProgressFunc), and internal/tools cannot reach it
// directly — internal/agent imports internal/tools, so the reverse would be
// an import cycle. WithProgress/ProgressFromContext is the plumbing a tool
// uses to emit a mid-call checkpoint; internal/agent bridges it onto the real
// sink by wrapping the context it hands to ExecuteWithContext, turning every
// note into a ToolProgressEvent{Phase: ToolProgressNote} the same way it
// already brackets a call with Start/Done events.
type ProgressFunc func(note string)

// progressKey is an unexported context key.
type progressKey struct{}

// WithProgress attaches a progress callback to the request context. Passing
// nil clears any callback previously attached — unlike WithApprover, this has
// no fail-closed meaning: a context with no progress callback simply produces
// no checkpoint notes, which ProgressFromContext's nil return already covers.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// ProgressFromContext returns the request's progress callback, or nil when
// none is attached. Callers must nil-check before invoking — a missing sink
// is the common case (no interactive caller, or a subagent/programmatic
// caller that never wired one up) and must be a complete no-op.
func ProgressFromContext(ctx context.Context) ProgressFunc {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(progressKey{}).(ProgressFunc)
	return fn
}
