"""System prompt and context builder."""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from loguru import logger


def build_system_prompt(
    workspace: str,
    skills_summary: str = "",
    memory_context: str = "",
    identity: dict[str, str] | None = None,
) -> str:
    """Build the full system prompt from workspace files and injected context.

    Args:
        workspace: Path to the workspace directory
        skills_summary: XML-formatted skill summaries (level 1)
        memory_context: Contents of MEMORY.md
        identity: Optional dict of identity file contents (agents, soul, user, tools, identity)
    """
    ws = Path(workspace)
    parts: list[str] = []

    # Core identity
    parts.append(_build_core_identity())

    # Bootstrap files from workspace
    if identity is None:
        identity = _load_identity_files(ws)

    for name, content in identity.items():
        if content.strip():
            parts.append(f"<{name}>\n{content.strip()}\n</{name}>")

    # Memory context (always loaded)
    if memory_context.strip():
        parts.append(f"<memory>\n{memory_context.strip()}\n</memory>")

    # Skills summary (level 1 - names and descriptions only)
    if skills_summary.strip():
        parts.append(f"<skills>\n{skills_summary.strip()}\n</skills>")

    # Current time
    now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    parts.append(f"<current_time>{now}</current_time>")

    return "\n\n".join(parts)


def _build_core_identity() -> str:
    """Build the core identity prompt."""
    return """You are joshbot, a personal AI assistant. You are helpful, capable, and proactive.

You have access to tools that let you interact with the filesystem, run shell commands, search the web, and manage your own memory and skills.

Key behaviors:
- Use your tools proactively to help the user
- Remember important information by updating your memory files
- When you learn something new or develop a useful capability, consider creating a skill for it
- Search your HISTORY.md when the user references past conversations
- Be concise but thorough in your responses
- If you're unsure about something, say so and suggest ways to find out

Memory system:
- MEMORY.md contains long-term facts about the user and context (always loaded)
- HISTORY.md is an append-only log of conversation summaries (searchable via grep)
- Use read_file and write_file to manage these files
- When conversations are consolidated, key facts go to MEMORY.md and summaries to HISTORY.md"""


def _load_identity_files(workspace: Path) -> dict[str, str]:
    """Load identity/bootstrap files from workspace."""
    files = {
        "agents": "AGENTS.md",
        "soul": "SOUL.md",
        "user": "USER.md",
        "tools": "TOOLS.md",
        "identity": "IDENTITY.md",
    }

    result: dict[str, str] = {}
    for key, filename in files.items():
        path = workspace / filename
        if path.exists():
            try:
                result[key] = path.read_text(encoding="utf-8")
            except Exception as e:
                logger.warning(f"Failed to read {filename}: {e}")

    return result


def format_tool_result(tool_call_id: str, name: str, result: str) -> dict[str, Any]:
    """Format a tool result as a message for the LLM."""
    return {
        "role": "tool",
        "tool_call_id": tool_call_id,
        "name": name,
        "content": result,
    }


def format_assistant_tool_calls(
    content: str, tool_calls: list[dict[str, Any]]
) -> dict[str, Any]:
    """Format an assistant message with tool calls."""
    msg: dict[str, Any] = {
        "role": "assistant",
        "content": content or None,
        "tool_calls": tool_calls,
    }
    return msg
