# joshbot

[![CI](https://github.com/bigknoxy/joshbot/actions/workflows/ci.yml/badge.svg)](https://github.com/bigknoxy/joshbot/actions/workflows/ci.yml)
[![Coverage](docs/coverage-badge.svg)](https://github.com/bigknoxy/joshbot/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/bigknoxy/joshbot.svg)](https://pkg.go.dev/github.com/bigknoxy/joshbot)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/dl/)
[![GitHub release](https://img.shields.io/github/v/release/bigknoxy/joshbot?include_prereleases)](https://github.com/bigknoxy/joshbot/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A nanobot-class personal AI agent as a **single ~19MB Go binary** — no Python, no venv, no runtime dependencies. Self-hosted, curl to running in under a minute, and scriptable enough to test in CI. Self-learning memory, skill self-creation, subagent delegation, multi-provider LLM routing, and Telegram/Discord chat all ship in the one static binary.

### Why joshbot instead of a Python agent stack?

| | joshbot | Typical Python agent (e.g. nanobot) |
|---|---|---|
| **Install** | `curl … \| bash` → one binary | `pip`/`venv`, interpreter + wheels to keep in sync |
| **Runtime deps** | Zero — static Go binary (~19MB) | CPython + a dependency tree |
| **Startup** | No interpreter, no imports — the binary is the runtime | Interpreter + import cost |
| **Shell safety** | Deny-listed **and** env-stripped (no API keys inherited), optional OS sandbox | Varies |
| **Untrusted skills** | Inert until `joshbot skills trust`, bound to a directory-tree hash | Varies |
| **Scriptability** | Every command non-interactive; `--output-format json`, exit-code contract | Varies |

joshbot is heavier on guarantees, lighter on your machine.

## Features

- **Self-Learning Memory** - Automatically remembers important facts across conversations using a structured fact system (categorized with SHA256-based IDs, confidence scoring, source tracking, and deduplication)
- **Context Compression** - Summarizes old context to stay within token limits; works well with small local models
- **Skill Self-Creation** - Creates new capabilities for itself as markdown files, with auto-detection from conversation patterns and LLM-based extraction
- **Subagent Delegation** - Spawns focused subagents for complex multi-step tasks
- **Telegram & Discord** - Chat from your phone with full media support; both fail closed on an empty allowlist
- **Scriptable / Non-Interactive** - Every command runs headless; `agent -m` for one-shot, `--output-format json`/`stream-json` for machine-readable output, `--resume` to thread sessions, and a stable exit-code contract for CI
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

### Global flags

These apply to every command:

| Flag | Effect |
|------|--------|
| `--no-color` | Strip ANSI colour from all output |
| `--log-level debug\|info\|warn\|error` | Set log verbosity (takes precedence over `--verbose`/`--debug`) |
| `--verbose` / `--debug` | Shortcuts for more detailed logging |

### Non-interactive & scriptable use

Every command works headless — no TTY, no prompts. This makes joshbot safe to drive from scripts and CI.

```bash
# One-shot message, plain text on stdout
joshbot agent -m "summarize ./NOTES.md"

# Machine-readable single JSON result (stdout is data only; logs go to stderr)
joshbot agent -m "hello" --output-format json

# Streaming NDJSON: tool_start / tool_done lines, then a terminal result line
joshbot agent -m "run the tests" --output-format stream-json

# Resume a prior session by the id echoed in a previous json result
joshbot agent -m "and now lint it" --output-format json --resume <session-id>
```

`--output-format` accepts `text` (default), `json`, or `stream-json`. The JSON
modes are non-interactive and require `-m`/`--message`. In JSON modes stdout
carries **only** the result document — logs are routed to stderr — so consumers
can parse stdout directly. `cost_usd` is emitted as `null` (no pricing table is
bundled; compute cost from the returned token `usage`).

#### Exit codes

joshbot returns a stable exit code so scripts can branch on the failure class:

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error |
| `2` | Auth / no provider configured (remediation included in the message) |
| `3` | Validation error (bad flag, e.g. unknown `--output-format`, or JSON mode without `-m`) |
| `4` | Confirmation required (reserved for destructive flows) |

In JSON modes a failure is emitted as a well-formed `{"type":"error","code":…,"remediation":…}` object on stderr.

A turn that fails **inside** the agent counts as a failure too: the agent reports
LLM errors in band as reply text (`Error processing request: ...`) so a chat
channel can show them, but `agent -m` translates that back into exit code `1`. In
JSON mode the result document carries `"is_error": true` with the failure text in
`result`, alongside the `{"type":"error",…}` document on stderr. The success path
is unchanged.

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

# Fully non-interactive: configure a provider without any prompts
joshbot onboard --force \
  --provider openrouter \
  --api-key "$OPENROUTER_API_KEY"
```

**Non-interactive onboarding** takes `--provider`, `--api-key` and `--api-base`
(the last is required for `azure`/`custom`). The API key also falls back to
`JOSHBOT_PROVIDERS__<PROVIDER>__API_KEY`. If `--force` is given with no way to
wire a real credential — no flag, no env key, no existing provider — onboarding
now **fails with a non-zero exit and an actionable message** instead of writing
a stub config and reporting success. `--provider` must name one of the supported
providers (`openrouter`, `openai`, `nvidia`, `groq`, `ollama`, `anthropic`,
`poolside`, `azure`, `custom`, `litellm`, `github-copilot`); anything else is
rejected and nothing is written. With `--force --provider <name>` the default
model comes from that provider (e.g. `llama3.1:8b` for `ollama`), not from
OpenRouter. Interactive `onboard` follows the same rule: if no provider ended up
configured — running with stdin closed, say — it exits non-zero with the same
message the `--force` path uses, though the config and workspace scaffold are
still written. After saving, onboard validates the credential (non-fatal) and
prints the provider's key URL if it looks wrong. Providers with no fixed
endpoint (`azure`, `custom`, `litellm`) report "could not verify ... no API base
URL configured" rather than being validated against someone else's API.

The interactive onboard flow will:
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

**Scan interval** defaults to **30m** and is configurable:

```json
{ "heartbeat": { "interval": "1h" } }
```

The value is a Go duration string (`30m`, `1h`, `1h30m`); empty, unparseable or
non-positive values fall back to 30m. Override with `JOSHBOT_HEARTBEAT__INTERVAL`.

**Completion contract:** each task is published to the agent with a marker telling
it this is an automated background check, not a user message — and to reply with
exactly `HEARTBEAT_OK` when nothing needs your attention. Those `HEARTBEAT_OK`
(and empty) replies are suppressed rather than delivered, so the heartbeat is
silent unless something genuinely warrants a ping. A tick is **skipped** (tasks
left unchecked, to retry) when no recipient chat ID is known yet; a task is only
checked off `[x]` once it has actually been published, so it never re-fires or
silently burns tokens against a dead end.

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
      "memory_window": 50,
      "streaming": false
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

**Per-platform enforcement:**

| Platform | Mechanism when `"workspace"` | Default (`"off"`) posture |
|----------|------------------------------|---------------------------|
| Linux | [Landlock](https://landlock.io/) LSM (re-exec helper) | Deny-list only |
| macOS | Seatbelt (`sandbox-exec` profile) | Deny-list only |
| Other (no sandbox available) | n/a | **Allowlist-only** — the shell tool falls back to a small set of non-escaping read/inspect commands unless the operator sets an explicit `shell_allow_list` |

While an allowlist is in force — whether set explicitly or defaulted on a
platform with no sandbox — a command containing a shell construct that can
introduce a second command word (`;`, `&`, `|`, a newline, a backtick, `$(`,
`<(`, `>(`) is refused: the command is passed to `sh -c` unchanged, so matching
only the first word admitted `echo hi; id`. Run one command per call. The
default list deliberately omits `find`, `go` and `git`, each of which launches a
program of the caller's choosing; name them in `shell_allow_list` if you need
them.

It fails closed: an unrecognized value, or `"workspace"` on a host whose kernel lacks the needed support, is a startup error rather than a silent no-op — set it back to `"off"` to run without containment. The runtime default is intentionally **not** the sandbox: network-denied-by-default breaks common workflows, so macOS/Linux still rely on the deny list by default and you opt in with `tools.shell_sandbox: "workspace"`.

### Streaming Responses

`agents.defaults.streaming` prints the assistant's reply as it arrives instead of
after the whole turn completes. It is **off by default** and applies to both
config formats.

```json
"agents": { "defaults": { "streaming": true } }
```

Two limits are worth knowing before turning it on:

- It only takes effect in the **interactive CLI on a real terminal**. `joshbot
  agent -m`, piped output and the Telegram channel are unaffected, so scripted
  output stays byte-identical.
- Streaming gives up the non-streaming path's **transparent provider fallback**.
  Once the first token has been printed it cannot be unprinted, so a failure
  part-way through appends a visible `[stream error: ...]` marker to the reply
  rather than silently retrying against the next provider in the chain. If you
  value the retry more than the latency, leave it off.

### Configuration Precedence

Where two sources set the same value, the later one in this list wins:

1. **Defaults** — compiled in (`config.Defaults()`).
2. **Config file** — `~/.joshbot/config.json`, or whatever `--config` points at.
3. **Environment variables** — any `JOSHBOT_*` variable overrides the file value
   for that key (`config.Load` applies the file first, then the env overrides).
4. **Command flags** — a flag that carries a config value (`onboard --provider`,
   `--api-key`, `--api-base`, `agent --model`) overrides both.

Two things this list deliberately does not include:

- **There is no project-scoped config.** joshbot does not read a `.joshbot/` or
  `joshbot.json` from the working directory — one machine has one config, chosen
  by `--config` when you need a second. A per-directory config would silently
  change which provider and workspace an agent run used depending on where it
  was invoked from.
- `--config` selects *which file* is read; it is not itself an override. Point it
  at a file and the env layer still applies on top.
- `--config` anchors the **whole home**, not just the file. Sessions, media, cron
  and the skills trust store live beside the config that selected them, so
  `joshbot onboard --config /tmp/trial/config.json` builds a complete second
  install under `/tmp/trial/` and leaves `~/.joshbot/` alone.

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
export JOSHBOT_CHANNELS__TELEGRAM__ALLOW_FROM="123456789,987654321"   # comma-separated
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

> **⚠️ BREAKING (unreleased):** An **empty `allow_from` now denies every sender** instead of allowing everyone. Previously a Telegram bot with no allowlist was open to the whole internet — anyone who found it got a direct line into an agent loop holding the shell tool. It now fails closed and logs a loud warning at startup naming the exact key to set. **If you relied on an empty allowlist, your bot will reject all messages until you add your numeric Telegram user ID to `channels.telegram.allow_from`.** The same fail-closed rule applies to Discord's `allow_from`.

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

On startup joshbot registers its command menu with Telegram (`/start`, `/new`,
`/status`, `/model`, `/personality`, `/compact`, `/help`), so they appear behind
the menu button and autocomplete as you type. If Telegram rejects the
registration it is logged and the bot starts anyway. A command that does not
exist gets an "Unknown command" reply listing the real ones instead of silence.

The commands whose behaviour lives in the agent (`/status`, `/model`,
`/personality`, `/compact`) are forwarded to it with the same allowlist gate as
a direct message, so they work identically in the Telegram menu and the CLI.

While the agent is working, the "typing…" indicator is refreshed every 4 seconds
until the reply is sent, so it stays visible for the whole turn.

## Discord Setup

joshbot also speaks Discord (gateway websocket + REST via the pure-Go
`discordgo` library, compiled into the single binary). Configure it in
`config.json` or via env vars — the onboarding wizard does not yet prompt for it:

```json
{
  "channels": {
    "discord": {
      "enabled": true,
      "token": "your-bot-token",
      "allow_from": ["123456789012345678"]
    }
  }
}
```

- `allow_from` entries are numeric Discord user IDs (snowflakes), usernames, or
  global names. Like Telegram, **an empty allowlist rejects everyone.**
- Env overrides: `JOSHBOT_CHANNELS__DISCORD__ENABLED`, `JOSHBOT_CHANNELS__DISCORD__TOKEN`, `JOSHBOT_CHANNELS__DISCORD__ALLOW_FROM` (comma-separated).
- Messages over 2000 chars are split (code-fence aware); the bot ignores its own
  and other bots' messages; `/help` and `/new` work as text commands.

> **⚠️ Enable one chat channel at a time.** The message bus exposes a *single*
> outbound channel that channel implementations read competitively, so running
> Discord and Telegram simultaneously has them steal each other's replies —
> roughly half of each conversation's answers are delivered to the other
> service's chat, with no error anywhere. Until the bus fans out per channel,
> enable **either** `channels.telegram` **or** `channels.discord`, not both.
> (`internal/channels/discord.go`, `consumeOutbound`.)

## MCP Servers (experimental)

joshbot ships a stdio [MCP](https://modelcontextprotocol.io/) client. Declaring a
server is an **operator-only** act — `config.json` lives outside the workspace
and cannot be written by a workspace-confined tool, so it is the trust boundary.
Discovered tools are registered under a namespaced name `mcp__<server>__<tool>`,
so a server can never shadow a built-in tool like `shell`.

```json
{
  "mcp": {
    "servers": {
      "myserver": {
        "command": "some-mcp-server",
        "args": ["--stdio"],
        "env": { "FOO": "bar" },
        "enabled": true
      }
    }
  }
}
```

> **Note:** declared servers are now started during component setup and their
> tools registered; startup is fail-soft, so a server that will not start is
> logged and skipped rather than aborting joshbot, and the processes are reaped
> on exit. MCP child processes get the same allowlisted, credential-screened
> environment as shell children (no provider API keys), but their **filesystem
> access is not sandboxed**. See `SECURITY.md`.

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
- `tools.shell_sandbox: "workspace"` additionally confines shell commands with an OS-level sandbox (Landlock on Linux, Seatbelt on macOS) — see [Shell Sandbox](#shell-sandbox) below. On platforms with no sandbox, the shell tool falls back to allowlist-only by default.
- Everything joshbot logs or prints is redacted first: API keys, `Authorization` headers, credential-shaped assignments and your home directory path are replaced with `[REDACTED]` and `~`, so a log or `joshbot status` dump can be pasted into a bug report. Session files on disk are deliberately exempt and stay verbatim at `0600` — rewriting conversation content on save would mangle legitimate text.

## Chat Commands

| Command | Channel | Description |
|---------|---------|-------------|
| `/start` | Telegram | Start a conversation (shows the help text) |
| `/new` | Telegram, Discord, CLI | Start a fresh session (clears context, model override and personality) |
| `/status` | Telegram, CLI | Show the current model, tool count, memory window and max iterations |
| `/model [name]` | Telegram, CLI | Switch model for this session (`--global` makes it the default for all sessions) |
| `/personality [name]` | Telegram, CLI | Set a named personality (`concise`, `technical`, `pirate`, `cheerful`, `formal`), any custom instruction, or `none` to clear |
| `/compact` | Telegram, CLI | Summarize older conversation context now |
| `/help` | Telegram, Discord, CLI | Show available commands |
| `/clear` | CLI | Clear the terminal screen |
| `/history` | CLI | Show input history |
| `/quit`, `/exit` | CLI | Exit the program |

### Interactive CLI line editor

When `joshbot agent` runs in a real terminal (stdin *and* stdout are TTYs), the
plain `> ` prompt is replaced by a lightweight line editor:

- **Tab** cycles slash-command completions, with a hint line listing candidates.
- **Up / Down** recall history on a single-line buffer, or move the cursor
  between lines in a multiline buffer.
- **Left / Right / Home / End / Backspace / Delete** move and edit the line.
- **Alt+Enter** (or Ctrl+J) inserts a newline for multiline editing.
- **Ctrl+C** quits; **Ctrl+D** quits on an empty buffer, otherwise deletes
  forward.

The prompt shows the session's current model, so a `/model` switch is visible
before you type your next message. The editor activates only when both input
and output are real terminals — piped or scripted `joshbot agent` output is
untouched.

`/model` and `/personality` changes are per-session and persisted, so a
model you pick mid-conversation survives a restart.

## Architecture

```
joshbot/
├── cmd/joshbot/     # CLI entry point
├── internal/
│   ├── agent/       # Core brain (loop, context)
│   ├── memory/      # Structured fact store (fact.go, search.go, metadata.go)
│   ├── skills/      # Skill discovery, detection, extraction, validation
│   ├── tools/       # Built-in tools (incl. memory_search, skill_registry)
│   ├── channels/    # Chat integrations (Telegram, Discord)
│   ├── bus/         # Message bus (decouples channels from agent)
│   ├── providers/   # LLM provider layer
│   ├── mcp/         # stdio MCP client (namespaced tool registration)
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

<!-- agent-skills:doc-keeper:start -->
## Reference (auto-tracked by doc-keeper)

### Environment Variables
- `JOSHBOT_WORKSPACE`: _(add description)_
<!-- agent-skills:doc-keeper:end -->
