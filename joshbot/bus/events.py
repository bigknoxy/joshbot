"""Message bus event types."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any


@dataclass
class InboundMessage:
    """A message received from a chat channel."""

    channel: str  # e.g., "telegram", "cli"
    channel_id: str  # e.g., "telegram:12345", "cli:local"
    sender_id: str
    sender_name: str
    content: str
    timestamp: datetime = field(default_factory=lambda: datetime.now(timezone.utc))
    attachments: list[dict[str, Any]] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)

    @property
    def session_key(self) -> str:
        """Unique session key for this conversation."""
        return self.channel_id


@dataclass
class OutboundMessage:
    """A message to send to a chat channel."""

    channel: str
    channel_id: str
    content: str
    timestamp: datetime = field(default_factory=lambda: datetime.now(timezone.utc))
    metadata: dict[str, Any] = field(default_factory=dict)
