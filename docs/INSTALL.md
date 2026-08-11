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
curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash
```

This script will:
1. Download the pre-built binary for your platform
2. Verify it against the release checksums — and **refuse to install** if they
   do not match, or cannot be fetched at all
3. Install the binary to `~/.local/bin` (preferred) or `/usr/local/bin`

For a specific version, or a different directory:
```bash
curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash -s -- --version v1.45.2
curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash -s -- --bin-dir ~/bin
```

`--bin-dir` creates the directory if it does not exist. Re-running the script
upgrades an existing installation in place; installing over an unrelated binary
at the same path requires `--force`.

Checksum verification fails closed: if the release checksums cannot be
retrieved, the script installs nothing rather than continuing unverified. Set
`JOSHBOT_SKIP_CHECKSUM=1` to override that, at your own risk.

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
docker run -it -v ~/.joshbot:/home/joshbot/.joshbot joshbot onboard

# Run gateway mode
docker run -d -v ~/.joshbot:/home/joshbot/.joshbot joshbot gateway
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
   - Choose a provider — NVIDIA NIM is the recommended default (free tier), with OpenRouter, Groq, Ollama, GitHub Copilot, and Poolside also offered
   - Enter your API key for the chosen provider (OpenRouter's free tier key: https://openrouter.ai/keys)

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
   - Default depends on the provider you configured — e.g. `moonshotai/kimi-k2-thinking` for NVIDIA NIM, `openrouter/free` for OpenRouter
   - You can specify any model supported by your provider

### Onboarding Options

```bash
# Force fresh setup (backs up existing config)
joshbot onboard --force

# Reconfigure while keeping existing data
joshbot onboard --keep-data

# Fully non-interactive
joshbot onboard --force --provider ollama
joshbot onboard --force --provider openrouter --api-key "$OPENROUTER_API_KEY"
```

`--provider` must name a supported provider (`openrouter`, `openai`, `nvidia`,
`groq`, `ollama`, `anthropic`, `poolside`, `azure`, `custom`, `litellm`,
`github-copilot`); any other name is rejected and nothing is written. The default
model comes from the provider you selected (e.g. `llama3.1:8b` for `ollama`).
`--api-key` falls back to `JOSHBOT_PROVIDERS__<PROVIDER>__API_KEY`, and
`azure`/`custom` also need `--api-base`.

Onboarding **exits non-zero when no provider ended up configured** — including
interactive runs with stdin closed. The config and workspace scaffold are still
written; only the exit status reports the failure. Credential validation after
save is non-fatal, and providers with no fixed endpoint (`azure`, `custom`,
`litellm`) report "could not verify ... no API base URL configured" instead of
being validated against an unrelated API.

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
- Scheduled reminders (`cron` tool — durations like `30m`, `2h`, `1d`)

### Check Status

View your current configuration and system status:

```bash
joshbot status
```

```
╔═══════════════════════════════════════════╗
║            joshbot status                ║
╚═══════════════════════════════════════════╝
Version:        1.42.0
Config file:    ~/.joshbot/config.json (exists)
Workspace:      ~/.joshbot/workspace (exists)
Sessions:       ~/.joshbot/sessions

Model:          openai/gpt-4
Max tokens:     8192
Temperature:    0.7
Memory window:  50

Providers:      openrouter
Telegram:       disabled
Workspace restricted: enabled
```

If a provider is present but not registered, `status` says why — for example `nvidia (disabled — missing "api_key")` or `openrouter (disabled — set "enabled": true)`. If any workspace skills are awaiting approval, a `Skills:` line lists them.

### All Commands

| Command | Description |
|---------|-------------|
| `joshbot onboard` | First-time setup wizard |
| `joshbot agent` | Interactive CLI chat mode |
| `joshbot gateway` | Start all channels (Telegram, Discord) |
| `joshbot status` | Show configuration and status |
| `joshbot preflight` | Check the config would work, without calling any provider (exits non-zero if it would not) |
| `joshbot --output json <cmd>` | Machine-readable form of `preflight`, `status`, `skills list`, `mcp list`, `profiles list`, `auth status` and `configure --list` |
| `joshbot profiles list` | List named model profiles and where each would send requests |
| `joshbot agent --profile <name>` | Run with a named profile (also on `gateway` and `preflight`) |
| `joshbot skills list` \| `trust <name>` \| `untrust <name>` | Review and approve workspace skills |
| `joshbot mcp list` \| `trust <name>` \| `untrust <name>` | Review and approve MCP servers' advertised tools |
| `joshbot configure` | Configure LLM providers and settings |
| `joshbot sessions list` \| `show <id>` \| `prune <id>` \| `new <id>` \| `export <id>` | Inspect, manage and export stored conversations |
| `joshbot auth github-copilot` \| `status` | Manage OAuth authentication |
| `joshbot service install` \| `uninstall` \| `status` | Manage joshbot as a system service |
| `joshbot update` | Update to the latest release |
| `joshbot uninstall` | Remove the binary and optionally its config |
| `joshbot version` / `joshbot --version` | Show version |
| `joshbot --help` | Show help |

Every command runs non-interactively. `joshbot agent -m "..."` answers a single
message and exits; `--output-format json|stream-json` (which require `-m`) put
only the result document on stdout and route logs to stderr. Exit codes are
stable: `0` success, `1` general error, `2` auth / no provider, `3` validation,
`4` confirmation required. A turn that fails inside the agent exits `1` as well —
the agent reports LLM errors in band as reply text so chat channels can show
them, and the CLI translates that back into a non-zero exit, with `"is_error":
true` in the JSON result document and a `{"type":"error",…}` document on stderr.

`joshbot agent -m "..." --image path.png` attaches an image. The flag is
repeatable and requires `-m`. The type is decided by sniffing the content, not
the extension (PNG, JPEG, GIF, WebP); limits are 5 MB per image and 20 MB per
request; and if no configured model is known to accept images the run fails
before any provider call, naming the models tried. Telegram photos and image
documents are attached the same way.

---

## Configuration

### Config File Location

joshbot stores all configuration and data in `~/.joshbot/`:

```
~/.joshbot/
├── config.json          # Main configuration file (0600)
├── skills.trust         # Approved workspace skills (0600)
├── sessions/            # Conversation history, one JSONL file per
│                        #   channel:senderID (0600). A load that hits an
│                        #   unreadable line skips it and preserves the
│                        #   original bytes at <session-id>.jsonl.corrupt.
│                        #   Compaction moves the summarized messages to an
│                        #   append-only <session-id>.history.jsonl archive,
│                        #   which the agent never reads back and which grows
│                        #   for the life of the session
├── media/               # Downloaded media files
├── cron/                # Created but unused; jobs live in workspace/cron/jobs.json
└── workspace/           # Memory, skills, and context files
    ├── SOUL.md          # Personality definition
    ├── USER.md          # User profile
    ├── AGENTS.md        # Agent behavior instructions
    ├── IDENTITY.md      # Bot identity
    ├── HEARTBEAT.md     # Proactive tasks checklist
    ├── cron/
    │   └── jobs.json    # Scheduled reminder/cron jobs
    ├── memory/
    │   ├── MEMORY.md    # Long-term memory
    │   └── HISTORY.md   # Event log
    └── skills/          # Custom skills
```

### Model-Centric Configuration (Recommended)

The new model-centric format simplifies configuration. Define models directly with their API settings:

```json
{
  "schema_version": 1,
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
        "model": "ollama/llama3.2"
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
  },
  "gateway": {
    "host": "0.0.0.0",
    "port": 18790
  },
  "log_level": "info"
}
```

**Key Benefits:**
- **Provider auto-detection**: Model prefix like `groq/` automatically configures the correct API endpoint
- **Fallback chains**: If the primary model fails, try the next in the fallback list
- **Simpler setup**: No separate provider configuration needed

#### Provider Auto-Detection

joshbot automatically detects the provider from the model name prefix:

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

For models without a recognized prefix, you must provide `api_base`.

### Legacy Provider Configuration

The old format is still supported for backward compatibility:

```json
{
  "schema_version": 1,
  "providers": {
    "openrouter": {
      "api_key": "sk-or-v1-your-key-here",
      "api_base": "",
      "extra_headers": {},
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
      "memory_window": 50,
      "streaming": true
    }
  },
  "channels": {
    "telegram": {
      "enabled": false,
      "token": "",
      "allow_from": [],
      "proxy": ""
    },
    "discord": {
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
    "restrict_to_workspace": true,
    "shell_allow_list": [],
    "filesystem_allowed_paths": [],
    "shell_sandbox": "off",
    "shell_sandbox_allow_network": false,
    "shell_approval": "off"
  },
  "gateway": {
    "host": "0.0.0.0",
    "port": 18790
  },
  "log_level": "info"
}
```

> **Important:** each provider under `providers` needs `"enabled": true` to register — omitting it silently disables that provider rather than erroring.

### Environment Variables

All configuration values can be overridden with environment variables using the `JOSHBOT_` prefix. Use `__` (double underscore) for nested values:

**Model-Centric Format (New):**
```bash
# Configure models (indexed)
export JOSHBOT_MODELS_CONFIG__MODELS__0__NAME="smart"
export JOSHBOT_MODELS_CONFIG__MODELS__0__MODEL="anthropic/claude-sonnet-4"
export JOSHBOT_MODELS_CONFIG__MODELS__0__API_KEY="sk-ant-..."

export JOSHBOT_MODELS_CONFIG__MODELS__1__NAME="fast"
export JOSHBOT_MODELS_CONFIG__MODELS__1__MODEL="groq/llama-3.3-70b-versatile"
export JOSHBOT_MODELS_CONFIG__MODELS__1__API_KEY="gsk_..."

# Set active model and fallback chain
export JOSHBOT_MODELS_CONFIG__AGENT__MODEL="smart"
export JOSHBOT_MODELS_CONFIG__AGENT__FALLBACK="fast,local"  # comma-separated
```

**Legacy Format:**
```bash
# Provider configuration
export JOSHBOT_PROVIDERS__OPENROUTER__API_KEY="sk-or-v1-..."

# Agent settings
export JOSHBOT_AGENTS__DEFAULTS__MODEL="anthropic/claude-sonnet-4"
export JOSHBOT_AGENTS__DEFAULTS__MAX_TOKENS="16384"
export JOSHBOT_AGENTS__DEFAULTS__TEMPERATURE="0.5"
```

**Common Settings:**
```bash
# Telegram configuration
export JOSHBOT_CHANNELS__TELEGRAM__ENABLED="true"
export JOSHBOT_CHANNELS__TELEGRAM__TOKEN="123456:ABC..."
export JOSHBOT_CHANNELS__TELEGRAM__ALLOW_FROM="123456789,987654321"  # comma-separated; empty denies everyone

# Discord configuration
export JOSHBOT_CHANNELS__DISCORD__ENABLED="true"
export JOSHBOT_CHANNELS__DISCORD__TOKEN="MTIz..."
export JOSHBOT_CHANNELS__DISCORD__ALLOW_FROM="123456789012345678"

# Tool settings
export JOSHBOT_TOOLS__RESTRICT_TO_WORKSPACE="true"
export JOSHBOT_TOOLS__EXEC__TIMEOUT="120"
export JOSHBOT_TOOLS__SHELL_ALLOW_LIST="git,status,go,test"
export JOSHBOT_TOOLS__FILESYSTEM_ALLOWED_PATHS="/var/log,/opt/shared"

# Logging
export JOSHBOT_LOG_LEVEL="debug"
```

### Key Settings

#### Model Selection

Choose an LLM model based on your needs:

| Use Case | Recommended Model | Notes |
|----------|-------------------|-------|
| Free tier | `openrouter/free` | No cost via OpenRouter, auto-routes to best free model |
| Better quality | `anthropic/claude-sonnet-4` | Requires Anthropic or OpenRouter credits |
| Fast responses | `groq/llama-3.3-70b-versatile` | Requires Groq API key |
| Local | `ollama/llama3.2` | No API key needed, runs locally |

**Model-Centric Config (Recommended):**
```json
{
  "models_config": {
    "models": [
      { "name": "smart", "model": "anthropic/claude-sonnet-4", "api_key": "sk-ant-..." },
      { "name": "fast", "model": "groq/llama-3.3-70b-versatile", "api_key": "gsk_..." }
    ],
    "agent": { "model": "smart", "fallback": ["fast"] }
  }
}
```

**With Fallback Chain:**
```json
{
  "models_config": {
    "models": [
      { "name": "primary", "model": "anthropic/claude-sonnet-4", "api_key": "sk-ant-..." },
      { "name": "backup", "model": "groq/llama-3.3-70b-versatile", "api_key": "gsk_..." },
      { "name": "local", "model": "ollama/llama3.2" }
    ],
    "agent": { "model": "primary", "fallback": ["backup", "local"] }
  }
}
```

If `primary` fails, joshbot automatically tries `backup`, then `local`.

#### Multiple Providers

You can configure multiple providers in the same config:

```json
{
  "models_config": {
    "models": [
      { "name": "claude", "model": "anthropic/claude-sonnet-4", "api_key": "sk-ant-..." },
      { "name": "gpt", "model": "openai/gpt-4o", "api_key": "sk-..." },
      { "name": "llama", "model": "groq/llama-3.3-70b-versatile", "api_key": "gsk_..." }
    ],
    "agent": { "model": "claude", "fallback": ["gpt", "llama"] }
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

**Fine-grained tool allowances:**

- `tools.shell_allow_list` (array): If set, only these commands (prefix match) are allowed to run.
- `tools.filesystem_allowed_paths` (array): Additional absolute paths allowed outside the workspace.

Example:

```json
{
  "tools": {
    "restrict_to_workspace": true,
    "shell_allow_list": ["git", "go", "ls"],
    "filesystem_allowed_paths": ["/var/log", "/opt/shared"]
  }
}
```

> Tip: When `shell_allow_list` is non-empty, commands must match an entry exactly or use it as a prefix (e.g., `git status`).

#### Shell Sandbox (Linux and macOS, opt-in)

`restrict_to_workspace` and `shell_allow_list` are text-based checks — useful, but not a hard boundary. `tools.shell_sandbox` adds real OS-level containment — [Landlock](https://landlock.io/) on Linux, Seatbelt (`sandbox-exec`) on macOS:

```json
{
  "tools": {
    "shell_sandbox": "workspace",
    "shell_sandbox_allow_network": false
  }
}
```

- `"off"` (default) — no containment beyond the checks above.
- `"workspace"` — confines shell commands' filesystem access to the workspace plus toolchain build caches (e.g. `GOCACHE`, `~/.cache`); everything else, including the rest of `$HOME`, is unreachable. Outbound TCP is denied unless `shell_sandbox_allow_network` is `true`.

It fails closed: an unrecognized value, a platform with no sandbox implementation (Windows, BSD), or a Linux kernel without Landlock support makes joshbot refuse to start rather than run unconfined. Set it back to `"off"` if that happens and you don't need containment. On platforms with no sandbox available, the shell tool instead defaults to allowlist-only when `tools.shell_allow_list` is unset.

#### Shell Approval (opt-in)

`tools.shell_approval` gates shell commands behind your own decision. The sandbox limits what a command can reach; approval decides whether it runs at all.

```json
{
  "tools": {
    "shell_approval": "interactive"
  }
}
```

- `"off"` (default) — commands run without asking.
- `"interactive"` — prompt before each command; `[a]ll for this session` stands until you exit the CLI.
- `"always"` — prompt before every command, with no remembered answer.

The prompt shows the full command line and its working directory, and only an explicit `y` approves — EOF, a timed-out turn and Ctrl-C are all denials. An unrecognized value is a startup error, never a silent `"off"`.

**It only works in the interactive CLI.** The prompt is installed by the interactive loop and only when stdout is a terminal, so the gateway (Telegram/Discord), cron jobs, the heartbeat scanner and piped `agent -m` runs have nobody to ask — with the gate on, their shell commands are **denied** rather than left blocking. If joshbot runs as a service, use `shell_sandbox` and `shell_allow_list` there instead.

Separately, spawned shell commands no longer inherit joshbot's own environment — they get an allowlisted subset (PATH, common toolchain variables, etc.) with anything credential-shaped stripped out, so provider API keys are not exposed to commands the agent runs.

#### Web Fetch SSRF Protection

`web_fetch` and `web_search` block localhost, private IP ranges, and metadata hostnames (for example: `127.0.0.1`, `10.0.0.0/8`, `169.254.169.254`, and `metadata.google.internal`), enforced at connection time. If you see **"URL blocked by security policy"**, use a public URL or proxy through a safe external endpoint.

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

> **Security Note:** `allow_from` is enforced deny-by-default: an empty or unset list rejects **every** sender and the channel logs a startup warning naming the key to set. Add your numeric Telegram user ID (as a string) before the bot will answer you.

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
├── cron/
│   └── jobs.json        # Scheduled reminder/cron jobs
├── memory/
│   ├── MEMORY.md        # Long-term facts (always in context)
│   └── HISTORY.md       # Searchable event log
└── skills/              # Custom skills (auto-discovered, need approval — see below)
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
| `HEARTBEAT.md` | Tasks for autonomous processing (checked every 5 min) |

### Streaming Responses

`agents.defaults.streaming` (default `true` since v1.48.0; set it to `false` to
restore whole-reply delivery) prints the reply as it arrives
rather than after the turn completes. It applies to the interactive CLI on a
real terminal and to Telegram, where the reply message is edited in place at
most every 3 seconds — `joshbot agent -m` and piped output are unchanged — and it
trades away the non-streaming path's transparent provider fallback: once text
has been printed it cannot be retried against another provider, so a mid-stream
failure appends a visible `[stream error: ...]` marker instead.

Upgrading from v1.47.x turns it on even if your config file already says
`"streaming": false`: that key has no `omitempty`, so it was written into every
config those versions saved regardless of intent. The schema v4→v5 migration
resets it once and logs that it did; set it to `false` afterwards and it stays off.

### Skill Approval

A `SKILL.md` placed in `workspace/skills/` — whether you write it or the agent creates it for itself — becomes part of the agent's standing instructions, so it is **inert until you approve it**:

```bash
joshbot skills list          # See what's pending
joshbot skills trust <name>  # Approve after reading the file
joshbot skills trust --all   # Approve everything pending
joshbot skills untrust <name> # Revoke approval
```

Approval is bound to the file's SHA-256 hash, so editing an approved skill revokes it until it's approved again. `joshbot status` also surfaces any skills awaiting review. Skills bundled with the release (see the [README](../README.md#bundled-skills)) don't need approval.

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
echo '{"providers":{"openrouter":{"api_key":"sk-or-...","enabled":true}}}' > ~/.joshbot/config.json
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
3. Ensure your user ID is in `allow_from` — an empty list rejects everyone
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
# Take ownership and restore owner-only access.
# Do NOT use `chmod -R 755` here: config.json holds live provider API keys,
# and the session and log files hold full conversation content.
chown -R "$USER" ~/.joshbot
chmod 700 ~/.joshbot
find ~/.joshbot -type d -exec chmod 700 {} +
find ~/.joshbot -type f -exec chmod 600 {} +

# Or recreate
rm -rf ~/.joshbot
joshbot onboard
```

#### GitHub Copilot not authenticated

**Problem:** Copilot models return "not authenticated" or "requires authentication".

**Solution:**
```bash
joshbot auth github-copilot
```

The device flow saves a token to `~/.joshbot/auth.json` and enables the `github-copilot` provider in `config.json`. If the token expires, rerun the auth command.

#### "URL blocked by security policy"

**Problem:** `web_fetch` or `web_search` refuses a URL.

**Solution:** Use a public URL. Both tools block localhost/private IPs and metadata endpoints to prevent SSRF.

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

For more details, see the [README.md](../README.md) or explore the `internal/skills/bundled/` directory for examples.
