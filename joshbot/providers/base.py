"""LLM provider abstractions."""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any


@dataclass
class ToolCallRequest:
    """A tool call requested by the LLM."""

    id: str
    name: str
    arguments: dict[str, Any]


@dataclass
class LLMResponse:
    """Response from an LLM provider."""

    content: str = ""
    tool_calls: list[ToolCallRequest] = field(default_factory=list)
    finish_reason: str = ""
    usage: dict[str, int] = field(default_factory=dict)

    @property
    def has_tool_calls(self) -> bool:
        return len(self.tool_calls) > 0


class LLMProvider(ABC):
    """Abstract base class for LLM providers."""

    @abstractmethod
    async def chat(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]] | None = None,
        model: str = "",
        max_tokens: int = 8192,
        temperature: float = 0.7,
    ) -> LLMResponse:
        """Send a chat completion request."""
        ...

    @abstractmethod
    async def transcribe(self, audio_path: str) -> str:
        """Transcribe an audio file to text."""
        ...
