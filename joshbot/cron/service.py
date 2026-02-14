"""Cron service for scheduling tasks and reminders."""

from __future__ import annotations

import asyncio
import json
import re
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

from loguru import logger

from ..bus.events import InboundMessage
from ..bus.queue import MessageBus
from .types import CronJob


# Delay pattern: 5m, 2h, 1d
DELAY_PATTERN = re.compile(r"^(\d+)([mhd])$")


class CronService:
    """Timer-based job scheduler with JSON persistence."""

    def __init__(self, cron_dir: str | Path, bus: MessageBus) -> None:
        self._dir = Path(cron_dir)
        self._dir.mkdir(parents=True, exist_ok=True)
        self._bus = bus
        self._jobs: dict[str, CronJob] = {}
        self._timers: dict[str, asyncio.Task] = {}
        self._running = False

    @property
    def _jobs_file(self) -> Path:
        return self._dir / "jobs.json"

    async def start(self) -> None:
        """Start the cron service and schedule existing jobs."""
        self._running = True
        self._load_jobs()

        for job in self._jobs.values():
            await self._schedule_job(job)

        logger.info(f"Cron service started with {len(self._jobs)} jobs")

    async def stop(self) -> None:
        """Stop all scheduled jobs."""
        self._running = False
        for task in self._timers.values():
            task.cancel()
        self._timers.clear()
        logger.info("Cron service stopped")

    async def create_job(
        self,
        name: str,
        schedule: str,
        message: str,
        channel: str = "cli",
        channel_id: str = "cli:local",
    ) -> CronJob:
        """Create and schedule a new job."""
        job_id = str(uuid.uuid4())[:8]

        # Determine if recurring (cron expression) or one-time (delay)
        is_delay = bool(DELAY_PATTERN.match(schedule))

        next_run = ""
        if is_delay:
            delay_secs = self._parse_delay(schedule)
            next_run = (
                datetime.now(timezone.utc) + timedelta(seconds=delay_secs)
            ).isoformat()
        else:
            try:
                from croniter import croniter

                cron = croniter(schedule, datetime.now(timezone.utc))
                next_run = cron.get_next(datetime).isoformat()
            except Exception:
                next_run = "invalid schedule"

        job = CronJob(
            id=job_id,
            name=name,
            schedule=schedule,
            message=message,
            channel=channel,
            channel_id=channel_id,
            next_run=next_run,
            recurring=not is_delay,
        )

        self._jobs[job_id] = job
        self._save_jobs()
        await self._schedule_job(job)

        logger.info(f"Created cron job: {name} (id: {job_id})")
        return job

    async def delete_job(self, job_id: str) -> bool:
        """Delete a scheduled job."""
        if job_id in self._timers:
            self._timers[job_id].cancel()
            del self._timers[job_id]

        if job_id in self._jobs:
            del self._jobs[job_id]
            self._save_jobs()
            logger.info(f"Deleted cron job: {job_id}")
            return True
        return False

    async def list_jobs(self) -> list[CronJob]:
        """List all scheduled jobs."""
        return list(self._jobs.values())

    def _parse_delay(self, delay: str) -> float:
        """Parse a delay string (e.g., '30m', '2h', '1d') to seconds."""
        match = DELAY_PATTERN.match(delay)
        if not match:
            raise ValueError(f"Invalid delay format: {delay}")

        value = int(match.group(1))
        unit = match.group(2)

        multipliers = {"m": 60, "h": 3600, "d": 86400}
        return value * multipliers[unit]

    async def _schedule_job(self, job: CronJob) -> None:
        """Schedule a job's next execution."""
        if not self._running:
            return

        if DELAY_PATTERN.match(job.schedule):
            # One-time delay
            delay = self._parse_delay(job.schedule)
            self._timers[job.id] = asyncio.create_task(self._run_after(job, delay))
        else:
            # Recurring cron
            self._timers[job.id] = asyncio.create_task(self._run_cron(job))

    async def _run_after(self, job: CronJob, delay: float) -> None:
        """Run a one-time job after a delay."""
        try:
            await asyncio.sleep(delay)
            await self._execute_job(job)
            # Clean up one-time jobs
            await self.delete_job(job.id)
        except asyncio.CancelledError:
            pass
        except Exception as e:
            logger.error(f"Cron job error ({job.id}): {e}")

    async def _run_cron(self, job: CronJob) -> None:
        """Run a recurring cron job."""
        try:
            from croniter import croniter

            while self._running:
                cron = croniter(job.schedule, datetime.now(timezone.utc))
                next_time = cron.get_next(datetime)
                delay = (next_time - datetime.now(timezone.utc)).total_seconds()

                if delay > 0:
                    await asyncio.sleep(delay)

                await self._execute_job(job)

                # Update next run
                job.next_run = cron.get_next(datetime).isoformat()
                self._save_jobs()

        except asyncio.CancelledError:
            pass
        except Exception as e:
            logger.error(f"Cron job error ({job.id}): {e}")

    async def _execute_job(self, job: CronJob) -> None:
        """Execute a job by sending its message through the bus."""
        logger.info(f"Executing cron job: {job.name}")
        await self._bus.publish_inbound(
            InboundMessage(
                channel="cron",
                channel_id=job.channel_id,
                sender_id="cron",
                sender_name=f"Cron: {job.name}",
                content=f"[Scheduled Task: {job.name}] {job.message}",
            )
        )

    def _load_jobs(self) -> None:
        """Load jobs from disk."""
        if self._jobs_file.exists():
            try:
                data = json.loads(self._jobs_file.read_text())
                for job_data in data:
                    job = CronJob.from_dict(job_data)
                    self._jobs[job.id] = job
            except Exception as e:
                logger.error(f"Failed to load cron jobs: {e}")

    def _save_jobs(self) -> None:
        """Save jobs to disk."""
        try:
            data = [job.to_dict() for job in self._jobs.values()]
            self._jobs_file.write_text(json.dumps(data, indent=2))
        except Exception as e:
            logger.error(f"Failed to save cron jobs: {e}")
