"""Heartbeat service for proactive agent wake-ups."""

from __future__ import annotations

import asyncio
from pathlib import Path

from loguru import logger

from ..bus.events import InboundMessage
from ..bus.queue import MessageBus


class HeartbeatService:
    """Periodically reads HEARTBEAT.md and triggers agent processing.

    This enables the agent to perform proactive tasks without user input.
    Users can add tasks to HEARTBEAT.md, and the agent will process them
    on the next heartbeat cycle (default: every 30 minutes).
    """

    def __init__(
        self,
        workspace: str | Path,
        bus: MessageBus,
        interval: int = 1800,  # 30 minutes
    ) -> None:
        self._workspace = Path(workspace)
        self._bus = bus
        self._interval = interval
        self._running = False
        self._task: asyncio.Task | None = None

    @property
    def heartbeat_path(self) -> Path:
        return self._workspace / "HEARTBEAT.md"

    async def start(self) -> None:
        """Start the heartbeat loop."""
        self._running = True
        self._task = asyncio.create_task(self._loop())
        logger.info(f"Heartbeat service started (interval: {self._interval}s)")

    async def stop(self) -> None:
        """Stop the heartbeat loop."""
        self._running = False
        if self._task:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
        logger.info("Heartbeat service stopped")

    async def _loop(self) -> None:
        """Main heartbeat loop."""
        while self._running:
            try:
                await asyncio.sleep(self._interval)
                await self._check_heartbeat()
            except asyncio.CancelledError:
                break
            except Exception as e:
                logger.error(f"Heartbeat error: {e}")

    async def _check_heartbeat(self) -> None:
        """Read HEARTBEAT.md and process any actionable content."""
        if not self.heartbeat_path.exists():
            return

        try:
            content = self.heartbeat_path.read_text(encoding="utf-8").strip()
            if not content or content == self._get_empty_template().strip():
                return

            # Check for actionable items (lines starting with - [ ])
            lines = content.splitlines()
            has_tasks = any(
                line.strip().startswith("- [ ]") or line.strip().startswith("- []")
                for line in lines
            )

            if has_tasks:
                logger.info("Heartbeat: found actionable tasks")
                await self._bus.publish_inbound(
                    InboundMessage(
                        channel="heartbeat",
                        channel_id="heartbeat:system",
                        sender_id="system",
                        sender_name="Heartbeat",
                        content=f"[Heartbeat] Please review and process the following tasks from HEARTBEAT.md:\n\n{content}",
                    )
                )
        except Exception as e:
            logger.error(f"Failed to read HEARTBEAT.md: {e}")

    def _get_empty_template(self) -> str:
        return """# Heartbeat Tasks

Add tasks here for joshbot to process automatically.
Use checkbox format:

- [ ] Example task to process
- [x] Completed task (will be ignored)
"""

    async def initialize(self) -> None:
        """Create HEARTBEAT.md template if it doesn't exist."""
        if not self.heartbeat_path.exists():
            self.heartbeat_path.write_text(self._get_empty_template(), encoding="utf-8")
            logger.info("Initialized HEARTBEAT.md")
