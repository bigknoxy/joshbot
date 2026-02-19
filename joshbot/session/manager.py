"""Session management with JSONL persistence."""

from __future__ import annotations

import json
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from loguru import logger


@dataclass
class Session:
    """A conversation session."""

    key: str
    messages: list[dict[str, Any]] = field(default_factory=list)
    created_at: float = field(default_factory=time.time)
    updated_at: float = field(default_factory=time.time)
    metadata: dict[str, Any] = field(default_factory=dict)


class SessionManager:
    """Manage conversation sessions with JSONL file persistence."""

    def __init__(self, sessions_dir: str | Path) -> None:
        self._dir = Path(sessions_dir)
        self._dir.mkdir(parents=True, exist_ok=True)
        self._cache: dict[str, Session] = {}

    def _session_path(self, key: str) -> Path:
        """Get file path for a session key."""
        # Sanitize key for filesystem
        safe_key = key.replace(":", "_").replace("/", "_")
        return self._dir / f"{safe_key}.jsonl"

    async def get_or_create(self, key: str) -> Session:
        """Get existing session or create a new one."""
        # Check cache first
        if key in self._cache:
            return self._cache[key]

        # Try to load from disk
        path = self._session_path(key)
        if path.exists():
            session = self._load_session(key, path)
            self._cache[key] = session
            return session

        # Create new session
        session = Session(key=key)
        self._cache[key] = session
        return session

    def _load_session(self, key: str, path: Path) -> Session:
        """Load a session from a JSONL file."""
        messages: list[dict[str, Any]] = []
        metadata: dict[str, Any] = {}
        created_at = time.time()

        try:
            with open(path, "r") as f:
                for line_num, line in enumerate(f):
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        data = json.loads(line)
                        if line_num == 0 and data.get("_type") == "metadata":
                            metadata = data
                            created_at = data.get("created_at", created_at)
                        else:
                            messages.append(data)
                    except json.JSONDecodeError:
                        logger.warning(f"Skipping malformed line in {path}")
        except Exception as e:
            logger.error(f"Failed to load session {key}: {e}")

        return Session(
            key=key,
            messages=messages,
            created_at=created_at,
            updated_at=time.time(),
            metadata=metadata,
        )

    async def save(self, session: Session) -> bool:
        """Save a session to disk as JSONL. Returns True if successful."""
        session.updated_at = time.time()
        path = self._session_path(session.key)

        # Limit message count to prevent huge files
        MAX_MESSAGES = 1000
        if len(session.messages) > MAX_MESSAGES:
            logger.warning(
                f"Session {session.key} has {len(session.messages)} messages, truncating to {MAX_MESSAGES}"
            )
            session.messages = session.messages[-MAX_MESSAGES:]

        try:
            # Atomic write: write to temp file then rename
            temp_path = path.with_suffix(".tmp")
            with open(temp_path, "w") as f:
                # Write metadata header
                metadata = {
                    "_type": "metadata",
                    "key": session.key,
                    "created_at": session.created_at,
                    "updated_at": session.updated_at,
                    **session.metadata,
                }
                f.write(json.dumps(metadata) + "\n")

                # Write messages
                for msg in session.messages:
                    f.write(json.dumps(msg) + "\n")

            # Atomic rename
            temp_path.rename(path)

            logger.debug(
                f"Saved session {session.key} ({len(session.messages)} messages)"
            )
            return True
        except Exception as e:
            logger.error(f"Failed to save session {session.key}: {e}")
            # Clean up temp file if it exists
            try:
                temp_path.unlink(missing_ok=True)
            except Exception:
                pass
            return False

    async def delete(self, key: str) -> None:
        """Delete a session."""
        if key in self._cache:
            del self._cache[key]

        path = self._session_path(key)
        if path.exists():
            path.unlink()
            logger.debug(f"Deleted session {key}")

    async def list_sessions(self) -> list[str]:
        """List all session keys."""
        keys = []
        for path in self._dir.glob("*.jsonl"):
            try:
                with open(path, "r") as f:
                    first_line = f.readline().strip()
                    if first_line:
                        data = json.loads(first_line)
                        keys.append(data.get("key", path.stem))
            except Exception:
                keys.append(path.stem)
        return keys
