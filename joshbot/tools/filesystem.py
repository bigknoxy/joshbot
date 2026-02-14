"""Filesystem tools: read, write, edit, list."""

from __future__ import annotations

import os
from pathlib import Path
from typing import Any

from loguru import logger

from .base import Tool


class ReadFileTool(Tool):
    """Read contents of a file."""

    def __init__(self, workspace: str = "", restrict: bool = False):
        self._workspace = workspace
        self._restrict = restrict

    @property
    def name(self) -> str:
        return "read_file"

    @property
    def description(self) -> str:
        return "Read the contents of a file. Returns the file content as text."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "Path to the file to read"},
                "offset": {
                    "type": "integer",
                    "description": "Line number to start from (0-indexed)",
                    "default": 0,
                },
                "limit": {
                    "type": "integer",
                    "description": "Maximum number of lines to read",
                    "default": 500,
                },
            },
            "required": ["path"],
        }

    def _resolve_path(self, path: str) -> Path:
        p = Path(path).expanduser()
        if not p.is_absolute() and self._workspace:
            p = Path(self._workspace) / p
        if self._restrict and self._workspace:
            resolved = p.resolve()
            ws = Path(self._workspace).resolve()
            if not str(resolved).startswith(str(ws)):
                raise PermissionError(f"Access denied: {path} is outside workspace")
        return p

    async def execute(
        self, path: str, offset: int = 0, limit: int = 500, **kwargs: Any
    ) -> str:
        try:
            p = self._resolve_path(path)
            if not p.exists():
                return f"Error: File not found: {path}"
            if not p.is_file():
                return f"Error: Not a file: {path}"

            content = p.read_text(encoding="utf-8", errors="replace")
            lines = content.splitlines()
            total = len(lines)
            selected = lines[offset : offset + limit]

            result = "\n".join(
                f"{i + offset + 1}: {line}" for i, line in enumerate(selected)
            )
            if offset + limit < total:
                result += f"\n\n... ({total - offset - limit} more lines)"
            return result
        except PermissionError as e:
            return f"Error: {e}"
        except Exception as e:
            return f"Error reading file: {e}"


class WriteFileTool(Tool):
    """Write content to a file."""

    def __init__(self, workspace: str = "", restrict: bool = False):
        self._workspace = workspace
        self._restrict = restrict

    @property
    def name(self) -> str:
        return "write_file"

    @property
    def description(self) -> str:
        return "Write content to a file. Creates parent directories if needed. Overwrites existing content."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "Path to the file to write"},
                "content": {
                    "type": "string",
                    "description": "Content to write to the file",
                },
            },
            "required": ["path", "content"],
        }

    def _resolve_path(self, path: str) -> Path:
        p = Path(path).expanduser()
        if not p.is_absolute() and self._workspace:
            p = Path(self._workspace) / p
        if self._restrict and self._workspace:
            resolved = p.resolve()
            ws = Path(self._workspace).resolve()
            if not str(resolved).startswith(str(ws)):
                raise PermissionError(f"Access denied: {path} is outside workspace")
        return p

    async def execute(self, path: str, content: str, **kwargs: Any) -> str:
        try:
            p = self._resolve_path(path)
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(content, encoding="utf-8")
            return f"Successfully wrote {len(content)} bytes to {path}"
        except PermissionError as e:
            return f"Error: {e}"
        except Exception as e:
            return f"Error writing file: {e}"


class EditFileTool(Tool):
    """Edit a file with find-and-replace."""

    def __init__(self, workspace: str = "", restrict: bool = False):
        self._workspace = workspace
        self._restrict = restrict

    @property
    def name(self) -> str:
        return "edit_file"

    @property
    def description(self) -> str:
        return "Edit a file by replacing an exact text match. The old_text must match exactly (including whitespace)."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "Path to the file to edit"},
                "old_text": {
                    "type": "string",
                    "description": "Exact text to find and replace",
                },
                "new_text": {"type": "string", "description": "Text to replace with"},
            },
            "required": ["path", "old_text", "new_text"],
        }

    def _resolve_path(self, path: str) -> Path:
        p = Path(path).expanduser()
        if not p.is_absolute() and self._workspace:
            p = Path(self._workspace) / p
        if self._restrict and self._workspace:
            resolved = p.resolve()
            ws = Path(self._workspace).resolve()
            if not str(resolved).startswith(str(ws)):
                raise PermissionError(f"Access denied: {path} is outside workspace")
        return p

    async def execute(
        self, path: str, old_text: str, new_text: str, **kwargs: Any
    ) -> str:
        try:
            p = self._resolve_path(path)
            if not p.exists():
                return f"Error: File not found: {path}"

            content = p.read_text(encoding="utf-8")
            count = content.count(old_text)

            if count == 0:
                return f"Error: old_text not found in {path}"
            if count > 1:
                return f"Error: Found {count} matches for old_text. Provide more context to make it unique."

            new_content = content.replace(old_text, new_text, 1)
            p.write_text(new_content, encoding="utf-8")
            return f"Successfully edited {path}"
        except PermissionError as e:
            return f"Error: {e}"
        except Exception as e:
            return f"Error editing file: {e}"


class ListDirTool(Tool):
    """List directory contents."""

    def __init__(self, workspace: str = "", restrict: bool = False):
        self._workspace = workspace
        self._restrict = restrict

    @property
    def name(self) -> str:
        return "list_dir"

    @property
    def description(self) -> str:
        return "List contents of a directory. Shows files and subdirectories."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "path": {
                    "type": "string",
                    "description": "Path to the directory to list",
                    "default": ".",
                },
            },
            "required": [],
        }

    def _resolve_path(self, path: str) -> Path:
        p = Path(path).expanduser()
        if not p.is_absolute() and self._workspace:
            p = Path(self._workspace) / p
        if self._restrict and self._workspace:
            resolved = p.resolve()
            ws = Path(self._workspace).resolve()
            if not str(resolved).startswith(str(ws)):
                raise PermissionError(f"Access denied: {path} is outside workspace")
        return p

    async def execute(self, path: str = ".", **kwargs: Any) -> str:
        try:
            p = self._resolve_path(path)
            if not p.exists():
                return f"Error: Directory not found: {path}"
            if not p.is_dir():
                return f"Error: Not a directory: {path}"

            entries = sorted(
                p.iterdir(), key=lambda e: (not e.is_dir(), e.name.lower())
            )
            lines = []
            for entry in entries:
                suffix = "/" if entry.is_dir() else ""
                size = ""
                if entry.is_file():
                    size = f"  ({entry.stat().st_size} bytes)"
                lines.append(f"  {entry.name}{suffix}{size}")

            return (
                f"Contents of {path}:\n" + "\n".join(lines)
                if lines
                else f"Directory {path} is empty"
            )
        except PermissionError as e:
            return f"Error: {e}"
        except Exception as e:
            return f"Error listing directory: {e}"
