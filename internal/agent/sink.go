package agent

import (
	"context"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// UsageSink receives per-LLM-call token usage from the ReAct loop. It is
// called once per provider Chat response so a headless caller (e.g. the
// JSON output modes of the CLI) can accumulate total token usage for a
// request. Like ProgressFunc and StreamSink it rides the per-request
// context, never touching shared Agent state.
type UsageSink func(providers.Usage)

// StreamEvent is emitted during streaming to deliver incremental assistant
// text to a per-request sink. (Stage 3 of the streaming work; not yet wired
// into the ReAct loop — this type exists so stage 3 can reuse the same
// context-carried mechanism built here in stage 1.)
type StreamEvent struct {
	// Delta is the incremental assistant text for this chunk.
	Delta string
	// Done marks the final event for this turn.
	Done bool
}

// StreamSink receives StreamEvents from the ReAct loop when streaming is
// enabled. (Stage 3; not yet used.)
type StreamSink func(StreamEvent)

// requestSink carries per-request presentation callbacks from the caller
// (e.g. the interactive CLI) into the ReAct loop. It is attached to the
// context.Context passed to Process, so each request gets its own sink with
// no shared mutable state on Agent — avoiding the data race and cross-talk
// that would occur if the sink were a struct field on the shared Agent.
//
// Both the tool-progress callback (ProgressFunc) and the streaming sink
// (StreamSink) ride the same mechanism, since they share the identical
// shape and the identical trap: per-request, side-channel, bus-invisible.
type requestSink struct {
	progress ProgressFunc
	stream   StreamSink
	usage    UsageSink
}

// sinkKey is an unexported context key type to avoid collisions.
type sinkKey struct{}

// WithSink attaches a per-request tool-progress callback to the context.
// When the context already carries a sink, the new progress callback
// replaces the existing one. Passing nil clears the progress callback
// while preserving any stream sink.
//
// This is the per-request replacement for the removed WithProgressCallback
// option / SetProgressCallback method, which stored the callback as mutable
// shared state on Agent.
func WithSink(ctx context.Context, progress ProgressFunc) context.Context {
	existing := sinkFromContext(ctx)
	if existing != nil {
		// Copy to avoid mutating a value that may be shared across goroutines.
		s := *existing
		s.progress = progress
		return context.WithValue(ctx, sinkKey{}, &s)
	}
	return context.WithValue(ctx, sinkKey{}, &requestSink{progress: progress})
}

// WithStreamSink attaches a per-request stream sink to the context.
// When the context already carries a sink, the new stream sink replaces
// the existing one. Passing nil clears the stream sink while preserving
// any progress callback.
//
// (Stage 3; not yet wired into the ReAct loop.)
func WithStreamSink(ctx context.Context, sink StreamSink) context.Context {
	existing := sinkFromContext(ctx)
	if existing != nil {
		s := *existing
		s.stream = sink
		return context.WithValue(ctx, sinkKey{}, &s)
	}
	return context.WithValue(ctx, sinkKey{}, &requestSink{stream: sink})
}

// sinkFromContext extracts the requestSink from the context.
// Returns nil if no sink was attached.
func sinkFromContext(ctx context.Context) *requestSink {
	s, _ := ctx.Value(sinkKey{}).(*requestSink)
	return s
}

// progressFromContext returns the per-request tool-progress callback from
// the context, or nil if none is attached. A nil return is a complete no-op:
// callers should guard with a nil check before invoking.
func progressFromContext(ctx context.Context) ProgressFunc {
	if s := sinkFromContext(ctx); s != nil {
		return s.progress
	}
	return nil
}

// streamSinkFromContext returns the per-request stream sink from the
// context, or nil if none is attached.
//
// (Stage 3; not yet used.)
func streamSinkFromContext(ctx context.Context) StreamSink {
	if s := sinkFromContext(ctx); s != nil {
		return s.stream
	}
	return nil
}

// WithUsageSink attaches a per-request token-usage callback to the context.
// When the context already carries a sink, the new usage callback replaces
// the existing one. Passing nil clears the usage callback while preserving
// any progress and stream callbacks.
func WithUsageSink(ctx context.Context, usage UsageSink) context.Context {
	existing := sinkFromContext(ctx)
	if existing != nil {
		s := *existing
		s.usage = usage
		return context.WithValue(ctx, sinkKey{}, &s)
	}
	return context.WithValue(ctx, sinkKey{}, &requestSink{usage: usage})
}

// usageFromContext returns the per-request usage callback from the context,
// or nil if none is attached.
func usageFromContext(ctx context.Context) UsageSink {
	if s := sinkFromContext(ctx); s != nil {
		return s.usage
	}
	return nil
}

// ProgressFromContext exposes the per-request progress callback to callers
// outside this package (e.g. test doubles that stand in for *agent.Agent and
// need to emit synthetic tool-progress events the way the real ReAct loop
// does). Returns nil if none is attached.
func ProgressFromContext(ctx context.Context) ProgressFunc {
	return progressFromContext(ctx)
}

// UsageFromContext exposes the per-request usage callback to callers outside
// this package. Returns nil if none is attached.
func UsageFromContext(ctx context.Context) UsageSink {
	return usageFromContext(ctx)
}
