# Dogfooding & bug reproduction notes

How the 2026-08-15 "joshbot says it'll check but never follows up" investigation was
run, on which machine, and what it proved. Read this before re-running any repro or
re-using this box for joshbot testing.

## Environment

- **Host**: dev box (`hostname`: unknown to agents — ask `hostname` on the box), Linux,
  root user. **This is NOT the machine running the production Telegram gateway.**
- **Repo**: `/root/code/joshbot` (github.com/bigknoxy/joshbot), Go toolchain per `go.mod`
  (1.24). Working branch was `feat/proxmox-skill`; `main` fast-forwarded to `48086a1`
  (v1.55.0) before testing.
- **Real joshbot home on this box**: `/root/.joshbot` — a **dev CLI instance** (poolside
  `laguna-s-2.1`, model-centric config, no Telegram key, empty logs). Not production.
- **API keys available in `~/.bashrc` (names only)**: `JOSHBOT_OPENROUTER_API_KEY`,
  `JOSHBOT_NVIDIA_API_KEY`, `JOSHBOT_TELEGRAM_BOT_TOKEN`, `BRAVE_API_KEY`, `POOLSIDE_API_KEY`.
  - `JOSHBOT_OPENROUTER_API_KEY` is **dead** (OpenRouter answers `401 {"error":{"message":"User not found."}}`
    on `/chat/completions`; `GET /models` returns 200 to *any* key, so it is not a valid probe —
    the AGENTS.md unauthenticated-/models gotcha).
  - The **poolside** key is live and is the one used for dogfooding.

## How to point joshbot at a sandbox home

`DefaultHome = filepath.Join(os.Getenv("HOME"), ".joshbot")` (`internal/config/config.go:106`).
The lever is **`HOME`**, not `JOSHBOT_HOME`:

```bash
export HOME=/root/code/joshbot/.dogfood   # → home = /root/code/joshbot/.dogfood/.joshbot
go build -o /root/code/joshbot/.dogfood/joshbot ./cmd/joshbot   # after a clean, rebuild
```

## Streaming only engages on a TTY

The stream sink (and the interactive editor) are gated on `isatty`. Non-interactive
`agent -m` never streams, so it does NOT reproduce streaming bugs. Allocate a pty with
`script`:

```bash
printf '<prompt>\n' | HOME=/root/code/joshbot/.dogfood \
  timeout 100 script -qec "./joshbot agent --debug" /dev/null > out/turnN.log 2>&1
```

Strip ANSI to read: `sed -e 's/\x1b\[[0-9;]*[a-zA-Z]//g' out/turnN.log`.

## Reproduction runs (2026-08-15)

Sandbox: `/root/code/joshbot/.dogfood/` — built binary `joshbot`, config
`.joshbot/config.json`, seeded `workspace/skills/proxmox/SKILL.md` (untrusted) and
`workspace/memory/MEMORY.md` ("Proxmox skill saga"). All sandbox artifacts were deleted
after the run (they held a live key) — see "Cleanup".

| # | Config | Prompt | Result |
|---|--------|--------|--------|
| turn1 | OpenRouter glm-5.2, timeout 45s | "build proxmox skill, narrate" | Converged in 42s, answered. Model narrated ("I'd be happy to dig into...") then ran tools. Auto-skill-creation fired (`list_dir-skill`). |
| turn2 | OpenRouter glm-5.2, timeout 20s (used ~/.joshbot home by mistake — JOSHBOT_HOME ignored) | deep exploration | Model ran 20+ tool calls in real `~/.joshbot/workspace`, was killed by external `timeout 70` (exit 124). No final answer. |
| turn3/3b | OpenRouter glm-5.2, timeout 20s | deep exploration | `401 User not found` — dead OR key. Key verified dead via curl. |
| **turn4** | **poolside laguna-s-2.1, `max_tool_iterations: 4`, timeout 30s** | deep exploration + implement | **REPRODUCED.** `Hit max iterations max=4` at t≈30s; max-iteration reply (agent.go:866-869) **never reached the screen**; sessions dir left **empty**. |

## What turn4 proved

User-visible sequence: model narration + `⏺ list_dir(...)` / `⎿ ok` tool lines, then
**silence** — no final answer, no "I've been working on this for a while", no `/resume` hint.
`grep` for `been working on this|found so far|/resume` in the pty capture: **0 matches**.

Failure cascade (all four post-loop writes failed — the 4 iterations exhausted the 30s
deadline, so max-iterations and timeout fired together):

```
Hit max iterations max=4
Failed to save checkpoint session=cli:cli_user error="context cancelled"      ← /resume would have nothing to resume
Failed to append history error="context deadline exceeded"
Skipping compaction write-back: archive failed error="context cancelled"
Failed to save session error="context cancelled"                              ← sessions/ dir left EMPTY
Failed to save updated topic error="context cancelled"
```

## Diagnosis (answers the original question)

It is **not** primarily a Telegram integration problem. It is a **core joshbot loop +
delivery** problem that shows up on the streaming path (and Telegram is the streaming
channel, so it shows up there loudest):

- **Bug A — reply swallow (delivery).** `reactLoop` returns the max-iteration reply
  (`agent.go:866-869`) and `Process` returns the timeout reply (`agent.go:520`) as plain
  text with a nil error, **after** streaming. The CLI (`cmd/joshbot/main.go:1945`) and the
  Telegram gateway both decide "did the user already see the answer?" by "did *anything*
  stream this turn?" (`progress.didStream()` / `TelegramStreamer.Finish` returning
  `delivered && !broken && shown == buf`). Anything streamed ⇒ the final reply is dropped.
  Result: user sees the "Let me dig into..." narration and then nothing — no timeout
  notice, no max-iteration notice, no `/resume` hint.
- **Bug B — session lost on a failed/truncated turn (persistence).** Pure-timeout path
  returns at `agent.go:520` before the save at `agent.go:549`, so the whole turn is never
  persisted. When the deadline fires right at the max-iteration boundary, every post-loop
  write dies on the expired context (turn4: checkpoint, history, compaction, session,
  topic). Next turn loads the pre-turn session ⇒ the model has **no memory of the previous
  turn's tool results** and re-explores from scratch ("Let me actually dig in... checking
  the skill registry, any existing files..." — the transcript's turn 2 verbatim).
- **Bug C — model behavior (helpfulness).** Both glm-5.2 and poolside narrate intent
  ("I'd be happy to dig into...", "Let me dig into what we've got...") with a tool call,
  then keep iterating instead of converging to a short result. With loop/time limits this
  ends in A+B. This is the "missing the mark on helpfulness" root — joshbot can't fix the
  model, but it must surface *why* it stopped.

## Fix directions (for the PR/issue)

- Delivery: the "was the answer shown?" signal must compare what was streamed against the
  **actual response**, not "anything streamed". Either stream the synthesized replies
  (timeout/max-iteration) through the sink, or have `Process` report "not streamed" so the
  caller force-publishes. Applies to both `runAgentLoop` (main.go:1945) and the gateway's
  `streamer.Finish` gating.
- Persistence: persist the session (and checkpoint) even when the turn fails; the
  timeout path must not skip `sessions.Save`. Writes after a fired deadline should use a
  fresh short-lived context (like the checkpoint save already does), not the dead turn ctx.

## Fix status (issue #283, branch `fix/streaming-reply-swallow-session-loss`)

Implemented and verified by regression tests (TDD: tests written red first, then fixed):

- **Bug A (delivery)** — `reactLoop`'s max-iteration reply and `Process`'s timeout reply
  are now emitted through the stream sink (`sink(StreamEvent{Delta: resp})`) before
  returning, so the CLI's `didStream()` and the Telegram streamer's `Finish` gating both
  count them as delivered. Covered by `TestStreaming_MaxIterationsReplyReachesSink` and
  `TestTimeoutReplyReachesSink` (`internal/agent/stream_reply_swallow_test.go`). The
  general `"Error processing request: ..."` path remains out of scope (it is already
  reported out of band as a non-200/non-`stop` stream) — documented follow-up.
- **Bug B (persistence)** — new `persistenceCtx` helper (10s bounded fresh context when
  the turn context has fired, unchanged otherwise) is used for every post-loop write:
  history append, compaction, session save ×2, the checkpoint save (which keeps its own
  5s bound), and a new session save on the pure-timeout path. Covers cancellation too.
  Covered by `TestTimeoutTurnPersistsSession` and
  `TestMaxIterationsNearDeadlinePersistsCheckpointAndSession`.
- **Bug C (prompt)** — WORK DISCIPLINE in `buildCoreIdentity()` now reads "Act before you
  narrate: run the tools, then report what you found. Never reply with an intention
  instead of doing it." Covered by `TestCoreIdentityActDontAnnounce` (prompt lint).

Verification: `go build ./...`, `go vet ./internal/agent`, `gofmt -l` clean; new tests
green; `go test ./internal/agent` full suite passes. `go test ./cmd/joshbot` has **12
pre-existing failures** on this box (onboard/configure/provider-credential tests) that
reproduce identically on clean `main` (48086a1) — none are introduced by this change.

## Cleanup

The live poolside key (`.joshbot/config.json`) and the 20 MB binary were deleted; the rest
of the sandbox (turn logs, seeded workspace, `ENV.md`) is kept under `/root/code/joshbot/.dogfood/`
and `.dogfood/` is now in `.gitignore`, so it can never be committed. `docs/DOGFOODING.md` and
`tasks/lessons.md` retain the repro steps without secrets. Rebuild + reseed to reuse:

## Lessons

- `HOME` is the home lever for joshbot, not `JOSHBOT_HOME` (which is only an env-override
  namespace for config keys). See `tasks/lessons.md`.
- `grep -o 'sk-or-[A-Za-z0-9]*'` truncates keys at the first hyphen. Use
  `sk-or-v1-[A-Za-z0-9_-]+`. See `tasks/lessons.md`.
- OpenRouter `GET /models` is unauthenticated; a 200 proves nothing about a key. Probe
  with a chat completion (or `joshbot preflight`), never `/models`.
