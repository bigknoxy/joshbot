# Contributing to joshbot

Thanks for your interest in contributing! This guide covers how to set up your development environment and submit changes.

## Development Setup

### Requirements

- **Go 1.24+** - The project uses modern Go features
- Git

### Getting Started

```bash
# Clone the repository
git clone https://github.com/bigknoxy/joshbot.git
cd joshbot

# Build the binary
go build -o joshbot ./cmd/joshbot
```

### Running the Application

```bash
./joshbot onboard          # First-time setup
./joshbot agent            # Interactive CLI mode
./joshbot gateway          # Telegram + all channels
./joshbot status           # Show config/status
```

## Building

```bash
# Build the binary
go build -o joshbot ./cmd/joshbot

# Install to $GOPATH/bin
go install ./cmd/joshbot
```

## Testing

```bash
# Run all tests
go test ./...

# Run tests for a specific package
go test ./internal/tools

# Run a specific test
go test ./internal/tools -run TestShell

# Run with verbose output
go test -v ./internal/agent

# Run with race detector
go test -race ./...
```

## Linting & Code Quality

```bash
# Format code
go fmt ./...

# Run static analysis
go vet ./...

# Clean up go.mod/go.sum
go mod tidy

# Optional: Advanced static analysis
go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck ./...
```

## Code Style

For detailed coding conventions and guidelines, see [AGENTS.md](./AGENTS.md). Key points:

- **Package structure**: Follow standard Go layout (`cmd/`, `internal/`, `pkg/`)
- **Import ordering**: Stdlib → third-party → local (separated by blank lines)
- **Error handling**: Return errors as values, wrap with context using `%w`
- **Testing**: Place tests in the same directory with `_test.go` suffix
- **Logging**: Use `charmbracelet/log` for structured logging

## Pull Request Process

1. **Create a feature branch**
   ```bash
   git checkout -b feature/your-feature-name
   # or
   git checkout -b fix/bug-description
   ```

2. **Write tests** for new functionality or bug fixes

3. **Update documentation** if your changes affect:
   - User-facing features
   - API or configuration
   - Build/development process

4. **Ensure tests pass**
   ```bash
   go test ./...
   go vet ./...
   go fmt ./...
   ```

5. **Submit a Pull Request**
   - Describe what changed and why
   - Reference any related issues
   - Include verification steps you performed

## Commit Message Guidelines

Write clear, concise commit messages that explain the "why" not just the "what":

```
feat: add web search tool for real-time information

Implements a new tool that allows the agent to perform web searches
and retrieve up-to-date information from search results.
```

### Common Types

- `feat:` - New feature
- `fix:` - Bug fix
- `refactor:` - Code refactoring (no behavior change)
- `docs:` - Documentation changes
- `test:` - Adding or updating tests
- `chore:` - Maintenance tasks

### Tips

- Use imperative mood ("add" not "added")
- Keep the first line under 72 characters
- Add details in the body if needed

## Directory Structure

```
joshbot/
├── cmd/joshbot/           # Main application entry point
├── internal/
│   ├── agent/             # Core ReAct agent loop
│   ├── bus/               # Message bus (goroutine-based)
│   ├── channels/          # Chat channels (CLI, Telegram)
│   ├── config/            # Configuration loading
│   ├── memory/            # MEMORY.md + HISTORY.md management
│   ├── providers/         # LLM provider integrations
│   ├── session/           # Session management
│   ├── skills/            # Skill discovery and loading
│   └── tools/             # Tool system and implementations
├── docs/                  # Documentation
├── workspace/             # User data (skills, memory)
└── tasks/                  # Task tracking
```

For more details on architecture and component interactions, see [AGENTS.md](./AGENTS.md).
