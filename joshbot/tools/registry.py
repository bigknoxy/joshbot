"""Dynamic tool registry for managing available tools."""

from __future__ import annotations

from typing import Any

from loguru import logger

from .base import Tool


class ToolRegistry:
    """Registry for dynamically managing tools."""

    def __init__(self) -> None:
        self._tools: dict[str, Tool] = {}

    def register(self, tool: Tool) -> None:
        """Register a tool."""
        self._tools[tool.name] = tool
        logger.debug(f"Registered tool: {tool.name}")

    def unregister(self, name: str) -> None:
        """Unregister a tool by name."""
        if name in self._tools:
            del self._tools[name]
            logger.debug(f"Unregistered tool: {name}")

    def get(self, name: str) -> Tool | None:
        """Get a tool by name."""
        return self._tools.get(name)

    def list_tools(self) -> list[Tool]:
        """List all registered tools."""
        return list(self._tools.values())

    def get_schemas(self) -> list[dict[str, Any]]:
        """Get OpenAI function-calling schemas for all tools."""
        return [tool.to_schema() for tool in self._tools.values()]

    async def execute(self, name: str, arguments: dict[str, Any]) -> str:
        """Execute a tool by name with given arguments.

        Returns the tool's result as a string.
        Raises ValueError if tool not found.
        """
        tool = self._tools.get(name)
        if not tool:
            return f"Error: Unknown tool '{name}'. Available tools: {', '.join(self._tools.keys())}"

        try:
            params = tool.validate_params(arguments)
            result = await tool.execute(**params)
            logger.debug(f"Tool {name} executed successfully")
            return result
        except Exception as e:
            error_msg = f"Tool '{name}' error: {type(e).__name__}: {e}"
            logger.error(error_msg)
            return error_msg

    def __len__(self) -> int:
        return len(self._tools)

    def __contains__(self, name: str) -> bool:
        return name in self._tools
