# joshbot

[![Go Report Card](https://goreportcard.com/badge/github.com/bigknoxy/joshbot)](https://goreportcard.com/report/github.com/bigknoxy/joshbot)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/dl/)
[![GitHub release](https://img.shields.io/github/v/release/bigknoxy/joshbot?include_prereleases)](https://github.com/bigknoxy/joshbot/releases/latest)

A lightweight personal AI assistant written in Go, featuring self-learning memory, skill self-creation, subagent delegation, and Telegram integration. Inspired by [nanobot](https://github.com/HKUDS/nanobot).

## Features

- **Self-Learning Memory** - Automatically remembers important facts across conversations (MEMORY.md + HISTORY.md)
- **Context Compression** - Summarizes old context to stay within token limits; works well with small local models
- **Skill Self-Creation** - Creates new capabilities for itself as markdown files
- **Subagent Delegation** - Spawns focused subagents for complex multi-step tasks
- **Telegram Integration** - Chat from your phone with full media support
- **Interactive CLI** - Rich terminal interface with markdown rendering
- **Multi-Provider LLM** - OpenRouter, Anthropic, OpenAI, Groq, DeepSeek, Gemini, and more
- **Tool Use** - File operations, shell commands, web search, scheduling, and more
- **Proactive Tasks** - Heartbeat system for autonomous task processing
- **Scheduled Reminders** - Cron-based task scheduling with natural delay syntax

## Requirements

- **Go 1.24+** (for building from source)
- **An LLM API key** — OpenRouter free tier works, no credit card needed
- **Linux or macOS** recommended

## Quick Start

### One-Line Install (Recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash
```

Downloads the latest binary release for your platform. Supports Linux and macOS (amd64/arm64).

For specific versions:
```bash
curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash -s -- -v v1.0.0
```

### Install with Go

```bash
go install github.com/bigknoxy/joshbot/cmd/joshbot@latest
```

Ensure `$GOPATH/bin` or `$HOME/go/bin` is in your PATH.

### Build from Source

```bash
git clone https://github.com/bigknoxy/joshbot.git
cd joshbot
go build -o joshbot ./cmd/joshbot
```

### Docker

```bash
docker build -t joshbot .
docker run -it -v ~/.joshbot:/root/.joshbot joshbot onboard
```

## Usage

```bash
joshbot onboard     # First-time setup
joshbot agent       # Interactive CLI chat
joshbot gateway     # Start all channels (Telegram, etc.)
joshbot status      # Show configuration and status
joshbot uninstall   # Remove joshbot binary and config
```

### Onboard Command

```bash
joshbot onboard              # Interactive setup
joshbot onboard --force      # Overwrite existing config
joshbot onboard --keep-data  # Reconfigure but preserve memory/skills
```

The onboard flow will:
- Ask for your OpenRouter API key (free at [openrouter.ai/keys](https://openrouter.ai/keys))
- Let you choose a personality (Professional, Friendly, Sarcastic, Minimal, or Custom)
- Set up your workspace and memory files

## Memory System

joshbot uses a two-file memory system that learns from your conversations:

| File | Purpose |
|------|---------|
| `MEMORY.md` | Long-term facts (always in context) |
| `HISTORY.md` | Searchable event log with timestamps |

### Memory Consolidation

When conversations grow large:
1. Old messages are summarized by the LLM
2. Key facts are extracted to MEMORY.md
3. A summary is appended to HISTORY.md
4. Context is compressed to stay within limits

**Context Compression** works efficiently with small local models (e.g., `gemma-2-9b`, `llama-3.2-3b`) — the summarization task is simple enough that you don't need a large model.

## Skills System

Skills are markdown files that extend joshbot's capabilities without code changes.

### Bundled Skills

| Skill | Description |
|-------|-------------|
| `memory` | Memory system usage (always loaded) |
| `skill-creator` | How to create new skills |
| `github` | GitHub CLI patterns (requires `gh` binary) |
| `cron` | Scheduling guidance |

### Creating Custom Skills

joshbot can create its own skills! Ask it to learn something, and it will create `~/.joshbot/workspace/skills/{name}/SKILL.md` with YAML frontmatter.

Skills use **progressive loading**:
- **Level 1:** Name + description always in context (~100 tokens)
- **Level 2:** Full content loaded on demand
- **Level 3:** Scripts/assets loaded as needed

### Skill Format

```yaml
---
name: my-skill
description: "What this skill does"
always: false
requirements: [bin:git, env:GITHUB_TOKEN]
tags: [development]
---

# My Skill

Instructions and examples...
```

## Subagent Delegation

For complex tasks, joshbot can spawn focused subagents that:
- Keep the main context clean
- Handle one specific objective
- Report back with results

Subagents are useful for:
- File exploration and pattern discovery
- Multi-step implementation tasks
- Parallel independent work

## Heartbeat (Proactive Tasks)

The heartbeat service (active in gateway mode) reads `~/.joshbot/workspace/HEARTBEAT.md` periodically. Add tasks in checkbox format:

```markdown
- [ ] Check if the server is still running
- [ ] Summarize today's news about AI
```

## Configuration

Config file: `~/.joshbot/config.json`

```json
{
  "providers": {
    "openrouter": {
      "api_key": "sk-or-v1-your-key-here"
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
      "allow_from": []
    }
  }
}
```

### Environment Variables

All config values can be set via environment variables with `JOSHBOT_` prefix:

```bash
export JOSHBOT_PROVIDERS__OPENROUTER__API_KEY="sk-or-..."
export JOSHBOT_CHANNELS__TELEGRAM__ENABLED="true"
```

### Changing the LLM Model

The default model is `openai/gpt-4`. For free alternatives via OpenRouter, try `arcee-ai/trinity-large-preview:free` or browse available models at openrouter.ai.

**To use Anthropic directly:**
```json
{
  "providers": { "anthropic": { "api_key": "sk-ant-..." } },
  "agents": { "defaults": { "model": "claude-sonnet-4-20250514" } }
}
```

**To use OpenAI directly:**
```json
{
  "providers": { "openai": { "api_key": "sk-..." } },
  "agents": { "defaults": { "model": "gpt-4o" } }
}
```

**To use a local model (Ollama, vLLM):**
```json
{
  "providers": {
    "custom": {
      "api_key": "",
      "api_base": "http://localhost:11434/v1"
    }
  },
  "agents": { "defaults": { "model": "openai/llama3.2" } }
}
```

## Telegram Setup

1. Message [@BotFather](https://t.me/BotFather) and send `/newbot` to create your bot
2. Copy the bot token
3. Find your user ID from [@userinfobot](https://t.me/userinfobot)
4. Add to config:

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "123456789:ABCdef...",
      "allow_from": ["123456789"]
    }
  }
}
```

5. Run: `joshbot gateway`

## Built-in Tools

| Tool | Description |
|------|-------------|
| `read_file` | Read file contents |
| `write_file` | Write/create files |
| `edit_file` | Find-and-replace editing |
| `list_dir` | List directory contents |
| `exec` | Execute shell commands (with safety guards) |
| `web_search` | Search the web (requires Brave API key) |
| `web_fetch` | Fetch and extract web page content |
| `message` | Send messages to channels |
| `spawn` | Create background tasks |
| `cron` | Schedule reminders/tasks |

## Chat Commands

| Command | Description |
|---------|-------------|
| `/start` | Start a conversation |
| `/new` | Start fresh (saves memory first) |
| `/help` | Show available commands |
| `/status` | Show system status |

## Architecture

```
joshbot/
├── cmd/joshbot/     # CLI entry point
├── internal/
│   ├── agent/       # Core brain (loop, context, memory, skills)
│   ├── tools/       # Built-in tools
│   ├── channels/    # Chat integrations (CLI, Telegram)
│   ├── bus/         # Message bus (decouples channels from agent)
│   ├── providers/   # LLM provider layer
│   ├── session/     # Conversation persistence (JSONL)
│   ├── cron/        # Task scheduling
│   └── heartbeat/   # Proactive wake-ups
└── config/          # Configuration
```

**Key patterns:**
- **Message bus**: Channels decoupled from agent via async queues
- **ReAct loop**: LLM → tools → reflect → repeat (max 20 iterations)
- **Progressive skill loading**: Minimal context overhead, full content on demand
- **Plain-file memory**: No databases, just markdown — simple and portable
- **Context compression**: Summarizes old context to stay within token limits

## Troubleshooting

**"No providers configured"** — Run `joshbot onboard` or create `~/.joshbot/config.json` with at least one provider.

**LLM calls failing** — Check your API key. Run `joshbot status` to verify configuration.

**Telegram bot not responding** — Verify `channels.telegram.enabled` is `true` and check your user ID is in `allow_from`.

## License

MIT — see [LICENSE](LICENSE).
