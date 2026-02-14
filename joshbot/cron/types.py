"""Cron job data types."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any


@dataclass
class CronJob:
    """A scheduled job."""

    id: str
    name: str
    schedule: str  # Cron expression or delay string
    message: str
    channel: str
    channel_id: str
    created_at: str = field(
        default_factory=lambda: datetime.now(timezone.utc).isoformat()
    )
    next_run: str = ""
    recurring: bool = False

    def to_dict(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "name": self.name,
            "schedule": self.schedule,
            "message": self.message,
            "channel": self.channel,
            "channel_id": self.channel_id,
            "created_at": self.created_at,
            "next_run": self.next_run,
            "recurring": self.recurring,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> CronJob:
        return cls(**{k: v for k, v in data.items() if k in cls.__dataclass_fields__})
