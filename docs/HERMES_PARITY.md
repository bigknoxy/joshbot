# Hermes Parity Plan

Gap analysis (2026-08-14) against the Hermes agent harness (Nous Research,
[NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent)), and the
plan to bring joshbot's *run quality* — resilience and configuration — on par,
while staying a small single Go binary. Progress is tracked by the checkboxes
here; each item ships as its own PR with tests and docs.

## Verdict

joshbot's decision logic is already Hermes-grade: fallback classification,
compaction, checkpoints, hash-pinned skill/MCP trust, sandboxing, and
`joshbot preflight` (which Hermes has no equivalent of). The gaps are the
**resilience layer** that makes Hermes feel seamless, and the **configuration
path** from install to working multi-provider fallback.

## How Hermes does fallback (the bar)

- Retries the *same* provider up to `api_max_retries: 3` on transient errors
  (429/5xx) before moving down the chain; 401/403/404 fail over immediately.
- Reset-aware cooldown: a rate-limited provider is skipped until its reset
  window passes — no per-turn timeout tax on a known-dead primary.
- Each fallback entry names its own provider **and** model (joshbot adopted
  this in v1.52.1).
- Key rotation is tried before cross-provider fallback (joshbot: at par via
  `KeyRotatingProvider`).
- The switch preserves conversation/tools/context; each new user turn retries
  the primary.

## Gap analysis

| Dimension | Hermes | joshbot today | Status |
|---|---|---|---|
| Fallback triggers | status-class based | status-class based, well-reasoned | at par |
| Same-provider retry | 3 retries w/ backoff | none — one 500 blip switches models | **gap (high)** |
| Retry-After / cooldown | reset-aware | nothing waits; dead primary re-dialled every turn | **gap (high)** |
| Mid-stream failure | retried / failed over | partial text saved as a *successful* turn | **gap (high)** |
| Fallback visibility | surfaced to user | log-only | gap (med) |
| Error legibility | actionable messages | raw upstream JSON, no next step | gap (med) |
| Setup wizard | `hermes setup` incl. fallback | no CLI writes `fallback_order` at all; menu shows 6 of ~11 providers | **gap (high)** |
| Credential check | real auth | `/models` probe — OpenRouter 200s any key → false "✓ validated" | **gap (high)** |
| Config format | one YAML + `config migrate` | two formats; onboard writes legacy + empty `models_config` stub | gap (med) |
| Preflight / doctor | — | `joshbot preflight` | **ahead** |
| Compaction & checkpoints | session lineage | archive-before-replace + `/resume` | at par |
| Sessions UX | search, `--resume`, recap | inspect/clear only | gap (med) |
| Skills / MCP trust | hub + scanning + tiers | hash-pinned trust (stronger model) | at par |
| Channels / voice / IDE / desktop | 25+ platforms etc. | CLI + Telegram + Discord + API | out of scope |

## Plan

### Phase 1 — resilience core

- [x] Same-provider retry with jittered backoff before fallback
      (`providers.<name>.max_retries`, default 2, `0` = immediate failover;
      retries only fallback-class errors)
- [x] Honor `Retry-After`: carried on `providers.FallbackError`, used as the
      retry/cooldown wait
- [x] Provider health tracking: cooldown table on `MultiProvider`; skip a
      failing provider for a window (seeded from Retry-After), re-probe on
      expiry; visible in `joshbot status`
- [x] Mid-stream failover: a stream dying partway is retried (same then next
      provider) instead of saving partial text as a successful turn
- [x] Visible fallback notice via the sink ("nvidia rate-limited — answered by
      poolside/laguna-s-2.1"), with a config toggle
- [ ] Typed errors end-to-end: finish `FallbackError` adoption; retire
      status-code extraction by error-string scanning
- [x] Fallback chaos harness checked in: fake HTTP providers (429 +
      Retry-After, strict-404 fallback, dying streams) exercising the real
      `MultiProvider`

### Phase 2 — configuration parity

- [x] Fix false-positive credential validation (1-token completion, or no
      checkmark for unauthenticated `/models` endpoints)
- [x] `joshbot configure fallback` (flag + interactive) — first CLI path that
      writes a fallback chain
- [x] Onboard offers fallback when a second provider key is present in env
- [ ] Converge on one canonical config format + `joshbot configure migrate`;
      stop serializing the empty `models_config` stub
- [x] Full provider menu derived from `configure.SupportedProviders()`
- [x] Actionable error mapping at the `Process` boundary (401 → configure
      hint, 429-all-failed → fallback hint, model 404 → `/model`/preflight,
      ollama 404 → `ollama pull`)

### Phase 3 — session & loop polish

- [ ] `joshbot agent --continue` + short recap on resume
- [ ] Session search over the JSONL store
- [ ] Empty LLM content surfaces as a provider error, not "I've processed
      your request."
- [ ] Esc interrupts a running CLI turn, marked interrupted in the session

### Phase 4 — prove it stays fixed

- [ ] Chaos harness runs in CI (fake providers, no live keys)
- [ ] liveeval gains fallback-continuity and misconfiguration-message tasks
- [ ] One "zero to two providers with fallback" recipe replaces the three
      parallel config explanations in README

## Deliberately out of scope

Hermes's weight joshbot will not chase: 25+ messaging platforms, voice/wake
word, desktop apps, IDE (ACP) adapters, skill marketplaces, cloud sandbox
backends, DSPy self-evolution, SQLite session storage. Keep and advertise
what's already ahead: preflight, fail-loud onboarding, the `agent -m`
exit-code contract, hash-pinned trust, Landlock/Seatbelt sandboxing, and the
single ~19 MB binary.
