"""Configuration file I/O."""

from __future__ import annotations

import json
import shutil
from pathlib import Path
from typing import Any, Callable

from loguru import logger

from .schema import CURRENT_SCHEMA_VERSION, Config, DEFAULT_HOME


CONFIG_FILE = DEFAULT_HOME / "config.json"


def _migrate_v0_to_v1(raw: dict[str, Any]) -> dict[str, Any]:
    """Migrate pre-versioned config to schema v1."""
    raw["schema_version"] = 1
    # Update defunct model if present
    agents = raw.get("agents", {})
    defaults = agents.get("defaults", {})
    if defaults.get("model") == "google/gemma-2-9b-it:free":
        defaults["model"] = "arcee-ai/trinity-large-preview:free"
        logger.info(
            "Migrated model from google/gemma-2-9b-it:free to arcee-ai/trinity-large-preview:free"
        )
    return raw


# Registry: from_version -> migration function
MIGRATIONS: dict[int, Callable[[dict[str, Any]], dict[str, Any]]] = {
    0: _migrate_v0_to_v1,
}


def load_config() -> Config:
    """Load configuration from file, falling back to defaults."""
    if CONFIG_FILE.exists():
        try:
            raw = json.loads(CONFIG_FILE.read_text())
            logger.debug(f"Loaded config from {CONFIG_FILE}")

            # Check schema version and run migrations if needed
            schema_version = raw.get("schema_version", 0)
            if schema_version > CURRENT_SCHEMA_VERSION:
                logger.warning(
                    f"Config schema v{schema_version} is newer than supported v{CURRENT_SCHEMA_VERSION}. "
                    f"You may be running an older version of joshbot."
                )
            if schema_version < CURRENT_SCHEMA_VERSION:
                logger.info(
                    f"Migrating config from schema v{schema_version} to v{CURRENT_SCHEMA_VERSION}"
                )
                # Back up config
                backup_path = CONFIG_FILE.with_suffix(".json.bak")
                shutil.copy2(CONFIG_FILE, backup_path)
                logger.info(f"Backed up config to {backup_path}")
                # Run migrations sequentially
                for version in range(schema_version, CURRENT_SCHEMA_VERSION):
                    if version in MIGRATIONS:
                        try:
                            raw = MIGRATIONS[version](raw)
                            logger.info(
                                f"Migrated config schema v{version} → v{version + 1}"
                            )
                        except Exception as e:
                            logger.error(
                                f"Config migration v{version} → v{version + 1} failed: {e}"
                            )
                            logger.warning("Using backup config from config.json.bak")
                            return Config()
                # Write migrated config back
                CONFIG_FILE.write_text(json.dumps(raw, indent=2) + "\n")
                logger.info(f"Wrote migrated config to {CONFIG_FILE}")

            return Config(**raw)
        except json.JSONDecodeError as e:
            logger.warning(f"Config file is corrupted: {e}, using defaults")
            return Config()
        except Exception as e:
            logger.warning(f"Failed to load config: {e}, using defaults")
            return Config()
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
