"""Memory system: MEMORY.md (long-term) + HISTORY.md (event log)."""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path

from loguru import logger


class MemoryManager:
    """Manage the two-file memory system.

    MEMORY.md - Long-term facts (user preferences, project context, decisions).
                Always loaded into the system prompt.
                Updated atomically (full rewrite) during consolidation.

    HISTORY.md - Append-only event log of conversation summaries.
                 Never loaded into the system prompt (to save context).
                 Searchable via `grep -i "keyword" memory/HISTORY.md`.
    """

    def __init__(self, workspace: str | Path) -> None:
        self._workspace = Path(workspace)
        self._memory_dir = self._workspace / "memory"
        self._memory_dir.mkdir(parents=True, exist_ok=True)

    @property
    def memory_path(self) -> Path:
        return self._memory_dir / "MEMORY.md"

    @property
    def history_path(self) -> Path:
        return self._memory_dir / "HISTORY.md"

    async def get_memory_context(self) -> str:
        """Get the current long-term memory content.

        This is loaded into every system prompt.
        """
        if self.memory_path.exists():
            try:
                content = self.memory_path.read_text(encoding="utf-8").strip()
                if content:
                    return content
            except Exception as e:
                logger.error(f"Failed to read MEMORY.md: {e}")
        return ""

    async def write_long_term(self, content: str) -> None:
        """Write long-term memory (full overwrite).

        Called during memory consolidation. The LLM decides what to keep.
        """
        try:
            self.memory_path.write_text(content.strip() + "\n", encoding="utf-8")
            logger.info(f"Updated MEMORY.md ({len(content)} chars)")
        except Exception as e:
            logger.error(f"Failed to write MEMORY.md: {e}")

    async def append_history(self, entry: str) -> None:
        """Append an entry to the event log.

        Each entry should be 2-5 sentences summarizing a conversation.
        Timestamp is prepended automatically.
        """
        timestamp = datetime.now(timezone.utc).strftime("[%Y-%m-%d %H:%M]")
        formatted = f"\n{timestamp} {entry.strip()}\n"

        try:
            with open(self.history_path, "a", encoding="utf-8") as f:
                f.write(formatted)
            logger.debug(f"Appended to HISTORY.md: {entry[:80]}")
        except Exception as e:
            logger.error(f"Failed to append to HISTORY.md: {e}")

    async def get_history(self) -> str:
        """Get the full history log (for debugging/inspection)."""
        if self.history_path.exists():
            try:
                return self.history_path.read_text(encoding="utf-8")
            except Exception as e:
                logger.error(f"Failed to read HISTORY.md: {e}")
        return ""

    async def initialize(self) -> None:
        """Initialize memory files with templates if they don't exist."""
        if not self.memory_path.exists():
            template = """# Long-Term Memory

## User Information
- (joshbot will learn about you as you chat)

## Preferences
- (your preferences will be recorded here)

## Projects & Context
- (project details and context will be stored here)

## Important Notes
- (key decisions and notes will go here)
"""
            self.memory_path.write_text(template, encoding="utf-8")
            logger.info("Initialized MEMORY.md with template")

        if not self.history_path.exists():
            header = "# Conversation History\n\nEvent log of conversation summaries.\n"
            self.history_path.write_text(header, encoding="utf-8")
            logger.info("Initialized HISTORY.md")
