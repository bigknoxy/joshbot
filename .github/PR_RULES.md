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

## Releases
- Triggered by **git tags** (`vX.Y.Z`)
- Release notes **auto-generated** from merged PRs
- Builds: linux/darwin/windows × amd64/arm64
- Docker: pushed to `ghcr.io/bigknoxy/joshbot`
- Site: deployed to GitHub Pages
- v0.x versions = pre-release
