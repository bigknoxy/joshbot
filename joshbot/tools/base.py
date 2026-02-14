"""Abstract base class for tools with JSON Schema validation."""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Any

from loguru import logger


class Tool(ABC):
    """Base class for all joshbot tools."""

    @property
    @abstractmethod
    def name(self) -> str:
        """Tool name (used in function calling)."""
        ...

    @property
    @abstractmethod
    def description(self) -> str:
        """Human-readable description of what the tool does."""
        ...

    @property
    @abstractmethod
    def parameters(self) -> dict[str, Any]:
        """JSON Schema for tool parameters."""
        ...

    @abstractmethod
    async def execute(self, **kwargs: Any) -> str:
        """Execute the tool with validated parameters. Returns result as string."""
        ...

    def validate_params(self, params: dict[str, Any]) -> dict[str, Any]:
        """Validate parameters against the JSON Schema.

        Basic validation: checks required fields and types.
        Returns cleaned params dict.
        """
        schema = self.parameters
        required = schema.get("required", [])
        properties = schema.get("properties", {})

        # Check required fields
        for field in required:
            if field not in params:
                raise ValueError(f"Missing required parameter: {field}")

        # Filter to known properties only
        cleaned = {}
        for key, value in params.items():
            if key in properties:
                cleaned[key] = value

        return cleaned

    def to_schema(self) -> dict[str, Any]:
        """Convert to OpenAI function-calling schema format."""
        return {
            "type": "function",
            "function": {
                "name": self.name,
                "description": self.description,
                "parameters": self.parameters,
            },
        }
