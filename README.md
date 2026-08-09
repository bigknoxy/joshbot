# joshbot

[![CI](https://github.com/bigknoxy/joshbot/actions/workflows/ci.yml/badge.svg)](https://github.com/bigknoxy/joshbot/actions/workflows/ci.yml)
[![Coverage](docs/coverage-badge.svg)](https://github.com/bigknoxy/joshbot/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/bigknoxy/joshbot.svg)](https://pkg.go.dev/github.com/bigknoxy/joshbot)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/dl/)
[![GitHub release](https://img.shields.io/github/v/release/bigknoxy/joshbot?include_prereleases)](https://github.com/bigknoxy/joshbot/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A lightweight personal AI assistant written in Go, featuring self-learning memory, skill self-creation, subagent delegation, and Telegram integration. Inspired by [nanobot](https://github.com/HKUDS/nanobot).

## Features

- **Self-Learning Memory** - Automatically remembers important facts across conversations using a structured fact system (categorized with SHA256-based IDs, confidence scoring, source tracking, and deduplication)
- **Context Compression** - Summarizes old context to stay within token limits; works well with small local models
- **Skill Self-Creation** - Creates new capabilities for itself as markdown files, with auto-detection from conversation patterns and LLM-based extraction
- **Subagent Delegation** - Spawns focused subagents for complex multi-step tasks
- **Telegram Integration** - Chat from your phone with full media support
- **Interactive CLI** - Rich terminal interface with markdown rendering
- **Multi-Provider LLM** - OpenRouter, Anthropic, OpenAI, Groq, Poolside, DeepSeek, Gemini, NVIDIA, and more
- **Model-Centric Config** - Simplified model configuration with provider auto-detection and fallback chains
- **Prompt Caching** - Intelligent caching of system prompts with mtime-based invalidation for faster responses
- **Tool Use** - File operations, shell commands, web search, scheduling, and more
- **Proactive Tasks** - Heartbeat system for autonomous task processing
- **Scheduled Reminders** - Ask for a reminder in `30m`, `2h` or `1d`, one-off or repeating; jobs persist across restarts

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
docker run -it -v ~/.joshbot:/home/joshbot/.joshbot joshbot onboard
```

## Usage

```bash
joshbot onboard # First-time setup
joshbot agent # Interactive CLI chat
joshbot agent --debug # CLI chat with debug logging
joshbot gateway # Start all channels (Telegram, etc.)
joshbot gateway --debug # Gateway with debug logging
joshbot status # Show configuration and status
joshbot skills list # Review workspace skills and approval state
joshbot skills trust <name> # Approve a workspace skill after reviewing it
joshbot configure # Configure LLM providers and settings
joshbot auth github-copilot # Authenticate with GitHub Copilot
joshbot service install # Install joshbot as a system service
joshbot update # Update to the latest release
joshbot uninstall # Remove joshbot binary and config
```

### Interactive CLI progress indicators

When `joshbot agent` runs interactively in a real terminal, it now shows
what's happening while it works instead of going silent for the length of
the ReAct loop:

- A single-line elapsed-time spinner is shown while waiting on the model
  ("thinking...").
- Each tool call the agent makes is announced, and its completion is shown
  with elapsed time, e.g.:

  ```
  ⏺ shell(go test ./...)
  ⎿ ok (1.2s)
  ```

This is purely cosmetic and terminal-aware: it is disabled automatically
when stdout is not a TTY (piped output, `joshbot agent -m "..."`,
`scripts/verify-local.sh`, etc.), so scripted and non-interactive usage stays
clean and parseable — no spinner, no ANSI codes, no progress lines.

### Debug Mode

Use `--debug` flag to enable detailed logging for troubleshooting:

```bash
# Debug mode for agent
joshbot agent --debug

# Debug mode for gateway
joshbot gateway --debug
```

Debug mode outputs detailed information about:
- LLM request/response details (model, content length, tool calls)
- HTTP response status codes
- Tool execution results
- Empty response detection and fallback behavior

This is especially useful when troubleshooting why joshbot returns "I've processed your request." instead of actual responses.

### Onboard Command

```bash
joshbot onboard              # Interactive setup
joshbot onboard --force      # Overwrite existing config
joshbot onboard --keep-data  # Reconfigure but preserve memory/skills
```

The onboard flow will:
- Ask for your LLM API key (defaults to NVIDIA NIM; OpenRouter free tier also supported at [openrouter.ai/keys](https://openrouter.ai/keys))
- Let you choose a personality (Professional, Friendly, Sarcastic, Minimal, or Custom)
- Set up your workspace and memory files

## Memory System

joshbot uses a structured fact-based memory system that learns from your conversations:

| File | Purpose |
|------|---------|
| `MEMORY.md` | Long-term structured facts (always in context) |
| `HISTORY.md` | Searchable event log with timestamps |

Facts use a structured format with SHA256-based IDs, categorized by type (`user_info`, `preference`, `project`, `decision`, `skill`, `system`), with confidence scoring (0.0-1.0) and source tracking. The `memory_search` tool enables keyword + category + tag search with relevance scoring.

### Memory Consolidation

When conversations grow large:
1. Old messages are summarized by the LLM
2. Key facts are extracted as structured facts to MEMORY.md (with reconciliation to avoid duplicates)
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
| `cron` | Scheduling guidance for the `cron` tool |

### Creating Custom Skills

joshbot can create its own skills! Ask it to learn something, and it will create `~/.joshbot/workspace/skills/{name}/SKILL.md` with YAML frontmatter.

A workspace skill becomes part of the agent's standing instructions, so it is **inert until an operator approves it** — including skills the agent creates for itself. Approval is bound to the file's SHA-256, so editing an approved skill revokes it until it's approved again. Bundled skills (the ones listed above) are exempt.

```bash
joshbot skills list          # See what's pending
joshbot skills trust <name>  # Approve after reviewing the file
joshbot skills trust --all   # Approve every pending skill
joshbot skills untrust <name> # Revoke approval
```

`joshbot status` also flags any skills awaiting review.

### Managing sessions

A session is one conversation, keyed `channel:senderID` and stored as JSONL under
`~/.joshbot/sessions`. There is exactly one per user per channel and it is loaded
automatically on every message, so there is nothing to "resume" — what these
commands give you is a way to see what exists, read one back, and clear one.

```bash
joshbot sessions list                    # ID, message count, size, age, notes
joshbot sessions show <id>               # print the conversation (redacted)
joshbot sessions show <id> --last 20     # just the tail
joshbot sessions prune <id>              # delete one conversation
joshbot sessions prune --older-than 30d  # delete everything untouched for 30 days
joshbot sessions new <id>                # archive it and start empty
```

`show` output is redacted: credentials and your home directory are stripped
before display, though the files on disk are left verbatim. Destructive
commands prompt for confirmation and take `--force` to run unattended; without a
terminal they decline rather than hang, and exit non-zero so a script does not
read a refusal as success. A damaged session is flagged in the `NOTES` column,
so it is visible without reading the directory; its `.jsonl.corrupt` quarantine
copy survives being loaded, but `prune` removes it along with the conversation.

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

### Model-Centric Configuration (Recommended)

The new model-centric format is simpler and more intuitive. Define models directly with their API configuration:

```json
{
  "models_config": {
    "models": [
      {
        "name": "smart",
        "model": "anthropic/claude-sonnet-4",
        "api_key": "sk-ant-..."
      },
      {
        "name": "fast",
        "model": "groq/llama-3.3-70b-versatile",
        "api_key": "gsk_..."
      },
      {
        "name": "local",
        "model": "ollama/llama3.2",
        "api_base": "http://localhost:11434/v1"
      }
    ],
    "agent": {
      "model": "smart",
      "fallback": ["fast", "local"]
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
    "web": { "search": { "api_key": "" } },
    "exec": { "timeout": 60 },
    "restrict_to_workspace": true
  }
}
```

**Benefits:**
- Provider auto-detected from model prefix (e.g., `groq/` → Groq API)
- Easy fallback chains — try next model if one fails
- No separate provider configuration needed

### Provider Auto-Detection

| Model Prefix | Provider | Default API Base |
|--------------|----------|------------------|
| `anthropic/` | Anthropic | `https://api.anthropic.com` |
| `openai/` | OpenAI | `https://api.openai.com/v1` |
| `groq/` | Groq | `https://api.groq.com/openai/v1` |
| `ollama/` | Ollama | `http://localhost:11434/v1` |
| `openrouter/` | OpenRouter | `https://openrouter.ai/api/v1` |
| `nvidia/` | NVIDIA NIM | `https://integrate.api.nvidia.com/v1` |
| `deepseek/` | DeepSeek | `https://api.deepseek.com/v1` |
| `gemini/` | Google Gemini | `https://generativelanguage.googleapis.com/v1beta` |
| `cerebras/` | Cerebras | `https://api.cerebras.ai/v1` |
| `poolside/` | Poolside | `https://inference.poolside.ai/v1` |

The prefix is stripped before the request is sent, because for most providers it
is joshbot's routing hint rather than part of the model name. **Poolside is the
exception** — its published IDs really are `poolside/laguna-s-2.1`, so the prefix
is kept. Nothing else needs to change; just use the ID exactly as the provider
lists it.

### Poolside

```bash
joshbot configure \
  --provider poolside \
  --api-key "$POOLSIDE_API_KEY" \
  --model poolside/laguna-s-2.1
```

The API base defaults to `https://inference.poolside.ai/v1`, so `--api-base` is
optional. Current models are `poolside/laguna-s-2.1` and `poolside/laguna-xs-2.1`
(`poolside/laguna-m.1` is deprecated as of 2026-07-28). Ask the endpoint for the
authoritative list:

```bash
curl -s https://inference.poolside.ai/v1/models \
  -H "Authorization: Bearer $POOLSIDE_API_KEY" | jq -r '.data[].id'
```

### Legacy Provider Configuration (Still Supported)

For backward compatibility, the old format still works:

```json
{
  "providers": {
    "openrouter": {
      "api_key": "sk-or-v1-your-key-here",
      "enabled": true
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
  },
  "tools": {
    "web": { "search": { "api_key": "" } },
    "exec": { "timeout": 60 },
    "restrict_to_workspace": true,
    "shell_allow_list": [],
    "filesystem_allowed_paths": [],
    "shell_sandbox": "off",
    "shell_sandbox_allow_network": false
  }
}
```

### Shell Sandbox

`tools.shell_sandbox` adds OS-level containment for shell commands, on top of the deny list (which screens command text — a filter, not a boundary). It's off by default so upgrading doesn't silently change what an existing setup can do.

- `"off"` (default) — no containment beyond the deny list.
- `"workspace"` — confines the filesystem to the workspace plus toolchain build caches (e.g. `GOCACHE`, `~/.cache`); `$HOME` and everything else outside that is unreachable. Outbound TCP is denied unless `tools.shell_sandbox_allow_network` is `true`.

Implemented via [Landlock](https://landlock.io/) and **Linux-only**. It fails closed: an unrecognized value, running on a non-Linux OS, or a kernel without Landlock support is a startup error rather than a silent no-op — set it back to `"off"` if you need to run without containment.

### Environment Variables

All config values can be set via environment variables with `JOSHBOT_` prefix:

**Model-Centric Format (New):**
```bash
# Configure models
export JOSHBOT_MODELS_CONFIG__MODELS__0__NAME="smart"
export JOSHBOT_MODELS_CONFIG__MODELS__0__MODEL="anthropic/claude-sonnet-4"
export JOSHBOT_MODELS_CONFIG__MODELS__0__API_KEY="sk-ant-..."

export JOSHBOT_MODELS_CONFIG__MODELS__1__NAME="fast"
export JOSHBOT_MODELS_CONFIG__MODELS__1__MODEL="groq/llama-3.3-70b-versatile"
export JOSHBOT_MODELS_CONFIG__MODELS__1__API_KEY="gsk_..."

# Set active model and fallback chain
export JOSHBOT_MODELS_CONFIG__AGENT__MODEL="smart"
export JOSHBOT_MODELS_CONFIG__AGENT__FALLBACK="fast"
```

**Legacy Format:**
```bash
export JOSHBOT_PROVIDERS__OPENROUTER__API_KEY="sk-or-..."
export JOSHBOT_CHANNELS__TELEGRAM__ENABLED="true"
```

### Changing the LLM Model

With the new model-centric config, changing models is straightforward:

**Use Anthropic Claude:**
```json
{
  "models_config": {
    "models": [{ "name": "claude", "model": "anthropic/claude-sonnet-4", "api_key": "sk-ant-..." }],
    "agent": { "model": "claude" }
  }
}
```

**Use OpenAI GPT-4:**
```json
{
  "models_config": {
    "models": [{ "name": "gpt", "model": "openai/gpt-4o", "api_key": "sk-..." }],
    "agent": { "model": "gpt" }
  }
}
```

**Use local Ollama (no API key needed):**
```json
{
  "models_config": {
    "models": [{ "name": "local", "model": "ollama/llama3.2" }],
    "agent": { "model": "local" }
  }
}
```

**With fallback chain:**
```json
{
  "models_config": {
    "models": [
      { "name": "primary", "model": "anthropic/claude-sonnet-4", "api_key": "sk-ant-..." },
      { "name": "fallback", "model": "groq/llama-3.3-70b-versatile", "api_key": "gsk_..." }
    ],
    "agent": { "model": "primary", "fallback": ["fallback"] }
  }
}
```

### GitHub Copilot (OAuth)

GitHub Copilot uses a device-code OAuth flow and stores its token in `~/.joshbot/auth.json`.

1. Start authentication:
   ```bash
   joshbot auth github-copilot
   ```
2. Follow the on-screen device flow instructions.
3. When prompted, choose a model (saved to `config.json` with `enabled: true`).

After auth, you can run `joshbot agent` or `joshbot gateway` normally.

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

On startup joshbot registers its command menu with Telegram (`/start`, `/help`,
`/new`), so they appear behind the menu button and autocomplete as you type. If
Telegram rejects the registration it is logged and the bot starts anyway. A
command that does not exist gets an "Unknown command" reply listing the real
ones instead of silence.

While the agent is working, the "typing…" indicator is refreshed every 4 seconds
until the reply is sent, so it stays visible for the whole turn.

## Built-in Tools

| Tool | Description |
|------|-------------|
| `read_file` | Read file contents |
| `write_file` | Write/create files |
| `edit_file` | Find-and-replace editing |
| `list_dir` | List directory contents |
| `glob` | Find files by pattern |
| `grep` | Search file contents |
| `shell` | Execute shell commands (deny-listed, allowlisted env, optional Linux sandbox) |
| `web_search` | Search the web (exa-cli / Exa MCP / DuckDuckGo — no key required) |
| `web_fetch` | Fetch and extract web page content |
| `message` | Send messages to other channels |
| `memory_search` | Search stored facts by keyword, category, or tags |
| `skill_registry` | List, create, and delete skills (workspace skills need `joshbot skills trust` before use) |
| `parallel_subagent` | Run multiple subagent tasks in parallel |
| `chain_execution` | Run subagent steps sequentially, feeding output forward |

**Security defaults:**
- `web_fetch` and `web_search` block localhost, private IP ranges, and metadata hosts (SSRF protection), enforced at dial time.
- `restrict_to_workspace` limits file and shell operations to the workspace unless explicitly allowed.
- Shell commands get an allowlisted environment, not joshbot's own — provider API keys and other secret-shaped variables are never inherited.
- `tools.shell_sandbox: "workspace"` additionally confines shell commands with an OS-level sandbox (Landlock, Linux only) — see [Shell Sandbox](#shell-sandbox) below.
- Everything joshbot logs or prints is redacted first: API keys, `Authorization` headers, credential-shaped assignments and your home directory path are replaced with `[REDACTED]` and `~`, so a log or `joshbot status` dump can be pasted into a bug report. Session files on disk are deliberately exempt and stay verbatim at `0600` — rewriting conversation content on save would mangle legitimate text.

## Chat Commands

| Command | Channel | Description |
|---------|---------|-------------|
| `/start` | Telegram | Start a conversation (shows the help text) |
| `/new` | Both | Start a new session (clears context) |
| `/help` | Both | Show available commands |
| `/clear` | CLI | Clear the terminal screen |
| `/history` | CLI | Show input history |
| `/quit`, `/exit` | CLI | Exit the program |

There is no `/status` chat command — use `joshbot status` from the shell instead.

## Architecture

```
joshbot/
├── cmd/joshbot/     # CLI entry point
├── internal/
│   ├── agent/       # Core brain (loop, context)
│   ├── memory/      # Structured fact store (fact.go, search.go, metadata.go)
│   ├── skills/      # Skill discovery, detection, extraction, validation
│   ├── tools/       # Built-in tools (incl. memory_search, skill_registry)
│   ├── channels/    # Chat integrations (CLI, Telegram)
│   ├── bus/         # Message bus (decouples channels from agent)
│   ├── providers/   # LLM provider layer
│   ├── session/     # Conversation persistence (JSONL)
│   ├── cron/        # Task scheduling
│   ├── heartbeat/   # Proactive wake-ups
│   └── config/      # Configuration loading
```

**Key patterns:**
- **Message bus**: Channels decoupled from agent via async queues
- **ReAct loop**: LLM → tools → reflect → repeat (max 20 iterations)
- **Progressive skill loading**: Minimal context overhead, full content on demand
- **Plain-file memory**: No databases, just markdown — simple and portable
- **Context compression**: Summarizes old context to stay within token limits
- **Prompt caching**: Static system prompt cached with mtime-based invalidation, reducing file I/O on every message
- **Model-centric config**: Provider auto-detected from model prefix, fallback chains for resilience

## Troubleshooting

**"No providers configured"** — Run `joshbot onboard` or create `~/.joshbot/config.json` with at least one provider.

**LLM calls failing** — Check your API key. Run `joshbot status` to verify configuration.

**Telegram bot not responding** — Verify `channels.telegram.enabled` is `true` and check your user ID is in `allow_from`.

**GitHub Copilot not authenticated** — Run `joshbot auth github-copilot`. If it previously worked, re-run auth to refresh an expired token.

**"URL blocked by security policy"** — `web_fetch` blocks localhost/private IPs and metadata endpoints to prevent SSRF. Use a public URL or proxy through an external service.

**Getting "I've processed your request." instead of actual responses** — This can happen when:
- The LLM returns empty content (rate limiting, API errors)
- Tool execution completes but the follow-up LLM call fails

To diagnose, run with `--debug` flag:
```bash
joshbot agent --debug
```

Debug output will show:
- LLM response details (content length, tool calls, finish reason)
- HTTP response status codes
- Empty content warnings when detected
- Fallback provider activation

If you see rate limit errors (HTTP 429), configure fallback providers in your config for resilience.

## License

MIT — see [LICENSE](LICENSE).
