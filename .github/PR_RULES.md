# PR & Release Rules

## Pull Request Process
1. **Always branch** from `main` — never commit directly to `main`
2. **Use the PR template** — fill all required sections
3. **Conventional commits** — `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`
4. **CI must be green** before merging
5. **Squash & merge** — keep history clean

## Commit Messages
- Types: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`
- Imperative mood ("add" not "added")
- First line under 72 characters
- Body with "why" if needed

## CHANGELOG.md

Every PR that changes observable behaviour adds an entry under `## [Unreleased]`.
Everything below that heading is **release history and is append-only** — a merged
release section is never edited, reworded or removed.

The `changelog-guard` CI job enforces that (`internal/relguard`, issue #120). It
fails a PR when either holds:

1. Merging would remove any line from a released `## [x.y.z]` section that exists
   on the base branch's tip — including deleting a whole section.
2. The newest released section in the PR's `CHANGELOG.md` is *behind* the newest
   tag on the base branch.

Being **ahead** of the newest tag is fine and expected: the version is stamped in a
PR and the tag is pushed only after it merges.

**When it fires,** the cause is almost always a branch cut before a release was
stamped. Rebase on the base branch and take the base branch's version of every
released section:

```bash
git fetch origin main --tags
git rebase origin/main          # keep origin/main's side of CHANGELOG.md conflicts
```

Do not "fix" it by deleting the entry the guard is complaining about — the whole
point is that git will merge that loss silently. Two PRs appending to
`[Unreleased]` can still lose an entry to auto-resolution with no conflict
(observed between #109 and #110); the guard does not catch that case, so check
your entry actually survived after merging.

## Releases
- Triggered by **git tags** (`vX.Y.Z`)
- Release notes **auto-generated** from merged PRs
- Builds: linux/darwin/windows × amd64/arm64
- Docker: pushed to `ghcr.io/bigknoxy/joshbot`
- Site: deployed to GitHub Pages
- v0.x versions = pre-release
