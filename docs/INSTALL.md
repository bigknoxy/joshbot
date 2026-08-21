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
   - Choose a provider — NVIDIA NIM is the recommended default (free tier), with OpenRouter, OpenAI, Groq, Ollama, Anthropic, Poolside, and GitHub Copilot also offered (Azure/custom/litellm need `--api-base` and are configured via flags)
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

Keys for *other* providers found in the environment are offered as fallbacks:
interactive runs ask per provider, `--force` runs add them with a printed
notice, and `provider_defaults.fallback_order` is written primary-first.

Onboarding **exits non-zero when no provider ended up configured** — including
interactive runs with stdin closed. The config and workspace scaffold are still
written; only the exit status reports the failure. Credential validation after
save is non-fatal and is a **one-token chat completion** — listing models is
not an auth check, since some providers' `/models` endpoint accepts any key. A
rejected key (401/403) is reported with the provider's key URL; providers with
no fixed endpoint (`azure`, `custom`, `litellm`) report "could not verify ...
no API base URL configured" instead of being validated against an unrelated
API.

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
- Outbound file attachments on Telegram (`send_file` tool)

#### Sending files out (`send_file`)

Ask joshbot for a file and it arrives in the chat as a real attachment rather
than a path you cannot open:

```
you: render the weekly report and send it to me
joshbot: [document] report.pdf (412 KB)
```

- The file must be **inside the workspace**. The path is resolved through the
  same containment walk the `filesystem` tool uses, so a path outside it — or
  one that escapes through a symlink at any component — errors and sends
  nothing.
- **The bytes decide the type**, not the extension: an image is sent as a photo
  (rendered inline), anything else as a document. A text file named `.png` is a
  document; a JPEG named `.dat` is a photo.
- Limits: 10 MiB as a photo, 10 MiB as a document. Every file is read once
  through the containment walk and carried on the message, which is why the
  document limit is below Telegram's own 50 MiB ceiling — it is a memory bound.
- **You cannot ask joshbot to send a file somewhere else.** The file goes to the
  conversation you asked in; the tool has no recipient argument.
- An optional caption is sent with the file. A caption over Telegram's
  1,024-character limit is sent as its own following message rather than cut.
- On a channel with no attachment support (Discord today) joshbot appends the
  file's name, size and path to the message under an explicit
  `[attachment not supported on this channel]` marker — never a silent drop.

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
| `joshbot serve [--listen host:port]` | Serve the OpenAI-compatible HTTP API (`POST /v1/chat/completions`, `GET /v1/models`, `POST /v1/audio/transcriptions` when `stt.provider` is set, and `POST /v1/embeddings` when `embeddings.provider` is set, plus a browser chat UI at `/` when `api.webui` is true) |
| `joshbot status` | Show configuration and status |
| `joshbot preflight` | Check the config would work, without calling any provider (exits non-zero if it would not) |
| `joshbot --output json <cmd>` | Machine-readable form of `preflight`, `status`, `skills list`, `mcp list`, `profiles list`, `auth status` and `configure --list` |
| `joshbot profiles list` | List named model profiles and where each would send requests |
| `joshbot agent --profile <name>` | Run with a named profile (also on `gateway` and `preflight`) |
| `joshbot skills list` \| `trust <name>` \| `untrust <name>` | Review and approve workspace skills |
| `joshbot mcp list` \| `trust <name>` \| `untrust <name>` | Review and approve MCP servers' advertised tools |
| `joshbot configure` | Configure LLM providers and settings |
| `joshbot configure --fallback "nvidia,poolside"` | Set the provider fallback order; `""` clears it |
| `joshbot configure --migrate` | Convert a legacy provider config to the model-centric format (refuses lossy conversions) |
| `joshbot sessions list` \| `show <id>` \| `prune <id>` \| `new <id>` \| `export <id>` | Inspect, manage and export stored conversations |
| `joshbot memory status` \| `consolidate` | Inspect and run the Dream two-stage memory system (`agents.defaults.dream_mode`) |
| `joshbot auth github-copilot [--force]` \| `status` | Manage OAuth authentication |
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

A **PDF** sent on Telegram is carried on the turn as a document attachment and
read by the model. There is no CLI flag for it. The bytes must start with
`%PDF-` (the filename and declared MIME decide only whether a download is worth
spending), limits are 8 MiB per document and 16 MiB per request, and if no
configured model is known to read documents the request fails before any
provider call, naming the models tried. Document capability is a separate,
narrower list than vision. Office formats (docx, xlsx, pptx) remain refused.

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
      "dream_mode": "off",
      "streaming": true,
      "subagent_max_depth": 2,
      "timeout": "10m"
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

> **Timeouts:** `agents.defaults.timeout` bounds one agent turn (default 2m) and
> `providers.<name>.timeout` bounds one request to that provider (default 300s for
> ollama, 120s elsewhere). Both take a duration string — `"600s"`, `"10m"`,
> `"1h30m"` — or a bare number of **seconds**, so `"timeout": 600` is ten minutes.
> This is Go's `time.ParseDuration` grammar, so `"1d"` is an error even though the
> `cron` tool accepts it. The agent key also reads from
> `JOSHBOT_AGENTS__DEFAULTS__TIMEOUT` under the same rules, for a deployment with
> no config file; an unparseable value fails startup rather than being ignored.

> **Retries:** `providers.<name>.max_retries` (default 2, range 0–10) is how many
> times a transient failure — 429, 5xx, or a network error — is retried on the
> same provider with exponential backoff (an upstream `Retry-After` header is
> honoured) before the fallback chain moves to the next provider. `0` means fail
> over immediately. A provider that keeps failing is deprioritized in the chain
> for a cooldown window rather than re-dialled on every turn. When a fallback
> answers, the reply opens with a one-line notice naming the failed provider and
> who answered; `agents.defaults.quiet_fallback: true` suppresses it.

> **Streaming token usage:** streaming requests send OpenAI's
> `stream_options: {"include_usage": true}` so the final chunk carries real token
> counts — without it every streaming turn reported `usage` as zeros to anything
> billing or budgeting off the API. If an endpoint rejects the field, set
> `providers.<name>.disable_stream_usage: true` (also available per model as
> `disable_stream_usage` in the model-centric format) to omit it; usage then
> reads zero for that provider's streaming turns, which is honest rather than
> wrong.
> Raise the agent timeout for a slow local
> model. Anything under a second is rejected at load, naming the key. A value
> written by an older joshbot is a raw nanosecond count (`900000000000`); it is
> still read correctly and rewritten as a string on the next save.

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
export JOSHBOT_AGENTS__DEFAULTS__TIMEOUT="10m"        # or a bare number of seconds
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
export JOSHBOT_API__LISTEN="127.0.0.1:18791"
export JOSHBOT_API__API_KEYS="key-one,key-two"  # comma-separated; replaces api.api_keys

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

**It works in the interactive CLI and on Telegram.** In the CLI the prompt appears when stdout is a terminal; on the Telegram gateway the command is posted in the chat with an inline keyboard — `[✅ Allow] [❌ Deny]`, plus `[🔓 Allow all (this session)]` under `"interactive"` — and the turn waits for your tap, bounded by the turn timeout (no answer is a denial; only the chat that was asked can answer). Discord, cron jobs, the heartbeat scanner and piped `agent -m` runs have nobody to ask — with the gate on, their shell commands are **denied** rather than left blocking. For unattended use, reach for `shell_sandbox` and `shell_allow_list` instead.

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

#### Self-hosted Bot API server (`api_url`)

`channels.telegram.api_url` points joshbot at a [self-hosted `telegram-bot-api`
server](https://github.com/tdlib/telegram-bot-api) instead of `api.telegram.org`.
Leave it unset for the public Bot API. Only `http` and `https` URLs are accepted,
and a malformed value is a **fatal** config error rather than a silent fallback.
The onboard/configure wizard validates the bot token against this endpoint when
it is set — a LAN-only server cannot be reached via the public API — and a
re-run of the wizard preserves this key (along with `reactions` and
`stream_drafts`) even though it does not prompt for it.

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "123456789:ABCdef...",
      "allow_from": ["123456789"],
      "api_url": "http://127.0.0.1:8081"
    }
  }
}
```

Setting it raises the **outbound** attachment cap for `send_file` from 10 MiB to
50 MiB, on both the tool and the transport, from one shared rule so the two
cannot disagree. The ceiling is 50 MiB and not the local server's 2 GB because
the whole payload is held in memory from the tool call until the upload
finishes — the cap is a memory bound, and raising it further needs the
file-descriptor rework tracked in
[#305](https://github.com/bigknoxy/joshbot/issues/305).

It changes **nothing inbound**: what joshbot downloads from a chat and forwards
to a provider is still bounded by the provider limits (5 MiB per image, 8 MiB
per PDF), because those bound what a model is billed to read, not what Telegram
will carry.


#### Reaction acknowledgements (`reactions`)

`channels.telegram.reactions` turns on an emoji acknowledgement placed on **your
own message**, so you get a "it heard me" signal before the first token of the
reply exists — and it costs no message slot, which matters in a group.

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "123456789:ABCdef...",
      "allow_from": ["123456789"],
      "reactions": true
    }
  }
}
```


👀 goes on the moment the turn is admitted to the bus, and 👍 replaces it when
the reply is on its way. Telegram's `setMessageReaction` *sets* rather than
appends, so the second write clears the first with no extra call. The completion
emoji is 👍 and not ✅ because ✅ is not in Telegram's free reaction set — a
premium-only emoji is rejected with `REACTION_INVALID` and the acknowledgement
would silently never appear.

It is **off by default** and opt-in: a bot in a group without permission to react
would otherwise log a failure on every single turn. A reaction is an ornament on
the turn and never part of it, so a failure is logged at debug and the reply is
unaffected. A sender the allowlist rejects is never acknowledged.

#### Streaming drafts (`stream_drafts`)

`channels.telegram.stream_drafts` streams a turn through the Bot API
[`sendMessageDraft`](https://core.telegram.org/bots/api#sendmessagedraft) method
instead of the `sendMessage` + `editMessageText` loop. Telegram renders it as a
native animated draft, and before any output exists an empty-text draft renders
as Telegram's own "Thinking…" placeholder, so the phone shows the turn started
before the first token arrives.

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "123456789:ABCdef...",
      "allow_from": ["123456789"],
      "stream_drafts": true
    }
  }
}
```


Off by default and carrying `omitempty`, so it is absent from every config
joshbot has already saved and needs no schema migration.

Four things to know before switching it on:

- **Private chats only.** The Bot API documents `chat_id` for this method as
  "the target private chat", so a group or supergroup keeps the edit loop.
- **It needs a new enough Bot API.** Empty text — the "Thinking…" placeholder —
  was allowed in **Bot API 10.0** (changelog dated 8 May 2026). Note that
  [#308](https://github.com/bigknoxy/joshbot/issues/308) says "Bot API 9.3/9.5";
  that is wrong, and the version above is what the published reference says.
  A server that does not know the method is not a problem: the first refusal
  turns drafts off **for that turn** and the edit loop carries the answer from
  that delta on, so nothing is lost but the animation.
- **A draft is ephemeral.** The reference calls it "a temporary 30-second
  preview", and the finished text must still be sent with an ordinary
  `sendMessage` to persist it. joshbot always does that at the end of the turn,
  so a draft never counts as delivery — which is also why a turn that streams
  nothing still gets its reply through the normal path while the placeholder
  expires on its own.
- **Tool progress rides the draft too.** In draft mode the `⚙️ …` status line
  goes into the draft slot rather than a real message, so there is no status
  message left over for the reply to replace.

It honours `channels.telegram.api_url`, since the raw call goes to the same
configured base URL.

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

### HTTP API (`joshbot serve`)

`joshbot serve` speaks the OpenAI chat API so any client that accepts a custom
base URL can use joshbot as a backend. The served "model" is the agent itself —
a request runs the full ReAct loop with tools, memory and skills, so `/v1/models`
advertises exactly one id (`joshbot`) and the request's `model` field is accepted
and ignored.

```json
{
  "api": {
    "listen": "127.0.0.1:18791",
    "api_keys": ["<a long random string>"]
  }
}
```

| Key | Default | Meaning |
|---|---|---|
| `api.listen` | `127.0.0.1:18791` | Bind address. `--listen` overrides it for one run. |
| `api.api_keys` | none | Accepted bearer tokens. Empty means the server refuses to start. |
| `api.webui` | `false` | Serve the browser chat UI at `/`. Off means those routes 404. |

Authentication is mandatory: a caller reaching this endpoint reaches the shell
and filesystem tools, so there is no unauthenticated mode and no default key. The
bind address is loopback for the same reason — see [SECURITY.md](../SECURITY.md)
before exposing it.

```bash
curl http://127.0.0.1:18791/v1/chat/completions \
  -H "Authorization: Bearer $JOSHBOT_API_KEY" \
  -d '{"messages":[{"role":"user","content":"hello"}]}'
```

Set `"stream": true` for `text/event-stream` frames ending in `data: [DONE]`. The
optional `user` field picks the session (`api:<user>`, default `default`).

Set `api.webui` to `true` to also serve a browser chat page at `/`; `joshbot
serve` then prints its URL at startup. It is off by default because the page is a
login form that accepts an `api.api_keys` value, and that key reaches the shell
and filesystem tools — such a form must not appear on every existing bind at
upgrade. With it off, `/`, `/webui/static/...`, `/webui/login`, `/webui/logout`,
`/webui/config` and `/webui/session` all answer 404.

The flow: open the URL, paste a configured API key, and the server exchanges it
for an `HttpOnly` `SameSite=Strict` session cookie (12-hour expiry, in memory
only, so a restart signs you out). Writes on the cookie path additionally require
the `X-Joshbot-CSRF` header the page reads from `GET /webui/config`. The
`Authorization: Bearer` path is checked first and is unaffected, so existing
OpenAI clients keep working. The page is embedded in the binary and loads nothing
from the network.

`POST /v1/audio/transcriptions` transcribes a `multipart/form-data` upload with
the provider configured under `stt` — it is not the agent, so there is no loop,
session or memory. Without `stt.provider` it answers 501 naming the key.

```bash
curl http://127.0.0.1:18791/v1/audio/transcriptions \
  -H "Authorization: Bearer $JOSHBOT_API_KEY" \
  -F file=@voice.ogg
```

The upload is capped at 25 MiB and sniffed by content (flac, mp3, mp4/m4a, ogg,
wav, webm), so a text file named `voice.mp3` is refused before it costs anything.

`POST /v1/embeddings` embeds text with the provider configured under
`embeddings` — also not the agent. Without `embeddings.provider` it answers 501
naming the key.

```json
{
  "embeddings": {
    "provider": "ollama",
    "model": "nomic-embed-text",
    "timeout": "60s"
  }
}
```

```bash
curl http://127.0.0.1:18791/v1/embeddings \
  -H "Authorization: Bearer $JOSHBOT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"input":["dog","puppy"]}'
```

The provider's key and `api_base` are reused, and no key is required (ollama is
keyless). `embeddings.model` defaults to `nomic-embed-text` for `ollama` and
`text-embedding-3-small` for `openai`; any other provider must set it.
`input` takes a string or an array of strings, `encoding_format` takes `float`
(default) or `base64`, and a request is refused before the provider is dialled
if it carries more than 128 inputs or any input over 64 KiB.

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

#### Dream (optional)

Set `agents.defaults.dream_mode` to `"record"` or `"full"` to enable a second
memory track that clusters recorded turns into durable insights using local
TF-IDF embeddings — no embedding API. Insights are surfaced by `memory_search`
and confidence decays with a 30-day half-life. It is off (`""`) by default and
leaves MEMORY.md and HISTORY.md untouched.

```bash
joshbot memory status        # mode, raw records, stored insights
joshbot memory consolidate   # run consolidation now
```

Data lives in `<workspace>/memory/dream_raw.log` and
`<workspace>/memory/dream_consolidated.jsonl`. Consolidation is manual — nothing
schedules it, so the raw log grows until you run `joshbot memory consolidate`
(or schedule it yourself). In `"record"` mode that command exits non-zero:
recording is all that mode does.

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

The device flow saves a token to `~/.joshbot/auth.json` and enables the `github-copilot` provider in `config.json`. That token does not expire on its own — joshbot exchanges it for a short-lived Copilot API token per request and refreshes that automatically. If GitHub revoked the authorization, redo the flow with `joshbot auth github-copilot --force` (without `--force` the command reports the existing token and exits).

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
