"""Abstract base class for chat channels."""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ..bus.queue import MessageBus


class BaseChannel(ABC):
    """Base class for all chat channels."""

    def __init__(self, bus: "MessageBus") -> None:
        self._bus = bus

    @property
    @abstractmethod
    def name(self) -> str:
        """Channel name (e.g., 'telegram', 'cli')."""
        ...

    @abstractmethod
    async def start(self) -> None:
        """Start the channel (connect, begin listening)."""
        ...

    @abstractmethod
    async def stop(self) -> None:
        """Stop the channel (disconnect, clean up)."""
        ...

    @abstractmethod
    async def send(self, channel_id: str, content: str) -> None:
        """Send a message to a specific target on this channel."""
        ...

    def is_allowed(self, sender_id: str) -> bool:
        """Check if a sender is allowed (default: allow all)."""
        return True
