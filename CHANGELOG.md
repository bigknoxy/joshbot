# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-02-14

### Added
- `joshbot update` command for self-updating (supports pipx, pip, and source installs)
- `joshbot update --check` to check for available updates without installing
- `joshbot --version` flag to display current version
- Version display in `joshbot status` output
- Config schema versioning with automatic migration framework
- CHANGELOG.md for tracking release history

### Changed
- Default model changed to `arcee-ai/trinity-large-preview:free` (previous default `google/gemma-2-9b-it:free` was removed from OpenRouter)
- Default log level set to WARNING (was DEBUG) for cleaner console output
- Version is now single source of truth from `joshbot/__init__.py` (pyproject.toml reads it dynamically)
- Updated README with `joshbot update` as primary upgrade method and Docker upgrade instructions

### Fixed
- Suppressed litellm banner messages ("Provider List", "Give Feedback") that cluttered console output
- Removed reference to defunct `google/gemma-2-9b-it:free` model

## [0.1.0] - 2026-02-01

### Added
- Initial release
- ReAct agent loop with multi-provider LLM support via litellm
- Async message bus architecture
- Telegram channel integration
- CLI interactive mode
- Tool system: filesystem, shell, web search/fetch, message, spawn, cron
- Self-learning memory (MEMORY.md + HISTORY.md)
- Skill self-creation system
- Session management (JSONL)
- Configuration via `~/.joshbot/config.json` with environment variable support
- First-time onboarding flow (`joshbot onboard`)
- Docker support with docker-compose
- pipx-based installation via install.sh
