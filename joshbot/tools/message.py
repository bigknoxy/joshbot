"""Message tool for sending messages to chat channels."""

from __future__ import annotations

from typing import Any, TYPE_CHECKING

from loguru import logger

from .base import Tool

if TYPE_CHECKING:
    from ..bus.queue import MessageBus


class MessageTool(Tool):
    """Send a message to a chat channel."""

    def __init__(self, bus: "MessageBus | None" = None):
        self._bus = bus

    def set_bus(self, bus: "MessageBus") -> None:
        """Set the message bus (for deferred initialization)."""
        self._bus = bus

    @property
    def name(self) -> str:
        return "message"

    @property
    def description(self) -> str:
        return "Send a message to a specific chat channel. Use this to proactively communicate."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "channel": {
                    "type": "string",
                    "description": "Channel name (e.g., 'telegram', 'cli')",
                },
                "channel_id": {
                    "type": "string",
                    "description": "Channel-specific target ID (e.g., 'telegram:12345')",
                },
                "content": {"type": "string", "description": "Message content to send"},
            },
            "required": ["channel", "channel_id", "content"],
        }

    async def execute(
        self, channel: str, channel_id: str, content: str, **kwargs: Any
    ) -> str:
        if not self._bus:
            return "Error: Message bus not available"

        from ..bus.events import OutboundMessage

        await self._bus.publish_outbound(
            OutboundMessage(
                channel=channel,
                channel_id=channel_id,
                content=content,
            )
        )
        return f"Message sent to {channel}:{channel_id}"
