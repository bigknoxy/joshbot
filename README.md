# joshbot

A lightweight personal AI assistant with self-learning, long-term memory, skill creation, and Telegram integration. Inspired by [nanobot](https://github.com/HKUDS/nanobot).

## Features

- **Self-Learning Memory** - Automatically remembers important facts across conversations
- **Long-Term History** - Searchable event log of past conversations
- **Skill Self-Creation** - Creates new capabilities for itself as markdown files
- **Telegram Integration** - Chat from your phone with full media support
- **Interactive CLI** - Rich terminal interface with markdown rendering
Provider LLM**- **Multi- - OpenRouter, Anthropic, OpenAI, Groq, DeepSeek, and more via litellm
- **Tool Use** - File operations, shell commands, web search, scheduling, and more
- **Proactive Tasks** - Heartbeat system for autonomous task processing
- **Scheduled Reminders** - Cron-based task scheduling with natural delay syntax

## Quick Start

### 1. Install

```bash
# From source
git clone https://github.com/yourusername/joshbot.git
cd joshbot
pip install .

# Or install in dev mode
pip install -e .
```

### 2. Onboard

```bash
joshbot onboard
```

This will:
- Ask for your OpenRouter API key (free at [openrouter.ai/keys](https://openrouter.ai/keys))
- Let you choose a personality (Professional, Friendly, Sarcastic, Minimal, or Custom)
- Set up your workspace and memory files

### 3. Chat

```bash
# Interactive terminal
joshbot agent

# Gateway mode (Telegram + all channels)
joshbot gateway
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
      "model": "google/gemma-2-9b-it:free",
      "max_tokens": 8192,
      "temperature": 0.7,
      "memory_window": 50
    }
  },
  "channels": {
    "telegram": {
      "enabled": false,
      "token": "",
      "allow_from": []
    }
  },
  "tools": {
    "web": {
      "search": { "api_key": "" }
    },
    "exec": { "timeout": 60 },
    "restrict_to_workspace": false
  }
}
```

### Changing the LLM Model

The default model is `google/gemma-2-9b-it:free` via OpenRouter (free, no credit card needed).

**To use a better model** (requires OpenRouter credits):
```json
"model": "anthropic/claude-sonnet-4-20250514"
```

**To use Anthropic directly:**
```json
{
  "providers": {
    "anthropic": { "api_key": "sk-ant-..." }
  },
  "agents": {
    "defaults": { "model": "claude-sonnet-4-20250514" }
  }
}
```

**To use OpenAI directly:**
```json
{
  "providers": {
    "openai": { "api_key": "sk-..." }
  },
  "agents": {
    "defaults": { "model": "gpt-4o" }
  }
}
```

**To use Groq (fast + voice transcription):**
```json
{
  "providers": {
    "groq": { "api_key": "gsk_..." }
  },
  "agents": {
    "defaults": { "model": "groq/llama-3.3-70b-versatile" }
  }
}
```

### Environment Variables

All config values can be set via environment variables with `JOSHBOT_` prefix:

```bash
export JOSHBOT_PROVIDERS__OPENROUTER__API_KEY="sk-or-..."
export JOSHBOT_CHANNELS__TELEGRAM__ENABLED="true"
export JOSHBOT_CHANNELS__TELEGRAM__TOKEN="123456:ABC..."
```

## Telegram Setup

1. Open Telegram and message [@BotFather](https://t.me/BotFather)
2. Send `/newbot` and follow the prompts to create your bot
3. Copy the bot token
4. Add to config:

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "123456789:ABCdefGHIjklMNOpqrsTUVwxyz",
      "allow_from": ["your_telegram_user_id"]
    }
  }
}
```

5. Start the gateway: `joshbot gateway`
6. Message your bot on Telegram!

**Finding your Telegram user ID:** Message [@userinfobot](https://t.me/userinfobot) on Telegram.

**Supported media:**
- Text messages
- Photos (downloaded and available as attachments)
- Voice messages (transcribed via Groq Whisper if configured)
- Documents (downloaded and referenced)

## Commands

### CLI

```bash
joshbot onboard   # First-time setup
joshbot agent     # Interactive CLI chat
joshbot gateway   # Start all channels (Telegram, etc.)
joshbot status    # Show configuration and status
```

### Chat Commands

These work in both CLI and Telegram:

| Command | Description |
|---------|-------------|
| `/start` | Start a conversation |
| `/new` | Start fresh (saves memory first) |
| `/help` | Show available commands |
| `/status` | Show system status |

## Memory System

joshbot has a two-file memory system that learns from your conversations:

### MEMORY.md (Long-Term Facts)
- Location: `~/.joshbot/workspace/memory/MEMORY.md`
- **Always loaded** into context
- Contains: user preferences, project context, decisions, important notes
- Updated automatically during memory consolidation

### HISTORY.md (Event Log)
- Location: `~/.joshbot/workspace/memory/HISTORY.md`
- Append-only log of timestamped conversation summaries
- Searchable (joshbot uses `grep` to search past events)
- Each entry is 2-5 sentences with `[YYYY-MM-DD HH:MM]` timestamp

### Memory Consolidation
When a conversation exceeds the memory window (default: 50 messages):
1. Older messages are summarized by the LLM
2. Key facts are extracted to MEMORY.md
3. A summary is appended to HISTORY.md
4. The session is trimmed to recent messages

## Skills System

Skills are markdown files that extend joshbot's capabilities without code changes.

### Bundled Skills
| Skill | Description |
|-------|-------------|
| `memory` | Memory system usage (always loaded) |
| `skill-creator` | How to create new skills |
| `github` | GitHub CLI patterns |
| `cron` | Scheduling guidance |

### Creating Custom Skills

joshbot can create its own skills! Ask it to learn something, and it will:

1. Create `~/.joshbot/workspace/skills/{name}/SKILL.md`
2. Write instructions with YAML frontmatter
3. Auto-discover the skill in future conversations

Skills use **progressive loading**:
- **Level 1:** Name + description always in context (~100 tokens each)
- **Level 2:** Full content loaded on demand via `read_file`
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

## Built-in Tools

| Tool | Description |
|------|-------------|
| `read_file` | Read file contents |
| `write_file` | Write/create files |
| `edit_file` | Find-and-replace editing |
| `list_dir` | List directory contents |
| `exec` | Execute shell commands (with safety guards) |
| `web_search` | Search the web (Brave API) |
| `web_fetch` | Fetch and extract web page content |
| `message` | Send messages to channels |
| `spawn` | Create background tasks |
| `cron` | Schedule reminders/tasks |

### Shell Safety

The `exec` tool blocks dangerous commands including:
- `rm -rf /`, `dd`, `mkfs` (destructive operations)
- `shutdown`, `reboot`, `halt` (system commands)
- Fork bombs and other malicious patterns
- Optional workspace sandboxing via `restrict_to_workspace: true`

## Docker

### Build and Run

```bash
docker build -t joshbot .
docker run -v ~/.joshbot:/root/.joshbot joshbot gateway
```

### Docker Compose

```bash
docker compose up -d
```

The `-v` mount persists config, sessions, memory, and skills across container restarts.

## Architecture

```
joshbot/
├── agent/          # Core brain (loop, context, memory, skills)
├── tools/          # Built-in tools (filesystem, shell, web, etc.)
├── channels/       # Chat integrations (CLI, Telegram)
├── bus/            # Async message bus (decouples channels from agent)
├── providers/      # LLM provider layer (litellm + registry)
├── session/        # Conversation persistence (JSONL)
├── cron/           # Task scheduling
├── heartbeat/      # Proactive wake-ups
└── config/         # Configuration (Pydantic)
```

**Key patterns:**
- **Message bus**: Async queues decouple channels from agent logic
- **ReAct loop**: LLM -> tools -> reflect -> repeat (max 20 iterations)
- **Progressive skill loading**: Minimal context overhead, full content on demand
- **Plain-file memory**: No databases, just markdown. Simple, debuggable, portable.

## Requirements

- Python 3.11+
- An LLM API key (OpenRouter free tier works)

## License

MIT
