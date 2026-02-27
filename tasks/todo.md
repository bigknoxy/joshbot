Migrate Python joshbot to Go — plan and acceptance criteria

- Goal: Replace the Python implementation with a complete, production-ready Go implementation and phase out the Python codebase. The running service should be the Go binary and provide at least feature parity with current gateway (Telegram inbound/outbound, ReAct agent loop with tools, message bus, sessions, skills, and basic observability).

- Acceptance criteria:
  - `go build ./cmd/joshbot` produces a working gateway binary that can start and accept Telegram updates (polling or webhook) and process messages end-to-end.
  - Existing Python behavior for allowlist, markdown handling, token redaction, empty-outbound guards, and outbound tracing (trace_id + digest) is implemented and verified in Go.
  - Unit tests cover critical components (bus, channels/telegram, agent loop core) and `go test ./...` passes.
  - A rollout plan is documented and the running gateway can be switched with zero/low downtime.

- Phases (high level):
  1. Discover & map (2 days)
     - Inventory Python modules and map to Go packages (channels, agent, bus, providers, tools, skills, config).
     - Produce a file-level migration map and an interface contract for each major component.
  2. Scaffold Go repo (1 day)
     - Create Go module, `cmd/joshbot`, `pkg/agent`, `pkg/bus`, `pkg/channels`, `pkg/providers`, `pkg/tools`, `pkg/config`, `internal/skills`.
     - Add CI skeleton (GitHub Actions) to run `go test` and `go vet`.
  3. Implement core infra (3-5 days)
     - Message bus, Inbound/Outbound message types, session storage (file-backed JSON), basic config loader.
  4. Channels: Telegram + CLI (3-5 days)
     - Implement Telegram channel (webhook + polling), markdown safe-send (MarkdownV2 escape), token redaction, empty message guard, outbound tracing.
     - CLI channel for local testing.
  5. Agent loop & providers (5-8 days)
     - ReAct agent loop, LLM provider interface, Litellm provider adapter (or a mock provider for local dev), tools registry and execution model.
  6. Skills, tools, sessions (3-5 days)
     - Skills loader (markdown files), tools port (message, shell, webfetch stubs), session persistence.
  7. Tests, observability, and hardening (2-4 days)
     - Unit tests, basic integration tests, structured logging, metrics, and troubleshooting (timeouts, retries).
  8. Rollout & cutover (1-2 days)
     - Deploy, smoke test, switch webhook/stop Python instance, monitor.

- Immediate next actions (I will do these now unless you instruct otherwise):
  1. Create a detailed migration map (repo-relative file list mapping Python -> Go packages) and place it at `tasks/migration_map.md`.
  2. Scaffold the Go module in `/root/code/joshbot-go` with `go mod init github.com/bigknoxy/joshbot` and a minimal `cmd/joshbot/main.go` that prints a help message.
  3. Add `tasks/` checklist entries for each phase above and mark discovery as `in_progress`.

- Verification steps (how I will prove migration progress):
  - Unit test pass: `go test ./...` (local CI will run as part of workflow).
  - Build success: `go build -o /tmp/joshbot ./cmd/joshbot` and `file /tmp/joshbot` to confirm a Go ELF binary.
  - Runtime smoke: start gateway in foreground `stdbuf -oL /tmp/joshbot gateway` and send a test Telegram message (or emulate via `curl` when using webhook) and show the inbound/outbound log sequence.

- Risks & mitigations:
  - Risk: Behavioral drift (subtle differences vs Python) — Mitigate: add unit tests for allowlist normalization, markdown escaping, empty message guard, and token-redaction early.
  - Risk: LLM provider differences — Mitigate: add a mock provider and integration tests to validate ReAct loop behavior before wiring production provider.
  - Risk: Downtime during cutover — Mitigate: use webhook setWebhook atomic switch or use canary instance and redirect traffic gradually.

- Deliverables (per phase):
  1. `tasks/migration_map.md` (file mapping).
  2. `joshbot-go` repo scaffold with `cmd/joshbot` and empty package skeletons.
  3. Working `pkg/bus` and message types with unit tests.
  4. `pkg/channels/telegram` implementing safe sends + webhook/polling toggle.
  5. Agent loop + provider adapters with ReAct skeleton and sample tool.

- Where to look / important files (when I start coding):
  - New Go workspace: `/root/code/joshbot-go/` (will create if missing)
  - Live Go binary: `/root/.local/bin/joshbot` (currently running) — I will not stop it unless you ask.

- Estimated calendar timeline: ~3	6 weeks for an initial production-ready parity (assuming single engineer, incremental shipping). Exact timing depends on complexity of LLM adapter and webhook integration.

Acceptance criteria checklist (copy to track progress):
- [ ] Migration map created (`tasks/migration_map.md`)
- [ ] Go module scaffolded in `/root/code/joshbot-go`
- [ ] Core bus + message types implemented + tests
- [ ] Telegram channel implemented (webhook + polling) + tests for markdown/empty/allowlist
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
