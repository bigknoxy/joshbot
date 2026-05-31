# Tasks

## Goal: Fix model config bug + abstract configure with CLI flags + tests

### Acceptance Criteria
- `joshbot config --provider nvidia --api-key xxx --model foo` sets the config correctly
- Interactive `joshbot config` wizard produces identical results
- `joshbot agent` picks up the configured model, not the registry default
- All paths have unit test coverage

### Bugs to Fix
1. `setDefaultProvider` (main.go:2901) overwrites model with registry default
2. `configureProvider` auto-default (main.go:2683) doesn't set model
3. NVIDIA registration (main.go:369-381) doesn't pass `p.Model`

### Implementation Plan

**Phase 1: Fix Bugs (targeted)**
- main.go:2901 — prefer per-provider model over registry default
- main.go:2683-2684 — also set model when auto-defaulting
- main.go:369-381 — pass p.Model to NVIDIA registration

**Phase 2: Create `internal/configure/` package**
- `configurator.go` — Configurator type with non-interactive API
- `configure_test.go` — comprehensive tests

**Phase 3: Wire CLI flags in main.go**
- `--provider`, `--api-key`, `--api-base`, `--model`, `--set-default`, `--remove` flags
- `runConfigure` delegates to configure package when flags are set
- Interactive wizard delegates to same package

- Where to look / important files (when I start coding):
  - New Go workspace: `/root/code/joshbot-go/` (will create if missing)
  - Live Go binary: `/root/.local/bin/joshbot` (currently running) — I will not stop it unless you ask.

- Estimated calendar timeline: ~3-6 weeks for an initial production-ready parity (assuming single engineer, incremental shipping). Exact timing depends on complexity of LLM adapter and webhook integration.

Acceptance criteria checklist (copy to track progress):
- [ ] Migration map created (`tasks/migration_map.md`)
- [ ] Go module scaffolded in `/root/code/joshbot-go`
- [ ] Core bus + message types implemented + tests
- [ ] Telegram channel implemented (webhook + polling) + tests for markdown/empty/allowlist

**Phase 4: End-to-end verification**
- Build + test
- Manual smoke test
- [ ] Agent loop and provider interfaces + mock provider tests
- [ ] Integration tests and CI pass
- [ ] Rollout plan executed and Python gateway decommissioned

---

Prepare joshbot for production users — remediation plan

- Goal: Harden security, fix functional gaps, improve reliability/UX, and ship a production-ready PR for review.
- Acceptance criteria:
  - Shell/FS/Web tools are safe by default, allow opt-in for broader access.
  - `/new` fully resets server-side sessions.
  - Provider errors are structured and fallback works; timeouts respected.
  - Message bus concurrency is bounded; Telegram/CLI reliability improvements verified.
  - Memory/skills prompt bloat reduced with controls.
  - Tests added/updated; `go test ./...` passes.
  - PR created with clear summary and verification story.

- Working notes:
  - Default to workspace-only file access; allow `restrict=false` for broader scope.
  - Shell access is allowed when user enables it; still avoid obviously dangerous commands.
  - Keep changes minimal and follow existing patterns.

- Tasks:
  - [ ] Security hardening: shell allowlist or safer execution, SSRF guard, filesystem restrictions
  - [ ] Session lifecycle: implement real `/new` reset
  - [x] Provider reliability: fix `WithTimeout`, structured errors/fallback
  - [ ] Concurrency: bound message bus dispatch
  - [ ] UX reliability: CLI full-line input, Telegram reconnect/media handling
  - [x] Memory/skills: reduce prompt bloat, dedupe/limits
  - [x] Memory/skills: dedupe consolidated facts, expand history window, skills summary-only
  - [x] Run go tests for modified packages
  - [ ] Tests: add coverage for providers/Telegram/memory where needed
  - [ ] Verification: `go test ./...`
  - [ ] PR: create ready-for-review PR

Provider reliability subtasks:
- [x] Fix WithTimeout in registry.go to respect input parameter
- [x] Update LiteLLM provider to return structured FallbackError
- [x] Add tests for WithTimeout
- [x] Add tests for FallbackError in LiteLLM provider
- [x] Add tests for error fallback logic
- [x] Run relevant go tests

---

# Current Task: Fix fallback logic using providers with enabled=false

## Issue
- main.go line 311: OpenRouter registered WITHOUT checking `p.Enabled`
- Other providers (nvidia, groq, ollama, github-copilot) correctly check `p.Enabled`
- Fallback chain includes all registered providers regardless of config Enabled status

## Plan
- [x] Fix main.go: Add `p.Enabled` check for OpenRouter registration  
- [x] Add Enabled field to ProviderEntry in multiprovider for runtime filtering
- [x] Add tests for enabled/disabled provider behavior in fallback
- [x] Run verification: go test ./..., go vet ./..., go build ./cmd/joshbot

---

# Current Task: Create PR and monitor CI

- Goal: Create a new branch, open a PR with current changes, and monitor CI to green. If CI fails, diagnose, fix, and update PR until green.
- Acceptance criteria:
  - PR exists from a new branch with a clear summary.
  - CI checks for the PR are green, or failures are fixed and rerun to green.
  - Provide PR URL and final CI status.

- Working notes:
  - Use existing repo conventions for branch naming and PR summary.
  - Do not modify unrelated files; keep changes minimal if fixes are needed.

- Plan:
  - [ ] Restate goal + acceptance criteria
  - [ ] Inspect git status, diffs, and recent commits for PR scope
  - [ ] Create new branch and open PR with summary
  - [ ] Monitor CI checks; if failing, diagnose root cause
  - [ ] Implement minimal fix, update PR, and re-check CI
  - [ ] Report PR URL and final CI status
