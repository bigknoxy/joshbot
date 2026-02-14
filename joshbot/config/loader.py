"""Configuration file I/O."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from loguru import logger

from .schema import Config, DEFAULT_HOME


CONFIG_FILE = DEFAULT_HOME / "config.json"


def _deep_merge(base: dict, override: dict) -> dict:
    """Deep merge two dicts, override takes priority."""
    result = base.copy()
    for key, value in override.items():
        if key in result and isinstance(result[key], dict) and isinstance(value, dict):
            result[key] = _deep_merge(result[key], value)
        else:
            result[key] = value
    return result


def load_config() -> Config:
    """Load configuration from file, falling back to defaults."""
    if CONFIG_FILE.exists():
        try:
            raw = json.loads(CONFIG_FILE.read_text())
            logger.debug(f"Loaded config from {CONFIG_FILE}")
            return Config(**raw)
        except Exception as e:
            logger.warning(f"Failed to load config: {e}, using defaults")
    return Config()


def save_config(config: Config) -> None:
    """Save configuration to file."""
    CONFIG_FILE.parent.mkdir(parents=True, exist_ok=True)
    data = config.model_dump(mode="json")
    CONFIG_FILE.write_text(json.dumps(data, indent=2) + "\n")
    logger.info(f"Saved config to {CONFIG_FILE}")


def ensure_dirs(config: Config) -> None:
    """Ensure all required directories exist."""
    for d in [
        config.home_dir,
        config.workspace_dir,
        config.sessions_dir,
        config.media_dir,
        config.cron_dir,
        config.workspace_dir / "memory",
        config.workspace_dir / "skills",
    ]:
        d.mkdir(parents=True, exist_ok=True)
