# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.9.0] - 2026-02-23

### Added

#### Exa Web Search Integration
- New `exa` tool with comprehensive web search capabilities via exa-cli
- Fallback chain: `exa_web_search` → `perplexity_search` → `brave_search` → `websearch`
- New web operation types:
  - `web_code`: Search code examples and programming solutions (exa-code)
  - `web_company`: Research company information and news (exa-company)
  - `web_research`: Deep research with multi-source analysis (perplexity_research)
- Live crawl modes: fallback (default) and preferred
- Configurable result counts and search depth

## [1.8.2] - 2026-02-22

### Fixed

- NVIDIA API URL configuration now properly reads from config

## [1.8.1] - 2026-02-22

### Fixed

- Bug fixes and stability improvements

## [1.8.0] - 2026-02-21

### Added

- Model context overrides: ability to specify different models for different contexts
- Per-skill model selection capability
- Context-aware model routing

### Changed

- Improved model configuration flexibility

## [1.7.0] - 2026-02-20

### Added

- Agent telemetry improvements for debugging
- Enhanced logging and tracing for tool execution
- Performance metrics collection

### Changed

- Improved observability of agent decision-making process

## [1.6.0] - 2026-02-19

### Added

- Memory persistence improvements
- Token budgeting system for context management
- Better handling of large conversations
- Automatic context compression for small models

### Changed

- Improved memory consolidation during idle periods

## [1.5.0] - 2026-02-18

### Added

- Auto-restart after update: joshbot automatically restarts after self-update
- Automatic binary refresh when new version is available

### Changed

- Update process now handles restart automatically

## [1.4.0] - 2026-02-17

### Added

- NVIDIA NIM API provider support
- New `configure nvidia` command for NVIDIA API configuration
- Service uninstall command: `joshbot service uninstall`

### Changed

- Improved provider configuration system
- Better error handling for provider failures

## [1.3.0] - 2026-02-16

### Added

- MultiProvider fallback system: automatic failover when primary provider fails
- New `configure` command for runtime configuration changes
- `configure provider`: Switch between different LLM providers
- `configure model`: Change the default model
- `configure api-key`: Update API keys
- Provider health checking and automatic fallback

### Changed

- Improved provider selection and configuration flow

## [1.2.2] - 2026-02-15

### Added

- User name personalization: bot addresses users by name
- Improved user context tracking

### Changed

- Better personalization based on user preferences

## [1.2.1] - 2026-02-15

### Fixed

- Filesystem tool improvements
- Better error handling for file operations
- Improved path resolution

## [1.2.0] - 2026-02-14

### Added

- Default model changes: updated to newer model versions
- New `update` command for self-updating joshbot
- Exa search integration as primary web search tool

### Changed

- Improved web search capabilities with Exa API
- Updated default provider configuration

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
