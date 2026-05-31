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
./joshbot agent --debug  # CLI mode with debug logging
./joshbot gateway     # Telegram + all channels
./joshbot gateway --debug # Gateway with debug logging
./joshbot status      # Show config/status
./joshbot configure   # Re-run config wizard
./joshbot update      # Self-update

# Docker
docker build -t joshbot .
docker run -it joshbot gateway
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

### Imports

- **Group imports** by stdlib, third-party, then local (separated by blank lines)
- **Use import aliases** for clarity when needed (e.g., `ctxpkg "github.com/bigknoxy/joshbot/internal/context"`)
- **Avoid blank imports** except for drivers/side effects

### Error Handling

- **Return errors as values** - don't panic in library code
- **Wrap errors with context** using `fmt.Errorf("operation failed: %w", err)`
- **Tools return error strings** (`return fmt.Sprintf("Error: File not found: %s", path)`) - don't return errors that require handling
- **Graceful degradation** with fallbacks
- **Check errors explicitly** - don't ignore return values

### Naming Conventions

| Element          | Convention         | Example                          |
|------------------|--------------------|----------------------------------|
| Packages         | lowercase, single  | `tools`, `bus`, `config`         |
| Types            | PascalCase         | `Agent`, `WebFetchTool`          |
| Functions/methods| PascalCase (exported), camelCase (unexported) | `BuildSystemPrompt`, `parseResponse` |
| Private fields   | camelCase          | `cfg`, `running`                 |
| Constants        | PascalCase or UPPER_SNAKE_CASE | `MaxOutput`, `MAX_QUEUE_SIZE` |
| Interfaces       | PascalCase + "er" suffix | `Provider`, `ToolExecutor` |

### Data Modeling

- **Structs** for data types (InboundMessage, LLMResponse, Session, etc.)
- **Embed interfaces** for composition
- **Use struct tags** for JSON/field mapping (`json:"field_name"`)
- **Functional options pattern** for complex configuration

### Interfaces & Extension Points

- **Small, focused interfaces** (prefer 1-3 methods)
- **Interface segregation** - define interfaces where they're used
- **Registry pattern** for tools (`Registry`) and providers
- **Functional options** for flexible construction (`Option func(*Type)`)

### Concurrency Patterns

- **Goroutines** for concurrent operations
- **Channels** for message bus (`chan InboundMessage`, `chan OutboundMessage`)
- **sync.Mutex/sync.RWMutex** for shared state
- **sync.WaitGroup** for goroutine coordination
- **context.Context** for cancellation and timeouts
- **select** for multiplexing channel operations

### Logging

- **charmbracelet/log** for structured logging (`log.Info`, `log.Debug`, `log.Warn`, `log.Error`)
- `log.Debug()` for routine operations, LLM request/response details, tool execution results
- `log.Info()` for significant events (tool execution, service start/stop)
- `log.Warn()` for recoverable issues, empty content detection
- `log.Error()` for failures

**Debug Mode:** Use `--debug` flag to enable DebugLevel logging:
```bash
joshbot agent --debug
joshbot gateway --debug
```

Debug logging provides visibility into:
- LLM response details: content_length, content_preview, tool_calls_count, finish_reason
- HTTP response status codes and model information
- Tool execution results with result_length and preview
- Empty content warnings with model and iteration info for troubleshooting

### String Formatting

- **fmt.Sprintf** for formatted strings
- **String concatenation** with `+` for simple cases
- **strings.Builder** for building complex strings efficiently

### Documentation

- **Package comments** at top of file (starts with "Package X ...")
- **Exported types/functions** must have doc comments
- **Example functions** for usage documentation (`ExampleTool_Execute`)

## Architecture Quick Reference

```
channels/ --> bus/MessageBus --> agent/Agent --> providers/LiteLLMProvider
(CLI,         (chan-based)      (ReAct loop)    (HTTP -> LLM API)
 Telegram)                          |
                               tools/Registry
                               (filesystem, shell,
                                web, message)
```

- **Message bus** decouples channels from agent via `InboundMessage`/`OutboundMessage` channels
- **ReAct loop**: LLM -> tool calls -> reflect -> repeat (max 20 iterations)
- **Memory**: `MEMORY.md` (always in context) + `HISTORY.md` (grep-searchable event log)
- **Skills**: Markdown files with YAML frontmatter, progressive loading (summary -> full content)
- **Sessions**: JSONL files in `~/.joshbot/sessions/`
- **Config**: `~/.joshbot/config.json`, JSON-validated, env vars with `JOSHBOT_` prefix
- **Prompt caching**: Static system prompt cached with mtime-based invalidation (`internal/agent/context.go`)
- **Model-centric config**: Provider auto-detected from model prefix, fallback chains supported (`internal/config/config.go`)

## Key Files

| File | Purpose |
|------|---------|
| `cmd/joshbot/main.go` | CLI entry point, service wiring, onboard flow |
| `internal/agent/agent.go` | Core ReAct agent loop, message processing |
| `internal/agent/context.go` | System prompt assembly with caching |
| `internal/memory/memory.go` | MEMORY.md + HISTORY.md management |
| `internal/skills/skills.go` | Skill discovery and progressive loading |
| `internal/tools/tool.go` | Tool interface (implement this to add new tools) |
| `internal/tools/registry.go` | Tool registration and execution |
| `internal/tools/shell.go` | Shell exec with safety deny-list |
| `internal/channels/telegram.go` | Telegram channel implementation |
| `internal/config/config.go` | All configuration structs, model-centric config, provider detection |
| `internal/bus/bus.go` | Channel-based message bus |
| `internal/providers/provider.go` | Provider interface and types |
| `internal/providers/litellm.go` | OpenRouter-compatible HTTP provider |

## Adding New Components

- **New tool**: Create in `internal/tools/`, implement `Tool` interface, register via `tools.RegistryWithDefaults()`
- **New channel**: Create in `internal/channels/`, implement channel logic, register in `main.go` gateway cmd
- **New skill**: Create `workspace/skills/{name}/SKILL.md` with YAML frontmatter (auto-discovered)

## Config

Config at `~/.joshbot/config.json`. Env var overrides with `JOSHBOT_` prefix (e.g., `JOSHBOT_PROVIDERS__OPENROUTER__API_KEY`). Model-name-based provider routing: `claude` → Anthropic, `gpt` → OpenAI, `gemini` → Gemini. Default fallback: OpenRouter.

**Legacy Provider Format:**
```json
{
  "agents": { "defaults": { "workspace": "~/.joshbot/workspace", "model": "openrouter/anthropic/claude-sonnet-4-20250514", "max_tool_iterations": 20 } },
  "providers": { "openrouter": { "api_key": "" }, "anthropic": {}, ... },
  "channels": { "telegram": { "enabled": false, "token": "", "allow_from": [] } }
}
```

**Model-Centric Format (Recommended):**
```json
{
  "models_config": {
    "models": [
      { "name": "smart", "model": "anthropic/claude-sonnet-4", "api_key": "sk-ant-..." },
      { "name": "fast", "model": "groq/llama-3.3-70b-versatile", "api_key": "gsk_..." }
    ],
    "agent": { "model": "smart", "fallback": ["fast"] }
  },
  "channels": {
    "telegram": { "enabled": false, "token": "", "allow_from": [] }
  }
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

## Provider Enabled Flag

Providers require `"enabled": true` in config to activate. This is a required boolean field:

```json
{
  "providers": {
    "nvidia": {
      "api_key": "nvapi-...",
      "enabled": true
    }
  }
}
```

Environment variables also set `enabled: true` automatically when a provider is configured via env var.

## Env Var Format

All config values can be overridden with `JOSHBOT_` prefix env vars using `__` as path separator:

```bash
# Canonical format (use provider routing key):
export JOSHBOT_PROVIDERS__OPENROUTER__API_KEY="sk-or-..."
export JOSHBOT_PROVIDERS__NVIDIA__API_KEY="nvapi-..."

# Model-centric format:
export JOSHBOT_MODELS_CONFIG__AGENT__MODEL="smart"
export JOSHBOT_MODELS_CONFIG__MODELS__0__NAME="fast"
export JOSHBOT_MODELS_CONFIG__MODELS__0__MODEL="groq/llama-3.3-70b-versatile"
export JOSHBOT_MODELS_CONFIG__MODELS__0__API_KEY="gsk_..."
```

Shorthand forms like `JOSHBOT_OPENROUTER_API_KEY` and `JOSHBOT_NVIDIA_API_KEY` are also accepted for backward compatibility.

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
