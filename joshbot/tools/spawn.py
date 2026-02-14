"""Spawn tool for creating background subagents."""

from __future__ import annotations

from typing import Any, Callable, Awaitable

from loguru import logger

from .base import Tool


class SpawnTool(Tool):
    """Spawn a background subagent for long-running tasks."""

    def __init__(self, spawn_fn: Callable[..., Awaitable[str]] | None = None):
        self._spawn_fn = spawn_fn

    def set_spawn_fn(self, fn: Callable[..., Awaitable[str]]) -> None:
        """Set the spawn function (for deferred initialization)."""
        self._spawn_fn = fn

    @property
    def name(self) -> str:
        return "spawn"

    @property
    def description(self) -> str:
        return "Spawn a background task that runs independently. The task has access to tools but cannot send messages or spawn sub-tasks. Use for long-running operations."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "task": {
                    "type": "string",
                    "description": "Description of the task for the subagent to perform",
                },
                "context": {
                    "type": "string",
                    "description": "Additional context or instructions",
                    "default": "",
                },
            },
            "required": ["task"],
        }

    async def execute(self, task: str, context: str = "", **kwargs: Any) -> str:
        if not self._spawn_fn:
            return "Error: Spawn function not configured"

        try:
            result = await self._spawn_fn(task, context)
            return result
        except Exception as e:
            return f"Error spawning subagent: {e}"
