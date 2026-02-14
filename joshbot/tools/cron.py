"""Cron tool for scheduling tasks and reminders."""

from __future__ import annotations

from typing import Any, TYPE_CHECKING

from loguru import logger

from .base import Tool

if TYPE_CHECKING:
    from ..cron.service import CronService


class CronTool(Tool):
    """Schedule reminders and recurring tasks."""

    def __init__(self, cron_service: "CronService | None" = None):
        self._cron = cron_service

    def set_cron_service(self, service: "CronService") -> None:
        """Set the cron service (for deferred initialization)."""
        self._cron = service

    @property
    def name(self) -> str:
        return "cron"

    @property
    def description(self) -> str:
        return "Schedule a reminder or recurring task. Supports one-time delays and cron expressions."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "action": {
                    "type": "string",
                    "enum": ["create", "list", "delete"],
                    "description": "Action to perform",
                },
                "name": {
                    "type": "string",
                    "description": "Name/description of the scheduled task",
                },
                "schedule": {
                    "type": "string",
                    "description": "Cron expression (e.g., '*/5 * * * *') or delay (e.g., '30m', '2h', '1d')",
                },
                "message": {
                    "type": "string",
                    "description": "Message to deliver when the task triggers",
                },
                "channel": {
                    "type": "string",
                    "description": "Channel to send the message to",
                },
                "channel_id": {"type": "string", "description": "Channel target ID"},
                "job_id": {
                    "type": "string",
                    "description": "Job ID (for delete action)",
                },
            },
            "required": ["action"],
        }

    async def execute(self, action: str, **kwargs: Any) -> str:
        if not self._cron:
            return "Error: Cron service not available"

        if action == "create":
            name = kwargs.get("name", "Unnamed task")
            schedule = kwargs.get("schedule", "")
            message = kwargs.get("message", name)
            channel = kwargs.get("channel", "cli")
            channel_id = kwargs.get("channel_id", "cli:local")

            if not schedule:
                return "Error: schedule is required for create action"

            job = await self._cron.create_job(
                name=name,
                schedule=schedule,
                message=message,
                channel=channel,
                channel_id=channel_id,
            )
            return f"Scheduled: '{name}' (id: {job.id}, schedule: {schedule})"

        elif action == "list":
            jobs = await self._cron.list_jobs()
            if not jobs:
                return "No scheduled tasks."
            lines = [
                f"- {j.name} (id: {j.id}, schedule: {j.schedule}, next: {j.next_run})"
                for j in jobs
            ]
            return "Scheduled tasks:\n" + "\n".join(lines)

        elif action == "delete":
            job_id = kwargs.get("job_id", "")
            if not job_id:
                return "Error: job_id is required for delete action"
            success = await self._cron.delete_job(job_id)
            return f"Deleted job {job_id}" if success else f"Job {job_id} not found"

        return f"Unknown action: {action}"
