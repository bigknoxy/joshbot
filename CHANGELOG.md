# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] - 2026-02-21

### Added

#### Interactive Telegram Setup
- Telegram setup wizard integrated into `joshbot onboard` (Step 4)
- Guides users through @BotFather bot creation
- Validates bot token via `getMe` API before saving
- Optional allowed usernames configuration
- Auto-saves to config without manual editing

#### Service Management
- `joshbot service install`: Install joshbot as a system service
- `joshbot service uninstall`: Remove the system service
- `joshbot service status`: Check service status
- **Systemd support** (Linux): Service installed to `/etc/systemd/system/joshbot.service`
- **Launchd support** (macOS): Service installed to `~/Library/LaunchAgents/com.joshbot.plist`
- Auto-start on boot with proper logging

#### Enhanced Onboard Flow
- Step 1: API key setup
- Step 2: Personality selection
- Step 3: Model selection
- Step 4: Telegram setup (optional)
- Step 5: Service installation (recommended for Telegram users)
- Explains why service install is needed for Telegram bots

### Changed

- Onboard now offers to start gateway automatically after Telegram setup
- Telegram token validation happens during setup (not at runtime)

## [1.0.0] - 2026-02-21

### Migration Notes

This release marks a complete rewrite of joshbot from Python to Go. The new Go implementation
offers improved performance, simpler deployment, and a more robust architecture. 

**Key changes:**
- Configuration from previous Python version is **not** compatible
- Memory files (MEMORY.md, HISTORY.md) in `~/.joshbot/` remain compatible
- Sessions in `~/.joshbot/sessions/` remain compatible
- Skills in `workspace/skills/` remain compatible
- Run `./joshbot onboard` to set up fresh configuration

### Added

#### Core Architecture
- Complete Go implementation (~3,600 LOC) with goroutine-based concurrency
- Message bus architecture decoupling chat channels from the agent loop
- ReAct agent loop with tool execution and reflection cycles (max 20 iterations)
- Multi-provider LLM support via OpenRouter-compatible APIs

#### Memory System
- **MEMORY.md**: Persistent long-term memory, always included in context
- **HISTORY.md**: Searchable event log (grep-based) for recent context
- Self-learning memory consolidation during idle periods
- Context compression for small models with token budgeting

#### Skills System
- Skill discovery from `workspace/skills/{skill}/SKILL.md` files
- Progressive loading: summary on first use, full content after first execution
- YAML frontmatter for skill metadata (name, triggers, description)

#### Tools
- Filesystem tool: read, write, list, glob, grep operations
- Shell tool: command execution with safety deny-list
- Web tool: search and fetch capabilities
- Message tool: send messages to Telegram/CLI
- Spawn tool: create subagents for isolated long-running tasks
- Cron tool: schedule recurring tasks

#### Proactive Behavior
- **Cron scheduling service**: Background scheduler for periodic tasks
- **Heartbeat**: Periodic health checks and self-maintenance tasks
- Subagent runner for background task isolation

#### Channels
- **Telegram**: Full bot integration with commands and inline queries
- **CLI**: Interactive terminal mode with readline support

#### CLI & Configuration
- `joshbot onboard`: First-time setup wizard
- `joshbot agent`: Interactive CLI mode
- `joshbot gateway`: Telegram + all channels mode
- `joshbot status`: Show configuration and status
- `--force` flag: Force fresh onboarding (skips existing config check)
- `--keep-data` flag: Preserve memory and sessions during re-onboarding
- Configuration via `~/.joshbot/config.json` with `JOSHBOT_` env var prefix

### Changed

- Default model: `anthropic/claude-3.5-sonnet` via OpenRouter
- Default log level: WARNING (cleaner output)
- Session format: JSONL files in `~/.joshbot/sessions/`
- Architecture: Channel-based message bus (publish/subscribe pattern)

### Removed

- Python runtime dependency
- litellm library (replaced with native HTTP client)
- pip/pipx installation (Go binary distribution)
- Python virtual environment management
