"""Skills discovery, loading, and progressive injection."""

from __future__ import annotations

import re
import shutil
from pathlib import Path
from typing import Any

from loguru import logger


class Skill:
    """Represents a loaded skill."""

    def __init__(
        self,
        name: str,
        description: str,
        path: Path,
        always: bool = False,
        requirements: list[str] | None = None,
        tags: list[str] | None = None,
    ) -> None:
        self.name = name
        self.description = description
        self.path = path
        self.always = always
        self.requirements = requirements or []
        self.tags = tags or []
        self._content: str | None = None
        self._available: bool | None = None

    @property
    def available(self) -> bool:
        """Check if skill requirements are met."""
        if self._available is not None:
            return self._available

        self._available = True
        for req in self.requirements:
            if req.startswith("bin:"):
                binary = req.split(":", 1)[1]
                if shutil.which(binary) is None:
                    self._available = False
                    break
            elif req.startswith("env:"):
                import os

                env_var = req.split(":", 1)[1]
                if not os.environ.get(env_var):
                    self._available = False
                    break

        return self._available

    def get_content(self) -> str:
        """Get the full skill content (level 2 loading)."""
        if self._content is not None:
            return self._content

        skill_file = self.path / "SKILL.md"
        if skill_file.exists():
            try:
                raw = skill_file.read_text(encoding="utf-8")
                # Strip YAML frontmatter
                if raw.startswith("---"):
                    parts = raw.split("---", 2)
                    if len(parts) >= 3:
                        self._content = parts[2].strip()
                    else:
                        self._content = raw
                else:
                    self._content = raw
            except Exception as e:
                logger.error(f"Failed to read skill {self.name}: {e}")
                self._content = ""
        else:
            self._content = ""

        return self._content

    def to_summary_xml(self) -> str:
        """Get level 1 summary as XML for system prompt injection."""
        avail = "true" if self.available else "false"
        return f'  <skill name="{self.name}" available="{avail}">{self.description}</skill>'


class SkillsLoader:
    """Discover and load skills from bundled and workspace directories."""

    def __init__(self, bundled_dir: str | Path, workspace_dir: str | Path) -> None:
        self._bundled_dir = Path(bundled_dir)
        self._workspace_dir = Path(workspace_dir) / "skills"
        self._skills: dict[str, Skill] = {}
        self._loaded = False

    def discover(self) -> None:
        """Discover all available skills. Workspace skills override bundled ones."""
        self._skills.clear()

        # Load bundled skills first
        if self._bundled_dir.exists():
            for skill_dir in self._bundled_dir.iterdir():
                if skill_dir.is_dir() and (skill_dir / "SKILL.md").exists():
                    skill = self._parse_skill(skill_dir)
                    if skill:
                        self._skills[skill.name] = skill

        # Load workspace skills (override bundled)
        if self._workspace_dir.exists():
            for skill_dir in self._workspace_dir.iterdir():
                if skill_dir.is_dir() and (skill_dir / "SKILL.md").exists():
                    skill = self._parse_skill(skill_dir)
                    if skill:
                        if skill.name in self._skills:
                            logger.info(
                                f"Workspace skill '{skill.name}' overrides bundled"
                            )
                        self._skills[skill.name] = skill

        self._loaded = True
        logger.info(f"Discovered {len(self._skills)} skills")

    def _parse_skill(self, skill_dir: Path) -> Skill | None:
        """Parse a skill from its directory."""
        skill_file = skill_dir / "SKILL.md"

        try:
            raw = skill_file.read_text(encoding="utf-8")
        except Exception as e:
            logger.warning(f"Failed to read {skill_file}: {e}")
            return None

        # Parse YAML frontmatter
        name = skill_dir.name
        description = ""
        always = False
        requirements: list[str] = []
        tags: list[str] = []

        if raw.startswith("---"):
            parts = raw.split("---", 2)
            if len(parts) >= 3:
                frontmatter = parts[1]
                for line in frontmatter.strip().splitlines():
                    line = line.strip()
                    if line.startswith("name:"):
                        name = line.split(":", 1)[1].strip().strip("\"'")
                    elif line.startswith("description:"):
                        description = line.split(":", 1)[1].strip().strip("\"'")
                    elif line.startswith("always:"):
                        val = line.split(":", 1)[1].strip().lower()
                        always = val in ("true", "yes", "1")
                    elif line.startswith("requirements:"):
                        # Inline list: [bin:git, env:GITHUB_TOKEN]
                        req_str = line.split(":", 1)[1].strip()
                        if req_str.startswith("["):
                            req_str = req_str.strip("[]")
                            requirements = [
                                r.strip().strip("\"'")
                                for r in req_str.split(",")
                                if r.strip()
                            ]
                    elif line.startswith("tags:"):
                        tags_str = line.split(":", 1)[1].strip()
                        if tags_str.startswith("["):
                            tags_str = tags_str.strip("[]")
                            tags = [
                                t.strip().strip("\"'")
                                for t in tags_str.split(",")
                                if t.strip()
                            ]

        if not description:
            # Try to extract first paragraph as description
            content = raw.split("---", 2)[-1].strip() if raw.startswith("---") else raw
            first_para = content.split("\n\n")[0].replace("\n", " ").strip()
            description = first_para[:200] if first_para else f"Skill: {name}"

        return Skill(
            name=name,
            description=description,
            path=skill_dir,
            always=always,
            requirements=requirements,
            tags=tags,
        )

    def get_summary(self) -> str:
        """Get XML summary of all skills (level 1 - for system prompt).

        Always-loaded skills have their full content included.
        Other skills just have name + description.
        """
        if not self._loaded:
            self.discover()

        parts = [
            "Available skills (use read_file to load full skill content when needed):"
        ]

        for skill in self._skills.values():
            parts.append(skill.to_summary_xml())

            # Always-loaded skills get full content injected
            if skill.always and skill.available:
                content = skill.get_content()
                if content:
                    parts.append(
                        f'  <skill-content name="{skill.name}">\n{content}\n  </skill-content>'
                    )

        return "\n".join(parts)

    def get_skill(self, name: str) -> Skill | None:
        """Get a specific skill by name."""
        if not self._loaded:
            self.discover()
        return self._skills.get(name)

    def list_skills(self) -> list[Skill]:
        """List all discovered skills."""
        if not self._loaded:
            self.discover()
        return list(self._skills.values())
