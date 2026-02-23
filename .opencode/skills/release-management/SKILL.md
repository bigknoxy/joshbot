---
name: release-management
description: Create semantic versioned releases for joshbot with git tags and GitHub Actions verification
license: MIT
compatibility: opencode
metadata:
  audience: maintainers
  workflow: github-actions
  project: joshbot
---

## What I do

Create consistent releases for the joshbot project using semantic versioning and git tags.

**joshbot's release process:**
- Version determined by git tags (no VERSION file)
- GitHub Actions triggered on `v*` tag push
- Auto-builds 6 platform binaries (linux/darwin/windows × amd64/arm64)
- Creates GitHub release with artifacts and checksums
- Pushes Docker image to ghcr.io/bigknoxy/joshbot

## When to use me

Use this skill when the user mentions:
- "push a release"
- "create a release"  
- "tag a release"
- "make a new version"
- "bump version"
- Any variation of the above

## Release Checklist

1. **Check current version:**
   ```
   git fetch --tags origin
   git tag --sort=-version:refname | head -5
   ```

2. **Determine next version (Semantic Versioning):**
   - MAJOR: Breaking changes (rare for this project)
   - MINOR: New features (e.g., exa-cli support) → v1.8.2 → v1.9.0
   - PATCH: Bug fixes → v1.8.2 → v1.8.3

3. **Update CHANGELOG.md:**
   - Add new section: `## [X.Y.Z] - YYYY-MM-DD`
   - Use `### Added`, `### Changed`, `### Fixed` subsections
   - Reference PR numbers

4. **Commit changelog:**
   ```
   git add CHANGELOG.md
   git commit -m "chore: add vX.Y.Z changelog"
   ```

5. **Create and push tag:**
   ```
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

6. **Verify CI is GREEN:**
   - Check GitHub Actions: https://github.com/bigknoxy/joshbot/actions
   - Wait for all workflows to complete with SUCCESS status
   - This is MANDATORY before release is considered complete

7. **Verify Release:**
   - Release appears at: https://github.com/bigknoxy/joshbot/releases
   - Docker image updated: `docker pull ghcr.io/bigknoxy/joshbot:vX.Y.Z`
   - Verify all 6 binaries are attached

## Critical Requirements

- **ALWAYS verify CI is green** after pushing the tag
- If CI fails, report failure and do NOT consider release complete
- Use `gh run list --branch main` or check web UI for CI status

## joshbot-Specific Notes

- **No breaking changes**: Personal AI assistant, major versions are rare
- **Feature = Minor**: Adding new tools/features warrants minor version bump
- **Auto-update**: Users can run `joshbot update` to get latest release
- **Install script**: https://github.com/bigknoxy/joshbot/install.sh
