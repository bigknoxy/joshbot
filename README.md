# joshbot

A lightweight personal AI assistant with self-learning, long-term memory, skill creation, and Telegram integration. Inspired by [nanobot](https://github.com/HKUDS/nanobot).

## Features

- **Self-Learning Memory** - Automatically remembers important facts across conversations
- **Long-Term History** - Searchable event log of past conversations
- **Skill Self-Creation** - Creates new capabilities for itself as markdown files
- **Telegram Integration** - Chat from your phone with full media support
- **Interactive CLI** - Rich terminal interface with markdown rendering
- **Multi-Provider LLM** - OpenRouter, Anthropic, OpenAI, Groq, DeepSeek, Gemini, and more via litellm
- **Tool Use** - File operations, shell commands, web search, scheduling, and more
- **Proactive Tasks** - Heartbeat system for autonomous task processing
- **Scheduled Reminders** - Cron-based task scheduling with natural delay syntax

## Requirements

- **Python 3.11+** (required — uses modern syntax like `str | None`)
- **An LLM API key** — OpenRouter free tier works, no credit card needed
- **Linux or macOS** recommended (Windows may require extra setup for `readability-lxml` C dependencies)

> **Note:** On Debian/Ubuntu, you may need system libraries for lxml: `sudo apt install libxml2-dev libxslt-dev`

## Quick Start

### Quick Install (Recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash
```

This installs joshbot via [pipx](https://pipx.pypa.io/) in an isolated environment. Requires Python 3.11+.

After install, run:

```bash
joshbot onboard
```

### Manual Install

```bash
git clone https://github.com/bigknoxy/joshbot.git
cd joshbot

# Create a virtual environment (recommended)
python3 -m venv .venv
source .venv/bin/activate  # On Windows: .venv\Scripts\activate

# Install
pip install .

# Or install in dev mode
pip install -e .
```

### 2. Onboard

```bash
joshbot onboard
```

This will:
- Ask for your OpenRouter API key (free at [openrouter.ai/keys](https://openrouter.ai/keys)) — you can press Enter to skip and configure later
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
      "api_key": "sk-or-v1-your-key-here",
      "api_base": "",
      "extra_headers": {}
    }
  },
  "agents": {
    "defaults": {
      "workspace": "~/.joshbot/workspace",
      "model": "google/gemma-2-9b-it:free",
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
  }
}
```

> **Security note:** `allow_from: []` (empty) means **anyone** can message your bot. Add your Telegram user ID to restrict access. `restrict_to_workspace: false` means tools can access files outside the workspace — set to `true` for sandboxed operation.

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

**To use Groq (fast inference):**
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

**To use a custom OpenAI-compatible endpoint (e.g., local vLLM, Ollama):**
```json
{
  "providers": {
    "custom": {
      "api_key": "your-key-or-empty",
      "api_base": "http://localhost:8000/v1"
    }
  },
  "agents": {
    "defaults": { "model": "openai/your-model-name" }
  }
}
```

### Voice Transcription Setup

Voice messages from Telegram are transcribed using Groq's Whisper API. To enable this, add a Groq provider **alongside** your main LLM provider:

```json
{
  "providers": {
    "openrouter": { "api_key": "sk-or-..." },
    "groq": { "api_key": "gsk_..." }
  }
}
```

Get a free Groq API key at [console.groq.com](https://console.groq.com). Without this, voice messages are saved as files but not transcribed.

### Proxy Configuration

If you're behind a firewall or in a restricted region, configure a proxy for Telegram:

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "...",
      "proxy": "socks5://user:pass@proxy-host:1080"
    }
  }
}
```

Supports HTTP and SOCKS5 proxies.

### Environment Variables

All config values can be set via environment variables with `JOSHBOT_` prefix and `__` for nesting:

```bash
export JOSHBOT_PROVIDERS__OPENROUTER__API_KEY="sk-or-..."
export JOSHBOT_CHANNELS__TELEGRAM__ENABLED="true"
export JOSHBOT_CHANNELS__TELEGRAM__TOKEN="123456:ABC..."
```

## Telegram Setup

1. Open Telegram and message [@BotFather](https://t.me/BotFather)
2. Send `/newbot` and follow the prompts to create your bot
3. Copy the bot token
4. Find your Telegram user ID by messaging [@userinfobot](https://t.me/userinfobot) — it returns a **number** like `123456789`
5. Add to config:

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

> **Important:** `allow_from` takes Telegram **user IDs** (numbers as strings), not usernames. An empty list allows anyone to use your bot.

6. Start the gateway: `joshbot gateway`
7. Message your bot on Telegram!

**Supported media:**
- Text messages
- Photos (downloaded and available as attachments)
- Voice messages (transcribed via Groq Whisper — see [Voice Transcription Setup](#voice-transcription-setup))
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
- Location: `~/.joshbot/workspace/memory/MEMORY.md` (default)
- **Always loaded** into context
- Contains: user preferences, project context, decisions, important notes
- Updated automatically during memory consolidation

### HISTORY.md (Event Log)
- Location: `~/.joshbot/workspace/memory/HISTORY.md` (default)
- Append-only log of timestamped conversation summaries
- Searchable (joshbot uses `grep` to search past events)
- Each entry is 2-5 sentences with `[YYYY-MM-DD HH:MM]` timestamp

### Memory Consolidation
When a conversation exceeds the memory window (default: 50 messages):
1. Older messages are summarized by the LLM
2. Key facts are extracted to MEMORY.md
3. A summary is appended to HISTORY.md
4. The session is trimmed to recent messages

### Heartbeat (Proactive Tasks)

The heartbeat service (active in gateway mode) reads `~/.joshbot/workspace/HEARTBEAT.md` every 30 minutes. Add tasks in checkbox format and joshbot will process them autonomously:

```markdown
- [ ] Check if the server is still running
- [ ] Summarize today's news about AI
```

## Skills System

Skills are markdown files that extend joshbot's capabilities without code changes.

### Bundled Skills
| Skill | Description |
|-------|-------------|
| `memory` | Memory system usage (always loaded into context) |
| `skill-creator` | How to create new skills |
| `github` | GitHub CLI patterns (requires `gh` binary) |
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

The `requirements` field supports `bin:name` (checks for binary in PATH) and `env:VAR` (checks for environment variable). Skills with unmet requirements are listed as unavailable.

## Built-in Tools

| Tool | Description |
|------|-------------|
| `read_file` | Read file contents |
| `write_file` | Write/create files |
| `edit_file` | Find-and-replace editing |
| `list_dir` | List directory contents |
| `exec` | Execute shell commands (with safety guards) |
| `web_search` | Search the web (requires [Brave API key](https://brave.com/search/api/)) |
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

# First-time setup (interactive)
docker run -it -v ~/.joshbot:/root/.joshbot joshbot onboard

# Run gateway
docker run -d -v ~/.joshbot:/root/.joshbot joshbot gateway
```

### Docker Compose

```bash
# Configure ~/.joshbot/config.json first, then:
docker compose up -d
```

The `-v` mount persists config, sessions, memory, and skills across container restarts. You can also pass API keys via environment variables in `docker-compose.yml` — see the comments in that file.

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
- **Heartbeat**: Reads HEARTBEAT.md every 30 minutes for proactive autonomous tasks

## Upgrading

```bash
pipx upgrade joshbot --pip-args='--force-reinstall'
```

Or reinstall:

```bash
pipx uninstall joshbot && pipx install "joshbot @ git+https://github.com/bigknoxy/joshbot.git"
```

If you installed from source:

```bash
cd joshbot
git pull
pip install .
```

Your config, sessions, and memory in `~/.joshbot/` are preserved across upgrades.

## Uninstall

### Quick Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/uninstall.sh | bash
```

This removes joshbot and optionally cleans up your data directory.

### Manual Uninstall

```bash
# Remove the package
pip uninstall joshbot

# Or if installed with pipx:
pipx uninstall joshbot

# Remove all data (config, sessions, memory, media)
rm -rf ~/.joshbot

# If you set environment variables, remove them from your shell profile:
# unset JOSHBOT_PROVIDERS__OPENROUTER__API_KEY
# etc.
```

## Troubleshooting

**"No providers configured"** — Run `joshbot onboard` or manually create `~/.joshbot/config.json` with at least one provider and API key.

**LLM calls failing** — Check your API key is valid. Run `joshbot status` to verify configuration. Ensure your model name matches the provider (e.g., `claude-sonnet-4-20250514` for Anthropic, `openrouter/...` prefix for OpenRouter).

**Telegram bot not responding** — Verify `channels.telegram.enabled` is `true` and the token is correct. Check that your user ID is in `allow_from` (or that the list is empty). If behind a firewall, configure the `proxy` field.

**`readability-lxml` install fails** — Install system dependencies: `sudo apt install libxml2-dev libxslt-dev` (Debian/Ubuntu) or `brew install libxml2 libxslt` (macOS).

**`web_search` returns errors** — This tool requires a Brave Search API key. Get one at [brave.com/search/api](https://brave.com/search/api/) and set it in `tools.web.search.api_key`.

## License

MIT — see [LICENSE](LICENSE).
