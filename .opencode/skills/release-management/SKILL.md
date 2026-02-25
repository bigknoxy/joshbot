# Release Management Skill

This skill provides instructions for managing releases in the joshbot project.

## Pre-Release Checklist

Before creating any release, ALWAYS perform these steps:

### 1. Check Current Release Version

```bash
# Check latest tags
git tag -l 'v*' | sort -V | tail -5

# Check GitHub releases
gh release list --limit 5
```

**IMPORTANT:** Note the latest version number. The next version should follow semantic versioning:
- **Patch** (x.y.Z): Bug fixes, minor changes → increment Z
- **Minor** (x.Y.0): New features, enhancements → increment Y, reset Z to 0
- **Major** (X.0.0): Breaking changes → increment X, reset Y and Z to 0

### 2. Verify All Changes Are Merged

```bash
# Pull latest main
git pull origin main

# Verify no uncommitted changes
git status

# Check recent commits
git log --oneline -5
```

### 3. Determine Correct Version Number

Based on the changes since last release:
- Bug fixes only → patch version (e.g., 1.9.0 → 1.9.1)
- New features → minor version (e.g., 1.9.1 → 1.10.0)
- Breaking changes → major version (e.g., 1.10.0 → 2.0.0)

## Release Process

### Step 1: Create and Push Tag

```bash
# Create tag with correct version
git tag v<X.Y.Z>

# Push tag to trigger release workflow
git push origin v<X.Y.Z>
```

### Step 2: Monitor Release Workflow

```bash
# Check workflow status
gh run list --workflow=release.yml --limit 3

# Watch specific run (get ID from above command)
gh run watch <RUN_ID> --exit-status
```

### Step 3: Verify Release

```bash
# Check release was created and marked as Latest
gh release list --limit 5

# Verify the Latest flag is on the new release
# Output should show: v<X.Y.Z>	Latest	v<X.Y.Z>	...
```

### Step 4: Fix if Not Latest

If the release is not marked as "Latest":

```bash
# Update release to set as latest
gh release edit v<X.Y.Z> --latest
```

## Common Issues

### Wrong Version Number

If you accidentally create a release with wrong version:

```bash
# Delete the release
gh release delete v<WRONG.VERSION> --yes

# Delete the remote tag
git push --delete origin v<WRONG.VERSION>

# Delete local tag
git tag -d v<WRONG.VERSION>

# Create correct version
git tag v<CORRECT.VERSION>
git push origin v<CORRECT.VERSION>
```

### Release Not Marked as Latest

The GitHub release workflow should automatically mark new releases as "Latest". If not:

```bash
gh release edit v<X.Y.Z> --latest
```

## Version History Reference

| Version | Date | Description |
|---------|------|-------------|
| v1.12.1 | 2026-02-25 | Model/provider sync fix, tool_call_id preservation |
| v1.12.0 | 2026-02-24 | Exa crawl for web_fetch, version/status display fixes |
| v1.11.0 | 2026-02-24 | Enhanced Ollama integration |
| v1.9.1 | 2026-02-24 | Provider registration fix, -m flag |
| v1.9.0 | 2026-02-23 | exa-cli tool |
| v1.8.2 | 2026-02-23 | NVIDIA API URL fix |
| v1.8.1 | 2026-02-23 | Onboarding Enabled flag fix |
| v1.8.0 | 2026-02-23 | NVIDIA in configure/onboard |
| v1.7.0 | 2026-02-23 | NVIDIA provider support |

## Quick Reference

```bash
# Full release workflow
git pull origin main
git tag -l 'v*' | sort -V | tail -1  # Check current version
git tag v<NEW.VERSION>               # Create new tag
git push origin v<NEW.VERSION>       # Push to trigger release
gh run list --workflow=release.yml --limit 1  # Check workflow
gh release list --limit 1            # Verify release
```
