---
name: github
description: "GitHub CLI (gh) usage patterns for repository management, PRs, and issues"
always: false
requirements: [bin:gh]
tags: [development, git]
---

# GitHub CLI Skill

Use the `gh` CLI tool for GitHub operations. All commands run via the `shell` tool.

## Common Operations

### Repository
```bash
gh repo view              # View current repo info
gh repo clone owner/repo  # Clone a repo
gh repo create name       # Create new repo
```

### Pull Requests
```bash
gh pr list                    # List open PRs
gh pr create --title "..." --body "..."  # Create PR
gh pr view 123                # View PR details
gh pr checkout 123            # Checkout PR locally
gh pr merge 123               # Merge PR
gh pr review 123 --approve    # Approve PR
```

### Issues
```bash
gh issue list                 # List open issues
gh issue create --title "..." --body "..."  # Create issue
gh issue view 123             # View issue
gh issue close 123            # Close issue
```

### Actions
```bash
gh run list                   # List workflow runs
gh run view 12345             # View run details
gh run watch 12345            # Watch run in real-time
```

### Search
```bash
gh search repos "query"       # Search repos
gh search issues "query"      # Search issues
gh search prs "query"         # Search PRs
```

## Tips
- Always check `gh auth status` before operations
- Use `--json` flag for machine-readable output
- Use `--jq` for filtering JSON output
- Shell commands run with a stripped-down environment — variables that look like credentials (including `GH_TOKEN`/`GITHUB_TOKEN`) are never passed through. Authenticate with `gh auth login` (its on-disk config under `HOME` is preserved) rather than relying on a token env var.
- If the operator has turned on `tools.shell_sandbox: workspace`, outbound network is blocked by default (`tools.shell_sandbox_allow_network` must be on) — `gh` commands will fail with a network error until that's enabled.
