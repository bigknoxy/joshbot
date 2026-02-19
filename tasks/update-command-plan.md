# Plan: `joshbot update` Command + Close All Version/Upgrade Gaps

## Overview

8 work items to add a self-update command and close all version/upgrade infrastructure gaps.

**Decisions made:**
- Version checking via GitHub API releases/tags
- Version sync via Hatch path source (`__init__.py` is single source of truth)
- Install method auto-detection (pipx → editable → Docker → pip fallback)
- Full scope: all 8 items
- Create first release tag (v0.2.0) as part of this work

---

## Item 1: Version Single Source of Truth

**Files**: `pyproject.toml`, `joshbot/__init__.py`, `joshbot/tools/web.py`

**Changes**:
- `pyproject.toml`: Remove hardcoded `version = "0.1.0"`, add `dynamic = ["version"]` and `[tool.hatch.version]` section pointing to `joshbot/__init__.py`
- `joshbot/__init__.py`: Keep `__version__` as single source
- `joshbot/tools/web.py` line 117: Replace hardcoded `"joshbot/0.1"` User-Agent with dynamic `from joshbot import __version__`

**Verify**: `pip install -e .` succeeds, `pip show joshbot` shows correct version

---

## Item 2: Config Schema Versioning + Migration Framework

**Files**: `joshbot/config/schema.py`, `joshbot/config/loader.py`

**Changes in `schema.py`**:
- Add `schema_version: int = 1` field to `Config`
- Add `CURRENT_SCHEMA_VERSION = 1` module constant

**Changes in `loader.py`**:
- Add `MIGRATIONS` registry mapping `from_version` to migration functions
- In `load_config()`: read raw JSON, check `schema_version`, run migrations, backup before migrating
- `save_config()` ensures `schema_version` is written
- Migration v0→v1: Add schema_version, update old model name if present

**Verify**: Load config without schema_version, confirm migration runs

---

## Item 3: `--version` Flag + Version in `status`

**File**: `joshbot/main.py`

- Typer version callback for `joshbot --version`
- Add version to `status` command output

**Verify**: `joshbot --version` and `joshbot status` both show version

---

## Item 4: `joshbot update` Command

**File**: `joshbot/main.py`

**Behavior**:
1. Fetch latest from GitHub API releases
2. Compare versions
3. Auto-detect install method (pipx → editable → Docker → pip)
4. Run update subprocess
5. Post-update config migration

**Flags**: `--check`/`-c` (check only), `--force`/`-f` (force reinstall)

**Error handling**: Network failure, rate limit, subprocess failure → graceful messages

---

## Item 5: CHANGELOG.md

Seed with v0.2.0 and v0.1.0 entries. Keep a Changelog format.

---

## Item 6: Update README

- Lead upgrading section with `joshbot update`
- Add Docker upgrade instructions
- Reference `--version` and `status`
- Point to CHANGELOG

---

## Item 7: Bump Version to 0.2.0

`joshbot/__init__.py`: `__version__ = "0.2.0"`

---

## Item 8: Final Verification + PR + Tag

1. Full install, run all commands
2. `joshbot --version` → 0.2.0
3. `joshbot status` → version shown
4. `joshbot update --check` → works
5. `joshbot update` → full flow executes
6. Create PR, merge, create v0.2.0 tag/release

---

## Dependency Graph

```
Item 1 (version source) ──┐
                           ├── Item 3 (--version + status)
Item 2 (config migration) ─┤
                           ├── Item 4 (update command) ── Item 8 (verify + PR + tag)
                           │
Item 5 (CHANGELOG) ────────┤
Item 6 (README) ───────────┤
Item 7 (version bump) ─────┘
```
