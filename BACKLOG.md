# joshbot — Audit Backlog (Panel Review + Competitive Research)

**Date:** 2026-08-07
**HEAD:** 744167b (main, v1.44.0)
**Method:** 5-expert panel review (security, evals, experience, growth, Go systems) + web research on competitors (OpenClaw, Claude Code, Codex CLI, Goose, Aider, OpenHands, Khoj) and 2024–2026 agent papers.

## Verdict

**Revise — composite 4.9/10.** Engineering craft is strong (clean gates, race-clean suite, thoughtful web/SSRF and skill-trust design), but three things gate the "as good and easy as top competitors" goal: (1) the security boundary that competitors ship by default doesn't exist on macOS, (2) the flagship proactive feature (heartbeat) is a silent token-burning loop with undeliverable output, and (3) every discovery + machine-readable-output channel that adoption depends on is dark.

Two blocking concerns (override the band for any "ship to new users" decision):
- **B1** No OS sandbox on macOS; deny-list is bypassable and is the sole boundary for a shell+web+chat agent.
- **B2** Empty Telegram allowlist permits everyone on a shell-capable bot.

### Panel scores

| Expert | Weight | Score | Confidence | Headline |
|---|---|---|---|---|
| Security | 0.28 | 4/10 | med-high | No macOS sandbox; deny-list is sole, bypassable boundary |
| Evals | 0.20 | 6/10 | high | Eval harness is good; but suite is RED on macOS (release platform) |
| Experience | 0.20 | 4/10 | med-high | Debug banner leaks messages; heartbeat silently burns tokens |
| Growth | 0.17 | 4/10 | medium | 0 stars, no topics, pitch buried, README funnels to nanobot |
| Go systems | 0.15 | 7/10 | high | Strong; bus shutdown panic window + coverage blind spots |

Weights: security-heaviest because the subject touches shell/web/channel trust boundaries; evals+experience raised to 0.20 each because the stated goal is CLI parity and easy onboarding; growth kept near default (adoption reading, per user framing).

---

## Prioritized backlog

GitHub issues: #138–#160 (label `audit-2026-08`). Item numbers below map in order: 1=#138, 2=#139, 3=#140, 4=#141, 5=#142, 6=#143, 7=#144, 8=#145, 9=#146, 10=#147, 11=#148, 12=#149, 13=#150, 14=#151, 15=#152, 16=#153, 17=#154, 18=#155, 19=#156, 20=#157, 21=#158, 22=#159, 23=#160.

Priority score = Severity (1–5) × Reach (1–5) ÷ Effort (S=1, M=2, L=3), scaled ×2, rounded. Higher = do first. "Parity" = closes a competitor gap.

| # | Item | Lane | Sev | Reach | Effort | Priority | Type |
|---|---|---|---|---|---|---|---|
| 1 | Fix `SandboxMode` zero-value: make `""` == `off` in `sandboxPreflight`/constructors; add macOS CI leg | Evals/Sec/Go | 5 | 5 | S | **50** | Bug |
| 2 | Remove `!!! BUS HANDLER INVOKED` debug print leaking message content to stderr (`main.go:1765`) | Experience | 5 | 4 | S | **40** | Bug |
| 3 | Empty Telegram allowlist → deny-all + loud startup warning (refuse Telegram+shell w/o allowlist) | Security | 5 | 4 | S | **40** | Bug/Sec |
| 4 | Fix heartbeat: one-shot per task (dedup/auto-check), route output to real chat, add `heartbeat.interval` config, document completion contract | Experience | 5 | 4 | M | **20** | Bug |
| 5 | `onboard --force` non-interactive: add `--provider/--api-key` flags + env, validate key, exit non-zero if 0 providers (no false "Setup complete!") | Experience | 4 | 4 | S | **32** | Bug/Parity |
| 6 | Bus `Stop()` send-on-closed-channel panic window: cancel-only shutdown or track handler goroutines + guard sends (`bus.go:168-199,333`) | Go systems | 4 | 3 | M | **12** | Bug |
| 7 | `--output-format json\|stream-json` on `agent -m`: stdout=data only, session_id + token/cost metadata, NDJSON tool events | Evals/Growth | 4 | 5 | M | **20** | Parity |
| 8 | GitHub discoverability: set repo topics (`ai-agent`, `personal-assistant`, `self-hosted`, `golang`), rewrite description | Growth | 3 | 5 | S | **30** | Parity |
| 9 | Rewrite pitch around real wedge: "nanobot-class personal agent as a single Go binary — curl to running in <1 min." README lead + site hero + `why-not-nanobot` table | Growth | 3 | 5 | S | **30** | Parity |
| 10 | Skill trust: hash whole skill dir tree, not just SKILL.md (`trust.go:73-81`) — closes sibling-file swap | Security | 4 | 2 | S | **16** | Sec |
| 11 | Distinct exit codes (0/1/2/3) + JSON errors on stderr w/ `code`+`remediation`; `--no-color`, log-level flags | Evals/Growth | 3 | 4 | M | **12** | Parity |
| 12 | Headless session resume: emit + accept session ID for `agent -m` chains (Claude Code `--resume` pattern) | Evals | 3 | 4 | M | **12** | Parity |
| 13 | macOS Seatbelt (`sandbox-exec`) or allowlist-only shell default on unsandboxed platforms | Security | 4 | 3 | L | **8** | Sec/Parity |
| 14 | Untimed HTTP in `ValidateToken` (`telegram.go:1617`) + `isRetryable` retries permanent 403s — add 10s timeout, stop retrying blocked/deactivated | Go systems | 3 | 3 | S | **18** | Bug |
| 15 | ~~Delete repo debris: `pyproject.toml`, `tests/*.py`, stale `pkg/`~~ — **done** (audit sweep, 2026-08-10) | Growth/Go | 2 | 4 | S | **16** | Cleanup |
| 16 | Symlink-resolve filesystem containment (`EvalSymlinks` in `path_guard.go`) — load-bearing once sandbox on | Security | 3 | 2 | S | **12** | Sec |
| 17 | Per-package coverage floors (or targeted tests) for `cmd/joshbot` (9.4%) + golden-output test of `agent -m` non-TTY | Evals/Go | 3 | 3 | M | **9** | Test |
| 18 | MCP client support (universal extension mechanism — Goose/Claude Code/Codex all have it; joshbot only for web) | Parity | 3 | 4 | L | **8** | Parity |
| 19 | Opt-in live eval (`joshbot eval` / `-tags liveeval`): ~5 fixed tasks (recall fact, tool choice, cron format, refuse denied shell) — kept out of CI | Evals | 3 | 3 | M | **9** | Test |
| 20 | Fix/delete dead Telegram code: `EditMessage` hardcodes chatID:0, `downloadFile` writes to CWD uncapped, unused SendPhoto/SendDocument | Go systems | 2 | 2 | M | **4** | Cleanup |
| 21 | Replace hard-coded site stat counters (`22,076 LOC` etc.) with durable claims (binary size/startup/deps) or CI-generated | Growth | 2 | 3 | S | **12** | Doc |
| 22 | Additional channels (Discord/Slack/WhatsApp) — OpenClaw ~29, Khoj has WhatsApp | Parity | 2 | 3 | L | **4** | Parity |
| 23 | `configure`/`ListProviders` only knows 6 of 11 providers; empty APIBase for anthropic/openai/azure/litellm (`configure.go:125`) | Experience | 2 | 3 | S | **12** | Bug |

---

## Grouped roadmap

**Sprint 1 — Correctness & trust (do now, mostly S-effort):** #1, #2, #3, #5, #14, #10. These are bugs + one-line security fixes. #1 first — until fixed, no local test result on macOS is trustworthy, undermining every other quality claim.

**Sprint 2 — Flagship feature + agentic parity:** #4 (heartbeat), #7 (JSON output), #8+#9 (discoverability — 5-min + copy work, huge leverage), #6 (bus panic), #11, #12.

**Sprint 3 — Depth & parity:** #13 (macOS sandbox), #16, #17, #18 (MCP), #19, #15/#20/#21 cleanup, #22, #23.

---

## Competitive gap summary (from research)

Table-stakes joshbot is missing vs OpenClaw / Claude Code / Codex / Goose / Aider:

1. **Machine-readable output** — `--output-format json|stream-json`, session_id + cost metadata, stdout=data / stderr=diagnostics. (#7, #11)
2. **Headless session resume** — emit + accept session ID. (#12)
3. **Default-on sandbox** — Codex sandboxes by default (Seatbelt/bubblewrap/Windows); joshbot's Landlock is opt-in + Linux-only. (#1, #13)
4. **MCP client** — universal extension mechanism everyone else has. (#18)
5. **Heartbeat polish** — copy OpenClaw's `HEARTBEAT_OK` suppression + per-monitor scratch docs. (#4)
6. **Discoverability** — topics, differentiated pitch, comparison table. (#8, #9)
7. **More channels** — Discord/Slack/WhatsApp/Signal. (#22)
8. **`--profile` state isolation + documented config precedence** (flags > env > project > user).

### Paper-derived ideas (longer horizon)

- **Memory:** Mem0 (extract→update→retrieve) and A-MEM (linked agent-managed notes) to upgrade fact extraction/consolidation beyond append+periodic-consolidate. MemGPT formalizes the MEMORY.md-in-context / HISTORY.md-archival split.
- **Context compression:** ACON (26–54% token reduction), Active Context Compression (agent decides when to consolidate) for session growth.
- **Injection defense:** CaMeL (planner + quarantined data LLM, ~100% AgentDojo block) and Spotlighting (cheap delimiting/encoding of untrusted web + inbound-Telegram content) extend the existing deterministic-policy approach with taint-tracking.
- **Evaluation:** tau²-bench (pass^k reliability), Terminal-Bench, AgentDojo (security) as models for probabilistic testing of cron/memory/tool behaviors.

---

## Open disagreements (panel did not resolve)

1. **Sandbox strictness vs usefulness** (Security ↔ Experience): a macOS sandbox tight enough to matter may break the assistant's usefulness and get switched off. Compromise in backlog: allowlist-only shell *default* on unsandboxed platforms (#13), not a hard sandbox.
2. **Which reading applies** (Growth): product-seeking-adoption (3/10) vs personal-tool-incidentally-public (8/10). Resolved toward product per user's explicit "as good as best competitors" framing — but if it's a personal tool, deprioritize #8/#9/#15/#22 and the composite rises to ~6.5.

## Not verified

- Competitor claims (nanobot, OpenClaw, PicoClaw/QwenPaw) from web sources, not their code; PicoClaw/QwenPaw from SEO listicles — leads only.
- Several finding chains traced-not-executed (heartbeat delivery failure, `""` sandbox on Linux re-exec policy). Shell test binary won't run on this macOS host (finding #1), so injection bypasses are reasoned from source.
- Release integrity (`install.sh`, `release.yml`), Copilot device-flow token storage — not reviewed this pass.
