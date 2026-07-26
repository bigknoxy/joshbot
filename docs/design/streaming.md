# Design: response streaming

Status: **draft, not implemented.** No code has been written against this.

## Goal

The user sees the assistant's final answer appear incrementally instead of after a
multi-second pause. Applies to the interactive CLI and to Telegram.

## Correction to the earlier sketch

An earlier note said this needs a delta/chunk concept on `bus.OutboundMessage`.
After reading the code, that is the wrong seam, for two reasons:

1. **`MessageBus.Publish` is lossy by design.** It is a non-blocking send onto a
   bounded queue and returns a `bool` when it drops (`internal/bus/bus.go:191`).
   Dropping one message of a conversation is survivable; dropping one *delta*
   silently corrupts the text with no error anywhere. Making deltas reliable
   would mean giving the bus ordering and backpressure it does not have.
2. **Every outbound consumer would have to learn reassembly.** The bus currently
   guarantees one message = one send. Deltas break that for all consumers,
   including ones that have nothing to do with streaming.

The right seam already exists: the optional per-call callback added for
tool-call progress (`internal/agent/progress.go`). It is exactly "a side channel
from the ReAct loop to whoever is presenting, that the bus never sees."

**So: the bus keeps carrying only the final, complete message.** Streaming rides
the callback seam. This also means the persistence path is untouched — session
writes, memory extraction and skill detection all still act on the one complete
string that `reactLoop` already returns.

## The blocker to clear first

`Agent.progress` is a plain struct field, written by `SetProgressCallback` and
read inside `reactLoop`, with no synchronisation.

This is **not a live bug**: today it is only ever set by `runAgentLoop`
(`cmd/joshbot/main.go:1093`), which is strictly serial, and is nil everywhere
else. But it is a trap. Telegram processes messages concurrently, so the moment
a second channel attaches a sink we get both a data race (`go test -race` would
catch it) and cross-talk — chat A's tokens rendered into chat B's reply.

**Fix before any Telegram work: carry the sink per-request rather than on the
`Agent` struct** — a value on the `context.Context` passed into `Process`, so it
is naturally scoped to one request. The existing progress callback should move
onto the same mechanism, since it has the identical shape and the identical trap.

## Architecture

### Sink

```go
// Package agent
type StreamEvent struct {
    Delta string // incremental assistant text
    Done  bool   // final event for this turn
}
type StreamSink func(StreamEvent)
```

Kept separate from `ToolProgressEvent` rather than adding a phase to it: the two
have different lifetimes (per-tool vs per-turn) and different consumers, and
overloading the existing enum would make every current `switch` incomplete.

### ReAct loop

When a sink is attached, `reactLoop` calls `a.provider.ChatStream` instead of
`a.provider.Chat`. It **accumulates chunks into the same `ChatResponse` shape the
non-streaming path produces**, and everything downstream of that point is
unchanged. Text deltas are forwarded to the sink as they arrive.

Two things that look like problems and are not:

- *You cannot know in advance whether a turn ends in tool calls or final text.*
  You do not need to. Stream unconditionally; if the completed turn turns out to
  carry tool calls, the text already emitted was the model reasoning out loud,
  which is fine (and often desirable) to show.
- *Tool arguments arrive fragmented.* They are accumulated before dispatch, same
  as today. Nothing executes on a partial argument.

The genuinely fiddly part is reassembly: tool-call fragments arrive split across
chunks and must be joined **by choice index and tool-call index**, not by
arrival order. This is the main defect source and is where the tests should
concentrate. It is also pure data transformation — no network, no terminal — so
it can be exhaustively unit-tested on recorded chunk sequences.

### CLI sink

Writes deltas straight to the writer, gated on `isTTY` exactly as the progress
renderer already is. Must stop the spinner on the first delta, or the spinner's
`\r` fights the streamed text. Serial, so no locking needed.

### Telegram sink

Telegram cannot stream; it can only be edited. So:

1. Send a placeholder message, keep its ID.
2. Buffer deltas; call `editMessageText` at most once every ~3s, and only if the
   buffer actually changed.
3. Final edit with the complete text.

Three existing behaviours it must compose with, all documented in `AGENTS.md`:

- **Typing indicator** — stop the keep-alive on the first delta; a "typing…"
  status above a partially-written message is wrong.
- **4096-char cap** — once the buffer would exceed it, finalise the current
  message and open a new one. Interacts with the code-fence-aware `splitMessage`.
- **Parse-mode fallback** — applies to *every* edit, not just the final send.
  Partial text is far more likely to be malformed Markdown (an unclosed fence
  mid-stream is normal), so this path will fire much more often than it does
  today.

Rate limits are the real constraint: roughly 1 message/second per chat, and
429s return `retry_after`, which must be honoured rather than retried blindly.

## Reliability trade-off (must be stated, not hidden)

`MultiProvider.ChatStream` falls back **only when the stream fails to open**
(`internal/providers/multiprovider.go:217`). Once the first chunk is delivered,
there is no fallback — the response is already partly on the user's screen and
cannot be un-said.

So streaming is a genuine reliability *downgrade* versus the current path, which
retries a failed provider transparently. The policy should be explicit: on
mid-stream failure, append a visible error marker to what was already shown.
Never silently truncate — a truncated answer that looks complete is worse than a
visible failure.

This is the main argument for shipping it behind a default-off flag until it has
real mileage.

## Open questions

- Do all configured providers support `stream: true` **together with** tool
  definitions? Needs checking per provider, poolside especially.
- Usage/token data may be absent or differently shaped in stream mode. Currently
  `Usage` is only logged (`internal/providers/litellm.go:222`), so nothing breaks
  — but confirm before assuming.

## Staging

Each stage is independently shippable and independently revertable.

1. **Per-request sink plumbing.** Context-carried; move the existing progress
   callback onto it. No behaviour change, no streaming yet. Clears the blocker.
2. **Chunk accumulator + tests.** Pure function, no I/O. Fragmented tool args,
   multi-choice, finish reasons, empty/malformed chunks.
3. **`reactLoop` streams when a sink is attached; CLI sink.** Behind
   `agents.defaults.streaming`, default off.
4. **Telegram sink.** Throttled edits, rate-limit handling, composition with
   typing/split/parse-mode.
5. **Default on, docs.** Only after real dogfooding on both channels.
