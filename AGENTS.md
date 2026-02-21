# AGENTS.md - Coding Agent Guidelines for joshbot

## Project Overview

joshbot is a lightweight personal AI assistant (~3,600 LOC Go) with self-learning
memory, skill self-creation, and Telegram integration. Architecture: goroutine-based
message bus decoupling chat channels from a ReAct agent loop backed by multi-provider
LLM via OpenRouter-compatible APIs.

## Build & Run Commands

```bash
# Build
go build -o joshbot ./cmd/joshbot

# Install to $GOPATH/bin
go install ./cmd/joshbot

# Run
./joshbot onboard          # First-time setup
./joshbot agent            # Interactive CLI mode
./joshbot gateway          # Telegram + all channels
./joshbot status           # Show config/status

# Run directly (development)
go run ./cmd/joshbot agent

# Docker
docker build -t joshbot .
docker run -it joshbot gateway
```

## Testing

Tests are colocated with source files using `_test.go` suffix:

```bash
go test ./...                              # Run all tests
go test ./internal/tools                   # Run one package
go test ./internal/tools -run TestShell    # Run specific test
go test -v ./internal/agent                # Verbose output
go test -race ./...                        # With race detector
```

Place tests in the same directory as the code being tested with `_test.go` suffix.
Most components are testable in isolation (tools, config, bus, session, memory, skills).

## Linting & Formatting

Go tooling is built-in. Use these commands:

```bash
go fmt ./...              # Format code
go vet ./...              # Static analysis
go mod tidy               # Clean up go.mod/go.sum

# Optional: install additional linters
go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck ./...         # Advanced static analysis
```

## Code Style

### Package Structure

The project follows standard Go project layout:
- `cmd/joshbot/` - Main application entry point
- `internal/` - Private application code (not importable externally)
- `pkg/` - Public packages (importable by external projects)

Every `.go` file follows this order:
1. Package comment (starts with "Package X ...")
2. Package declaration
3. Stdlib imports
4. Third-party imports (blank line separator)
5. Local imports (blank line separator)

```go
// Package tools provides the tool system for joshbot's agent.
package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/bigknoxy/joshbot/internal/providers"
)
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
- `log.Debug()` for routine operations
- `log.Info()` for significant events (tool execution, service start/stop)
- `log.Warn()` for recoverable issues
- `log.Error()` for failures

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

## Key Files

| File | Purpose |
|------|---------|
| `cmd/joshbot/main.go` | CLI entry point, service wiring, onboard flow |
| `internal/agent/agent.go` | Core ReAct agent loop, message processing |
| `internal/agent/context.go` | System prompt assembly |
| `internal/memory/memory.go` | MEMORY.md + HISTORY.md management |
| `internal/skills/skills.go` | Skill discovery and progressive loading |
| `internal/tools/tool.go` | Tool interface (implement this to add new tools) |
| `internal/tools/registry.go` | Tool registration and execution |
| `internal/tools/shell.go` | Shell exec with safety deny-list |
| `internal/channels/telegram.go` | Telegram channel implementation |
| `internal/config/config.go` | All configuration structs and loading |
| `internal/bus/bus.go` | Channel-based message bus |
| `internal/providers/provider.go` | Provider interface and types |
| `internal/providers/litellm.go` | OpenRouter-compatible HTTP provider |

## Adding New Components

**New tool**: Create `internal/tools/my_tool.go`, implement the `Tool` interface
(methods: `Name()`, `Description()`, `Parameters()`, `Execute()`), register via
`RegistryWithDefaults()` in `main.go` or create custom registry setup.

**New channel**: Create `internal/channels/my_channel.go`, implement channel logic
that publishes `InboundMessage` to the bus and subscribes to `OutboundMessage`.

**New skill**: Create `workspace/skills/{name}/SKILL.md` with YAML frontmatter. Auto-discovered.

## Go Version

Requires **Go 1.24+**. Uses modern features: generic types, structured logging,
improved error handling with `%w`, and context-aware cancellation throughout.

## Lessons Learned

> **IMPORTANT**: Always check `docs/MEMORY.md` when encountering issues. This file captures detailed failure modes, root causes, and prevention rules from past mistakes. Avoid repeating errors by reviewing learned lessons first.

### Cross-Platform Factory Pattern (Go)

When using build tags for platform-specific implementations:

1. **Each platform factory file MUST export the same function signature**
   - `factory_linux.go` → `func NewManager(cfg Config) (Manager, error)`
   - `factory_darwin.go` → `func NewManager(cfg Config) (Manager, error)`
   - `factory_other.go` → `func NewManager(cfg Config) (Manager, error)`

2. **Never put the factory function in the interface/struct file**
   - ❌ Bad: `service.go` defines `NewManager()`
   - ✅ Good: `service.go` defines interface only; `factory_*.go` files provide implementations

3. **Build tags must be exclusive per file**
   - `//go:build linux` for Linux
   - `//go:build darwin` for macOS
   - `//go:build !linux && !darwin` for fallback

4. **Test cross-platform builds locally before release**
   ```bash
   GOOS=linux GOARCH=amd64 go build ./...
   GOOS=darwin GOARCH=arm64 go build ./...
   GOOS=windows GOARCH=amd64 go build ./...
   ```

5. **Running as root: sudo not available**
   - When user is root (uid 0), `sudo` command doesn't exist
   - Detect with `os.Getuid() == 0` and skip sudo prefix
   - Applies to systemd service installation, file operations needing elevated permissions
