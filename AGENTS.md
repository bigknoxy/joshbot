# AGENTS.md - Coding Agent Guidelines for joshbot

## Project Overview

joshbot is a lightweight personal AI assistant (~3,600 LOC Python) with self-learning
memory, skill self-creation, and Telegram integration. Architecture: async message bus
decoupling chat channels from a ReAct agent loop backed by multi-provider LLM via litellm.

## Build & Run Commands

```bash
# Install (use a venv)
python3 -m venv .venv && source .venv/bin/activate
pip install -e .

# Run
joshbot onboard          # First-time setup
joshbot agent            # Interactive CLI mode
joshbot gateway          # Telegram + all channels
joshbot status           # Show config/status

# Docker
docker build -t joshbot .
docker compose up -d
```

## Testing

No test framework is configured yet. When adding tests:

```bash
pip install pytest pytest-asyncio
pytest                          # Run all tests
pytest tests/test_tools.py      # Run one file
pytest tests/test_tools.py::test_shell_safety -xvs  # Run single test, verbose, no capture
```

Place tests in `tests/` with `test_` prefix. Use `pytest-asyncio` for async tests.
Most components are testable in isolation (tools, config, bus, session, memory, skills).

## Linting & Formatting

Ruff is the preferred linter/formatter (cache exists for v0.12.5). No config is committed
yet. When running:

```bash
pip install ruff mypy
ruff check .               # Lint
ruff format .              # Format
ruff check --fix .         # Autofix
mypy joshbot/              # Type checking
```

## Code Style

### Module Structure

Every `.py` file follows this order:
1. Module docstring (single line)
2. `from __future__ import annotations`
3. Stdlib imports
4. Third-party imports (blank line separator)
5. Local imports using **relative paths** (blank line separator)

```python
"""Shell execution tool with safety guards."""

from __future__ import annotations

import asyncio
import re
from typing import Any

from loguru import logger

from .base import Tool
```

### Imports

- **Always relative** for local imports (`from ..bus.events import ...`, `from .base import ...`)
- **Never absolute** (`from joshbot.bus.events import ...` -- don't do this)
- **Lazy imports** for heavy/optional deps inside functions (litellm, telegram, httpx, readability)
- **`TYPE_CHECKING` guard** for imports only needed at type-check time (avoids circular imports)

### Type Annotations

- **Required** on all function/method signatures (parameters and return types)
- Modern syntax enabled by `from __future__ import annotations`:
  - `str | None` not `Optional[str]`
  - `list[str]` not `List[str]`
  - `dict[str, Any]` not `Dict[str, Any]`
- Use `-> None` for void returns
- Use `Any` sparingly (LLM response data, generic dicts)

### Naming Conventions

| Element          | Convention         | Example                          |
|------------------|--------------------|----------------------------------|
| Classes          | PascalCase         | `AgentLoop`, `WebFetchTool`      |
| Functions/methods| snake_case         | `build_system_prompt`            |
| Private members  | `_prefix`          | `self._config`, `self._running`  |
| Private methods  | `_prefix`          | `_parse_response`, `_resolve_path`|
| Constants        | UPPER_SNAKE_CASE   | `MAX_OUTPUT`, `DANGEROUS_PATTERNS`|
| Module variables | lowercase          | `app`, `console`                 |

### Data Modeling

- **`@dataclass`** for internal data types (InboundMessage, LLMResponse, Session, CronJob, etc.)
- **Pydantic `BaseModel`** for config/validation only (Config, ProviderConfig, TelegramConfig)
- Root `Config` extends `pydantic_settings.BaseSettings` for env var support

### Interfaces & Extension Points

- **`abc.ABC` + `@abstractmethod`** for base classes: `Tool`, `BaseChannel`, `LLMProvider`
- **Registry pattern** for tools (`ToolRegistry`) and providers (`PROVIDERS` dict)
- **Deferred initialization** via `set_*()` methods when dependencies aren't available at construction

### Async Patterns

- **Async-first**: all I/O-bound operations are `async def`
- `asyncio.Queue` for message bus decoupling
- `asyncio.create_task()` for background work (typing indicators, cron timers, heartbeat)
- `asyncio.gather()` for concurrent service startup
- `asyncio.wait_for()` with timeouts for queue processing and shell execution
- `run_in_executor(None, ...)` for blocking operations (stdin input)

### Error Handling

- **Try/except with loguru** at service boundaries -- never silently swallow errors
- **Tools return error strings** (`return f"Error: File not found: {path}"`) -- never raise
- **Graceful degradation** with fallbacks (rich -> plain text, HTML -> plain text)
- **No custom exception classes** -- use standard Python exceptions
- **`asyncio.CancelledError`** handled explicitly in long-running loops

### Logging

- **loguru** exclusively (`from loguru import logger`) -- no stdlib `logging`
- `logger.debug()` for routine operations
- `logger.info()` for significant events (tool execution, service start/stop)
- `logger.warning()` for recoverable issues
- `logger.error()` for failures

### String Formatting

- **f-strings exclusively** -- no `.format()` or `%` formatting

### Docstrings

- Google-style when multi-line (with `Args:` sections)
- Single-line for simple classes/methods
- Module docstrings required (single line at top of every file)

## Architecture Quick Reference

```
channels/ --> bus/MessageBus --> agent/AgentLoop --> providers/LiteLLMProvider
(CLI,          (async queues)    (ReAct loop)        (litellm -> LLM API)
 Telegram)                           |
                                tools/ToolRegistry
                                (filesystem, shell,
                                 web, message, cron)
```

- **Message bus** decouples channels from agent via `InboundMessage`/`OutboundMessage`
- **ReAct loop**: LLM -> tool calls -> reflect -> repeat (max 20 iterations)
- **Memory**: `MEMORY.md` (always in context) + `HISTORY.md` (grep-searchable event log)
- **Skills**: Markdown files with YAML frontmatter, progressive loading (summary -> full content)
- **Sessions**: JSONL files in `~/.joshbot/sessions/`
- **Config**: `~/.joshbot/config.json`, Pydantic-validated, env vars with `JOSHBOT_` prefix

## Key Files

| File | Purpose |
|------|---------|
| `joshbot/main.py` | CLI entry point, service wiring, onboard flow |
| `joshbot/agent/loop.py` | Core ReAct agent loop, memory consolidation |
| `joshbot/agent/context.py` | System prompt assembly |
| `joshbot/agent/memory.py` | MEMORY.md + HISTORY.md management |
| `joshbot/agent/skills.py` | Skill discovery and progressive loading |
| `joshbot/tools/base.py` | Tool ABC (implement this to add new tools) |
| `joshbot/tools/shell.py` | Shell exec with safety deny-list |
| `joshbot/channels/base.py` | Channel ABC (implement this to add new channels) |
| `joshbot/config/schema.py` | All Pydantic config models |
| `joshbot/bus/queue.py` | Async message bus |

## Adding New Components

**New tool**: Create `joshbot/tools/my_tool.py`, extend `Tool` ABC (implement `name`,
`description`, `parameters`, `execute`), register in `_build_tools()` in `main.py`.

**New channel**: Create `joshbot/channels/my_channel.py`, extend `BaseChannel` (implement
`name`, `start`, `stop`, `send`), add setup logic in `ChannelManager.setup_channels()`.

**New skill**: Create `skills/{name}/SKILL.md` with YAML frontmatter. Auto-discovered.

## Python Version

Requires **Python 3.11+**. Uses modern syntax: union types (`X | Y`), generic builtins
(`list[str]`), `match` statements are permitted.
