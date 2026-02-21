# Installation & Quick Start Guide

This guide will help you get joshbot up and running quickly. joshbot is a lightweight personal AI assistant with self-learning memory, skill self-creation, and Telegram integration.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Installation Methods](#installation-methods)
  - [Quick Install (Recommended)](#quick-install-recommended)
  - [From Source](#from-source)
  - [Using go install](#using-go-install)
  - [Docker](#docker)
- [First-Time Setup](#first-time-setup)
- [Basic Usage](#basic-usage)
  - [Interactive CLI Mode](#interactive-cli-mode)
  - [Gateway Mode](#gateway-mode)
  - [Check Status](#check-status)
- [Configuration](#configuration)
  - [Config File Location](#config-file-location)
  - [Environment Variables](#environment-variables)
  - [Key Settings](#key-settings)
- [Workspace Structure](#workspace-structure)
- [Troubleshooting](#troubleshooting)

---

## Prerequisites

Before installing joshbot, ensure you have the following:

### Required

- **Go 1.24+** - joshbot is written in Go and requires Go 1.24 or later
  ```bash
  # Check your Go version
  go version
  
  # Install Go (if needed)
  # macOS: brew install go
  # Ubuntu/Debian: sudo apt install golang-go
  # Or download from: https://go.dev/dl/
  ```

- **LLM Provider API Key** - joshbot needs an API key to connect to an LLM provider
  - **OpenRouter** (recommended for beginners): Free tier available at [openrouter.ai/keys](https://openrouter.ai/keys) - no credit card required
  - **Anthropic**: Get a key at [console.anthropic.com](https://console.anthropic.com)
  - **OpenAI**: Get a key at [platform.openai.com](https://platform.openai.com)
  - **Groq**: Free tier at [console.groq.com](https://console.groq.com)

### Optional

- **Telegram Bot Token** - Required for Telegram integration (see [Telegram Setup](#telegram-setup))
- **Docker** - For containerized deployment
- **Git** - For installing from source

---

## Installation Methods

### Quick Install (Recommended)

The fastest way to install joshbot is using the binary install script:

```bash
curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/scripts/install.sh | bash
```

This script will:
1. Download the latest pre-built binary for your platform
2. Verify the checksum
3. Install the binary to `/usr/local/bin` (requires sudo) or `~/.local/bin`

After installation, verify it works:

```bash
joshbot --version
```

### From Source

Building from source gives you the latest development version:

```bash
# Clone the repository
git clone https://github.com/bigknoxy/joshbot.git
cd joshbot

# Build the binary
go build -o joshbot ./cmd/joshbot

# Move to your PATH (optional)
sudo mv joshbot /usr/local/bin/

# Or install directly
go install ./cmd/joshbot
```

### Using go install

Install directly from GitHub:

```bash
go install github.com/bigknoxy/joshbot/cmd/joshbot@latest
```

The binary will be installed to `$GOPATH/bin` (usually `~/go/bin`). Make sure this directory is in your PATH:

```bash
# Add to your shell config (~/.bashrc, ~/.zshrc, etc.)
export PATH="$PATH:$(go env GOPATH)/bin"
```

### Docker

joshbot can run in Docker for isolated deployments:

```bash
# Build the image
docker build -t joshbot .

# Run first-time setup (interactive)
docker run -it -v ~/.joshbot:/root/.joshbot joshbot onboard

# Run gateway mode
docker run -d -v ~/.joshbot:/root/.joshbot joshbot gateway
```

#### Docker Compose

For easier management, use Docker Compose:

```bash
# Start joshbot
docker compose up -d

# View logs
docker compose logs -f

# Stop
docker compose down
```

The Docker Compose configuration persists your configuration, sessions, and memory in `~/.joshbot`.

---

## First-Time Setup

After installation, run the onboarding wizard:

```bash
joshbot onboard
```

The wizard will guide you through:

1. **LLM Provider Configuration**
   - Enter your OpenRouter API key (or skip to configure later)
   - Get a free key at: https://openrouter.ai/keys

2. **Personality Selection**
   ```
   Choose joshbot's personality:
     1. Professional - Concise, task-focused, minimal small talk
     2. Friendly - Warm, conversational, uses humor
     3. Sarcastic - Witty, dry humor, still helpful underneath
     4. Minimal - Extremely terse, just the facts
     5. Custom - Write your own SOUL.md
   ```

3. **Model Selection**
   - Default: `openai/gpt-4` (or use `arcee-ai/trinity-large-preview:free` for free via OpenRouter)
   - You can specify any model supported by your provider

### Onboarding Options

```bash
# Force fresh setup (backs up existing config)
joshbot onboard --force

# Reconfigure while keeping existing data
joshbot onboard --keep-data
```

After onboarding completes, you'll see:

```
╔═══════════════════════════════════════════╗
║           Setup complete!                  ║
╚═══════════════════════════════════════════╝

Config: ~/.joshbot/config.json
Workspace: ~/.joshbot/workspace

Quick start:
  joshbot agent    - Chat in the terminal
  joshbot gateway - Start Telegram + all channels
  joshbot status  - Check configuration
```

---

## Basic Usage

### Interactive CLI Mode

Start an interactive chat session in your terminal:

```bash
joshbot agent
```

```
joshbot agent mode. Type 'exit' to quit.
> Hello! What can you help me with?
> exit
```

This mode is ideal for:
- Quick questions and tasks
- Testing your configuration
- Development and debugging

### Gateway Mode

Start joshbot as a long-running service with all channels enabled:

```bash
joshbot gateway
```

```
╔═══════════════════════════════════════════╗
║         joshbot gateway running           ║
║  Model: openai/gpt-4                      ║
║  Telegram: disabled                        ║
║                                           ║
║  Press Ctrl+C to stop                     ║
╚═══════════════════════════════════════════╝
```

Gateway mode enables:
- Telegram bot integration
- Background task processing
- Heartbeat service for proactive tasks
- Scheduled reminders (cron)

### Check Status

View your current configuration and system status:

```bash
joshbot status
```

```
╔═══════════════════════════════════════════╗
║            joshbot status                 ║
╚═══════════════════════════════════════════╝
Version:        1.0.0
Config file:    ~/.joshbot/config.json (exists)
Workspace:      ~/.joshbot/workspace (exists)
Sessions:       ~/.joshbot/sessions

Model:          openai/gpt-4
Max tokens:     8192
Temperature:    0.7
Memory window:  50

Providers:      openrouter
Telegram:       disabled
Workspace restricted: disabled
```

### All Commands

| Command | Description |
|---------|-------------|
| `joshbot onboard` | First-time setup wizard |
| `joshbot agent` | Interactive CLI chat mode |
| `joshbot gateway` | Start all channels (Telegram, etc.) |
| `joshbot status` | Show configuration and status |
| `joshbot --version` | Show version |
| `joshbot --help` | Show help |

---

## Configuration

### Config File Location

joshbot stores all configuration and data in `~/.joshbot/`:

```
~/.joshbot/
├── config.json          # Main configuration file
├── sessions/            # Conversation history (JSONL)
├── media/               # Downloaded media files
├── cron/                # Scheduled tasks
└── workspace/           # Memory, skills, and context files
    ├── SOUL.md          # Personality definition
    ├── USER.md          # User profile
    ├── AGENTS.md        # Agent behavior instructions
    ├── IDENTITY.md      # Bot identity
    ├── HEARTBEAT.md     # Proactive tasks checklist
    ├── memory/
    │   ├── MEMORY.md    # Long-term memory
    │   └── HISTORY.md   # Event log
    └── skills/          # Custom skills
```

### Configuration File

The main config file is `~/.joshbot/config.json`:

```json
{
  "schema_version": 1,
  "providers": {
    "openrouter": {
      "api_key": "sk-or-v1-your-key-here",
      "api_base": "",
      "extra_headers": {}
    }
  },
  "agents": {
    "defaults": {
      "workspace": "~/.joshbot/workspace",
      "model": "openai/gpt-4",
      "max_tokens": 8192,
      "temperature": 0.7,
      "max_tool_iterations": 20,
      "memory_window": 50
    }
  },
  "channels": {
    "telegram": {
      "enabled": false,
      "token": "",
      "allow_from": [],
      "proxy": ""
    }
  },
  "tools": {
    "web": {
      "search": { "api_key": "" }
    },
    "exec": { "timeout": 60 },
    "restrict_to_workspace": false
  },
  "gateway": {
    "host": "0.0.0.0",
    "port": 18790
  },
  "log_level": "info"
}
```

### Environment Variables

All configuration values can be overridden with environment variables using the `JOSHBOT_` prefix. Use `__` (double underscore) for nested values:

```bash
# Provider configuration
export JOSHBOT_PROVIDERS__OPENROUTER__API_KEY="sk-or-v1-..."

# Agent settings
export JOSHBOT_AGENTS__DEFAULTS__MODEL="anthropic/claude-sonnet-4"
export JOSHBOT_AGENTS__DEFAULTS__MAX_TOKENS="16384"
export JOSHBOT_AGENTS__DEFAULTS__TEMPERATURE="0.5"

# Telegram configuration
export JOSHBOT_CHANNELS__TELEGRAM__ENABLED="true"
export JOSHBOT_CHANNELS__TELEGRAM__TOKEN="123456:ABC..."

# Tool settings
export JOSHBOT_TOOLS__RESTRICT_TO_WORKSPACE="true"
export JOSHBOT_TOOLS__EXEC__TIMEOUT="120"

# Logging
export JOSHBOT_LOG_LEVEL="debug"
```

### Key Settings

#### Model Selection

Choose an LLM model based on your needs:

| Use Case | Recommended Model | Notes |
|----------|-------------------|-------|
| Free tier | `arcee-ai/trinity-large-preview:free` | No cost via OpenRouter, good for testing |
| Better quality | `anthropic/claude-sonnet-4` | Requires Anthropic or OpenRouter credits |
| Fast responses | `groq/llama-3.3-70b-versatile` | Requires Groq API key |

To change the model:

```bash
# Via environment variable
export JOSHBOT_AGENTS__DEFAULTS__MODEL="anthropic/claude-sonnet-4"

# Or edit config.json
```

#### Multiple Providers

You can configure multiple providers:

```json
{
  "providers": {
    "openrouter": { "api_key": "sk-or-..." },
    "anthropic": { "api_key": "sk-ant-..." },
    "groq": { "api_key": "gsk_..." }
  }
}
```

#### Workspace Security

For sandboxed operation (recommended for production):

```json
{
  "tools": {
    "restrict_to_workspace": true
  }
}
```

This limits file and shell operations to the workspace directory only.

#### Telegram Setup

1. Create a bot via [@BotFather](https://t.me/BotFather) on Telegram
2. Get your bot token
3. Find your Telegram user ID via [@userinfobot](https://t.me/userinfobot)
4. Configure in `config.json`:

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "123456789:ABCdefGHIjklMNOpqrsTUVwxyz",
      "allow_from": ["123456789"]
    }
  }
}
```

> **Security Note:** `allow_from: []` (empty list) allows **anyone** to message your bot. Add your Telegram user ID (as a string) to restrict access.

---

## Workspace Structure

The workspace directory (`~/.joshbot/workspace/`) contains all your bot's knowledge and context:

```
workspace/
├── SOUL.md              # Bot personality and behavior
├── USER.md              # Your profile and preferences  
├── AGENTS.md            # Instructions for the agent
├── IDENTITY.md          # Bot's self-concept
├── HEARTBEAT.md         # Proactive task checklist
├── memory/
│   ├── MEMORY.md        # Long-term facts (always in context)
│   └── HISTORY.md       # Searchable event log
└── skills/              # Custom skills (auto-discovered)
    └── my-skill/
        └── SKILL.md
```

### Key Files Explained

| File | Purpose |
|------|---------|
| `SOUL.md` | Defines the bot's personality, communication style, and values |
| `USER.md` | Your profile, preferences, and current projects |
| `MEMORY.md` | Important facts the bot remembers across conversations |
| `HISTORY.md` | Timestamped log of past conversations (grep-searchable) |
| `HEARTBEAT.md` | Tasks for autonomous processing (checked every 30 min) |

### Memory System

joshbot uses a two-file memory system:

1. **MEMORY.md** - Always loaded in context
   - User preferences and facts
   - Project context and decisions
   - Important notes

2. **HISTORY.md** - Searchable event log
   - Timestamped conversation summaries
   - Searched via `grep` when needed
   - Grows over time

When conversations exceed the memory window (default: 50 messages), joshbot:
1. Summarizes older messages
2. Extracts key facts to MEMORY.md
3. Appends a summary to HISTORY.md
4. Trims the session to recent messages

---

## Troubleshooting

### Common Issues

#### "No providers configured"

**Problem:** joshbot can't find your API key.

**Solution:**
```bash
# Run onboarding
joshbot onboard

# Or manually create config
mkdir -p ~/.joshbot
echo '{"providers":{"openrouter":{"api_key":"sk-or-..."}}}' > ~/.joshbot/config.json
```

#### LLM calls failing

**Problem:** API returns errors.

**Solutions:**
1. Verify your API key is valid
2. Check the model name matches your provider:
   - OpenRouter: `provider/model-name` (e.g., `anthropic/claude-sonnet-4`)
   - Anthropic direct: `claude-sonnet-4`
   - OpenAI direct: `gpt-4o`
3. Ensure you have credits (for paid models)

```bash
# Check your configuration
joshbot status
```

#### Telegram bot not responding

**Problem:** Messages aren't being processed.

**Solutions:**
1. Verify `channels.telegram.enabled` is `true`
2. Check your bot token is correct
3. Ensure your user ID is in `allow_from` (or the list is empty)
4. If behind a firewall, configure the `proxy` field

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "correct-token-here",
      "allow_from": ["your-user-id"]
    }
  }
}
```

#### "go: command not found"

**Problem:** Go is not installed or not in PATH.

**Solution:**
```bash
# Install Go
# macOS
brew install go

# Ubuntu/Debian
sudo apt update && sudo apt install golang-go

# Add to PATH (add to ~/.bashrc or ~/.zshrc)
export PATH=$PATH:$(go env GOPATH)/bin
```

#### Build errors

**Problem:** `go build` fails with errors.

**Solutions:**
1. Ensure Go 1.24+ is installed:
   ```bash
   go version
   ```

2. Update dependencies:
   ```bash
   go mod download
   go mod tidy
   ```

3. Clean build cache:
   ```bash
   go clean -cache
   go build ./cmd/joshbot
   ```

#### Permission denied

**Problem:** Can't write to `~/.joshbot/`.

**Solution:**
```bash
# Fix permissions
chmod -R 755 ~/.joshbot

# Or recreate
rm -rf ~/.joshbot
joshbot onboard
```

### Getting Help

1. **Check status:** `joshbot status`
2. **Enable debug logging:** `joshbot --verbose agent`
3. **Review logs:** Check console output for errors
4. **File an issue:** [github.com/bigknoxy/joshbot/issues](https://github.com/bigknoxy/joshbot/issues)

### Uninstalling

```bash
# Remove the binary
joshbot uninstall

# Or manually
rm $(which joshbot)
rm -rf ~/.joshbot  # Also removes config, memory, sessions
```

---

## Next Steps

- **Explore tools:** joshbot can read/write files, run shell commands, search the web, and more
- **Create skills:** Teach joshbot new capabilities by creating skill files
- **Set up Telegram:** Chat with your bot from your phone
- **Configure heartbeat:** Set up proactive tasks for autonomous processing

For more details, see the [README.md](../README.md) or explore the `skills/` directory for examples.
