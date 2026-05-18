# AGENTS.md - Coding Agent Guidelines for joshbot

## Project Overview

joshbot is a lightweight personal AI assistant (~16,000 LOC Go non-test) with self-learning memory, skill self-creation, and Telegram integration. Architecture: goroutine-based message bus decoupling chat channels from a ReAct agent loop backed by multi-provider LLM via OpenRouter-compatible APIs.

Module: `github.com/bigknoxy/joshbot`. Go 1.24.0.

## Build & Run

No Makefile. Standard Go tooling:

```bash
go build -o joshbot ./cmd/joshbot     # Build
go install ./cmd/joshbot               # Install
go run ./cmd/joshbot agent             # Dev mode

# Runtime commands
./joshbot onboard     # First-time setup wizard
./joshbot agent       # Interactive CLI mode
./joshbot agent -m "..."  # Single message mode
./joshbot gateway     # Telegram + all channels
./joshbot status      # Show config/status
./joshbot configure   # Re-run config wizard
./joshbot update      # Self-update
```

Docker: `docker build -t joshbot . && docker run -it joshbot gateway`

## Testing

```bash
go test ./...                                          # All tests
go test -race ./...                                    # With race detector
go test -v ./internal/tools -run TestShell             # Single test
```

Tests colocated with `_test.go` suffix. Integration tests in `tests/` plus `internal/integration/`. Python test scripts in `tests/` (legacy from migration).

## Linting & Formatting

```bash
go fmt ./...
go vet ./...
go mod tidy
```

## Code Architecture

```
cmd/joshbot/main.go            -- CLI entry (urfave/cli/v2), service wiring, ~3,340 LOC
  internal/
    agent/agent.go             -- ReAct loop (max 20 iterations)
    agent/context.go           -- System prompt assembly (identity files + memory + skills)
    bus/bus.go                 -- Channel-based message bus (Inbound/OutboundMessage in bus.go)
    channels/cli.go            -- CLI readline channel (bufio.Reader)
    channels/telegram.go       -- Telegram long-polling channel (telebot)
    config/config.go           -- JSON config, env overrides (JOSHBOT_ prefix)
    context/context.go         -- Context propagation, registry, budget manager
    copilot/auth.go            -- GitHub Copilot auth flow
    cron/cron.go               -- Cron scheduler
    heartbeat/heartbeat.go     -- HEARTBEAT.md unchecked-task checker
    integration/               -- Integration tests
    learning/learning.go       -- Learning/summary extraction
    log/logger.go              -- Structured logging (charmbracelet/log + lipgloss)
    memory/memory.go           -- MEMORY.md + HISTORY.md management
    providers/                 -- Provider interface + LiteLLM, Ollama, Multiprovider
    service/                   -- Systemd/launchd service install (cross-platform factory)
    session/                   -- JSON-persisted conversation sessions
    skills/skills.go           -- Skill discovery from SKILL.md files
    subagent/subagent.go       -- Restricted subagent runner
    tools/                     -- Tool interface + Registry + filesystem, shell, web, message, async
  pkg/                         -- Incomplete refactor from internal/ (only bus + channels exist here)
    bus/                       -- Duplicates internal/bus
    channels/                  -- Duplicates internal/channels
```

> **IMPORTANT**: Edit `internal/` not `pkg/`. The `pkg/` directory is an incomplete parallel refactor. All production code imports from `internal/`.

## Key Interfaces

```go
// internal/tools/tool.go
type Tool interface {
    Name() string
    Description() string
    Parameters() []Parameter
    Execute(ctx interface{}, args map[string]any) ToolResult
}

// internal/providers/provider.go
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
    Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error)
    Name() string
    Config() Config
}
```

## Adding New Components

- **New tool**: Create in `internal/tools/`, implement `Tool` interface, register via `tools.RegistryWithDefaults()`
- **New channel**: Create in `internal/channels/`, implement channel logic, register in `main.go` gateway cmd
- **New skill**: Create `workspace/skills/{name}/SKILL.md` with YAML frontmatter (auto-discovered)

## Config

Config at `~/.joshbot/config.json`. Env var overrides with `JOSHBOT_` prefix (e.g., `JOSHBOT_PROVIDERS_OPENROUTER_API_KEY`). Model-name-based provider routing: `claude` → Anthropic, `gpt` → OpenAI, `gemini` → Gemini. Default fallback: OpenRouter.

```json
{
  "agents": { "defaults": { "workspace": "~/.joshbot/workspace", "model": "openrouter/anthropic/claude-sonnet-4-20250514", "max_tool_iterations": 20 } },
  "providers": { "openrouter": { "api_key": "" }, "anthropic": {}, ... },
  "channels": { "telegram": { "enabled": false, "token": "", "allow_from": [] } },
  "heartbeat": { "enabled": true, "interval": 30 }
}
```

## Gotchas

- **Telegram message limit**: Telegram Bot API enforces 4096 char max per message. joshbot does not split long messages — exceeding this causes send failure (not retryable). Ensure output stays under limit.
- **CLI stdin blocking**: `bufio.NewReader(os.Stdin).ReadString('\n')` blocks on stdin and can't be interrupted by context cancellation
- **Session key format**: `"channel:senderID"` (e.g., `"cli:cli-user"`, `"telegram:johndoe"`). Computed from `Channel:SenderID` — no explicit SessionKey field
- **OutboundMessage**: Uses `ChannelID` (not `ChatID`) for routing responses back to the correct chat
- **Workspace identity files**: `IDENTITY.md`, `SOUL.md`, `USER.md`, `AGENTS.md`, `TOOLS.md` loaded into system prompt via XML tags
- **Service cross-platform**: `internal/service/` uses build tags (`factory_linux.go`, `factory_darwin.go`, `factory_other.go`) — each must export the same function signature. Also has `systemd.go`, `launchd.go`, `openrc.go`, `unsupported.go`
- **`pkg/` is stale**: `pkg/` duplicates `internal/bus` and `internal/channels` — do not edit unless purposely finishing the refactor

## Pre-Release Checklist

```bash
go build ./cmd/joshbot
gofmt -d .                     # MUST return empty (no formatting diffs)
rm -rf ~/.joshbot
go test -race ./...
./joshbot agent -m "hello"    # Verify response
./joshbot status               # Verify config
```

## Release Process
1. Push changes to main first
2. **WAIT** for CI to pass (green checkmark on the commit) — do not push the tag until CI is green
3. Only then cut the release tag with `git tag vX.Y.Z && git push origin vX.Y.Z`
4. Monitor both the CI workflow and the Release workflow until all jobs are green
