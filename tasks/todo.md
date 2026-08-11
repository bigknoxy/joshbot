# Fix `/new` command experience

## Root Cause
CLI channel handles `/new` **locally** — clears input history but never sends through the bus.
Agent never gets the message → session file never deleted → next message loads old session →
old messages exceed budget → compressor wraps them in `<conversation_summary>` →
summary content is degenerate → LLM sees empty tags → responds with bizarre meta-commentary.

## Changes Needed

1. **`internal/channels/cli.go`**: `handleCommand("/new")` — remove local handling, instead send through the bus via `c.sendToBus()` (same as regular messages), letting the agent handle session deletion and return a useful response

2. **`internal/agent/agent.go`**: `handleCommand("new")` — improve response to show current model, tool count, memory window after session reset

3. **`internal/context/context.go`**: `CompressMessages` — if the joined/compressed content is empty, return empty string instead of `"<conversation_summary>\n"` wrapper (defensive fix)

## Verification
- `go build ./cmd/joshbot`
- `gofmt -d .`
- `go vet ./...`
- `go test -race ./...`