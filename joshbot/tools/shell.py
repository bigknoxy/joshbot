"""Shell execution tool with safety guards."""

from __future__ import annotations

import asyncio
import re
from typing import Any

from loguru import logger

from .base import Tool


# Dangerous command patterns (deny-list)
DANGEROUS_PATTERNS = [
    r"rm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?/\s*$",  # rm -rf /
    r"rm\s+-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\s+/",  # rm -rf /...
    r">\s*/dev/[sh]d[a-z]",  # write to raw disk
    r"dd\s+.*of=/dev/",  # dd to device
    r"mkfs\.",  # format filesystem
    r":()\s*\{\s*:\|:\s*&\s*\}\s*;:",  # fork bomb
    r"\bshutdown\b",  # shutdown
    r"\breboot\b",  # reboot
    r"\bhalt\b",  # halt
    r"\binit\s+0\b",  # init 0
    r"\bformat\s+[cCdD]:",  # Windows format
    r"chmod\s+-R\s+777\s+/",  # chmod 777 /
    r"chown\s+-R\s+.*\s+/\s*$",  # chown -R ... /
]

COMPILED_PATTERNS = [re.compile(p) for p in DANGEROUS_PATTERNS]

MAX_OUTPUT = 10000  # chars


class ShellTool(Tool):
    """Execute shell commands with safety guards."""

    def __init__(self, timeout: int = 60, workspace: str = "", restrict: bool = False):
        self._timeout = timeout
        self._workspace = workspace
        self._restrict = restrict

    @property
    def name(self) -> str:
        return "exec"

    @property
    def description(self) -> str:
        return "Execute a shell command and return its output. Use for running scripts, git, grep, and system commands."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "command": {
                    "type": "string",
                    "description": "Shell command to execute",
                },
                "timeout": {
                    "type": "integer",
                    "description": f"Timeout in seconds (default: {self._timeout})",
                },
            },
            "required": ["command"],
        }

    def _is_dangerous(self, command: str) -> str | None:
        """Check if command matches any dangerous pattern. Returns reason or None."""
        for pattern in COMPILED_PATTERNS:
            if pattern.search(command):
                return f"Blocked: command matches dangerous pattern '{pattern.pattern}'"
        return None

    async def execute(
        self, command: str, timeout: int | None = None, **kwargs: Any
    ) -> str:
        # Safety check
        danger = self._is_dangerous(command)
        if danger:
            logger.warning(f"Blocked dangerous command: {command}")
            return f"Error: {danger}"

        effective_timeout = timeout or self._timeout
        cwd = self._workspace if self._workspace else None

        try:
            process = await asyncio.create_subprocess_shell(
                command,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                cwd=cwd,
            )

            stdout, stderr = await asyncio.wait_for(
                process.communicate(),
                timeout=effective_timeout,
            )

            output = stdout.decode("utf-8", errors="replace")
            errors = stderr.decode("utf-8", errors="replace")

            result = ""
            if output:
                result += output
            if errors:
                result += f"\n[stderr]\n{errors}" if result else errors
            if not result:
                result = "(no output)"

            # Truncate if too long
            if len(result) > MAX_OUTPUT:
                result = (
                    result[:MAX_OUTPUT]
                    + f"\n\n... (truncated, {len(result) - MAX_OUTPUT} more chars)"
                )

            exit_info = (
                f"\n[exit code: {process.returncode}]"
                if process.returncode != 0
                else ""
            )
            return result + exit_info

        except asyncio.TimeoutError:
            return f"Error: Command timed out after {effective_timeout}s"
        except Exception as e:
            return f"Error executing command: {e}"
