# joshbot — Architect's Guide

joshbot is a self-hosted Go personal AI assistant (~24.8K LOC non-test, 1,135 test functions across 92 test files). Single binary, zero runtime deps.

**`AGENTS.md` (repo root) is the full agent guide** — key interfaces, code style, naming, concurrency and logging patterns, complete gotchas list. Read it before non-trivial work. This file is the quick index.

## Key facts

- **Go 1.24.0**, module `github.com/bigknoxy/joshbot`
- **~17MB binary**, ~30ms startup
- **Architecture**: goroutine message bus → ReAct agent loop → multi-provider LLM
- **Channels**: CLI (readline) + Telegram (long-polling, telebot)
- **Providers**: openrouter, openai, nvidia, groq, ollama, anthropic, poolside, azure, custom, litellm, github-copilot (device-flow auth via `joshbot auth github-copilot`)
- **Config**: `~/.joshbot/config.json`, env vars with `JOSHBOT_` prefix, two formats (legacy + model-centric)
- **Memory**: MEMORY.md (always in context) + HISTORY.md (appended event log) + fact extraction + consolidation
- **Skills**: SKILL.md files with YAML frontmatter, auto-discovered from workspace, progressive loading
- **Tools**: filesystem, shell (deny-listed), web (exa-cli/DuckDuckGo/MCP), message, skill_registry, memory_search, cron
- **Sessions**: JSONL files at `~/.joshbot/sessions/`, keyed `channel:senderID`
- **Cron**: Scheduled reminders via the `cron` tool (create/list/delete); jobs persisted as JSON at `workspace/cron/jobs.json` (see `internal/cron`)
- **Heartbeat**: Unchecked task scanner via HEARTBEAT.md

## Commands

No Makefile — standard Go tooling.

```bash
go build -o joshbot ./cmd/joshbot
go run ./cmd/joshbot agent -m "hello"        # single-message, non-interactive
go test ./...                                # all tests
go test -v ./internal/tools -run TestShell   # single test
go vet ./... && gofmt -l .                   # CI gates on both
./scripts/verify-local.sh                    # build + onboard + smoke-test heartbeat
```

## Layout

`cmd/joshbot` entrypoint. Under `internal/`: `agent` (ReAct loop), `bus` (message bus),
`channels` (channel.go = Channel interface, telegram.go), `providers` (registry.go = provider routing keys),
`copilot` (github-copilot device-flow auth + provider), `tools`, `memory`, `skills` (+ `skills/bundled/` = the skills embedded in the binary)
(+ `skills/trust.go` = workspace-skill approval store), `session`, `context`, `config`,
`configure`, `cron`, `heartbeat`, `subagent`, `service` (build-tagged per-OS), `learning`,
`integration`, `log`, `redact` (credential and home-path stripping for logs and printed output).

## Panel review

`.claude/skills/panel-review/` runs a five-expert review (security, evals,
experience, growth, Go systems) that debates and scores a change. Charters are in
`references/experts.md` and are harness-portable. See the Panel Review section of
`AGENTS.md` for how to run it.

## Important gotchas

- `internal/` is the source of truth. `pkg/` is a stale incomplete refactor — do not edit.
- The interactive CLI is `runAgentLoop` in `cmd/joshbot/main.go`, not a `Channel` implementation. `internal/channels` holds only the `Channel` interface and Telegram; a dead `cli.go` that implemented it was deleted in v1.42.x after misleading a bug diagnosis.
- `agent.Agent` carries its tool-progress callback **per-request via the context** (`agent.WithSink`), not as a struct field — so concurrent `Process` calls (e.g. Telegram) never cross-deliver events. `runAgentLoop` wires it up only when stdout is a real TTY, to drive the interactive CLI's tool-call lines (`⏺ tool(args)` / `⎿ ok (1.2s)`) and "thinking..." spinner. Non-TTY detection is the `isTTY` package var in `cmd/joshbot/main.go` (type-asserts to `*os.File` + `github.com/mattn/go-isatty`), overridable in tests instead of probing a real terminal.
- A blocking read inside a `select` with `default:` makes shutdown unobservable; `signal.Notify` also disables default termination, so a one-shot signal handler leaves the process unkillable (issue #104).
- All CLI commands work non-interactively (agent -m, onboard --force, configure --provider --api-key, uninstall --force, etc.)
- ExtraBody support for providers needing custom JSON body fields (poolside chat_template_kwargs, etc.)
- Model names are routed by prefix and the prefix is stripped before sending — except for providers listed in `prefixesPartOfModelID` (`internal/config/config.go`), where the prefix is part of the real model ID. Poolside is the only one today: `poolside/laguna-s-2.1` must be sent whole, or the API answers 404.
- Providers need `"enabled": true` in config to activate — omitting it silently disables them.
- Env var nesting uses `__`: `JOSHBOT_PROVIDERS__OPENROUTER__API_KEY`. Shorthand `JOSHBOT_OPENROUTER_API_KEY` still works.
- Session key is computed from `Channel:SenderID` — there is no `SessionKey` field.
- `internal/service/` is build-tagged (`factory_linux.go` / `factory_darwin.go` / `factory_other.go`) — all must export the same signature.
- A Telegram send with a parse mode set fails with `400 can't parse entities` on malformed Markdown/HTML, which is routine in LLM output. `isRetryable` does not cover it, so `Send` retries that part once with `ParseMode` cleared (`isParseEntityError`). Match narrowly on the description text; matching bare `400` would downgrade `chat not found` to plain text and hide real failures.
- Telegram hard-fails over 4096 chars; `Send` splits longer content via `splitMessage` (code-fence aware, byte-indexed).
- Telegram clears a chat action after 5 seconds, so "typing…" needs a keep-alive: `startTyping`/`stopTyping` run one goroutine per chat re-sending every 4s, stopped by `Send` or by `stopCh`. The `botCommands` slice is the single source for both the `SetCommands` menu and the unknown-command fallback — a new `bot.Handle("/x", ...)` must be added there too.
- Shell commands run with an allowlisted environment (`internal/tools/shell_env.go`), not the full parent env — no provider API keys, and anything credential-shaped by name (TOKEN, SECRET, API_KEY, etc.) is stripped even if otherwise allowlisted. A workflow relying on `GH_TOKEN`/`GITHUB_TOKEN` as an env var will not see it; use `gh auth login`'s on-disk config instead.
- `tools.shell_sandbox` (`"off"` default, `"workspace"`) applies Landlock filesystem containment on Linux via a re-exec helper; `tools.shell_sandbox_allow_network` (off by default) gates outbound TCP when sandboxed.
- Workspace skills (anything under `workspace/skills/`, as opposed to the bundled set embedded in the binary from `internal/skills/bundled/`) are inert until an operator runs `joshbot skills trust <name>`; trust is bound to a content hash in `~/.joshbot/skills.trust`, so editing a trusted skill revokes it. `Loader.Create` (used by the skill_registry tool) writes but never approves. A workspace skill that reuses a bundled skill's name replaces it in the registry and is withheld until trusted — it does not silently "take over" the bundled behavior.
- The `cron` tool takes **durations** (`30m`, `2h`, `1d`, `1h30m`), never 5-field cron expressions — `internal/cron` only knows `delay:<d>` and `every:<d>`. It is registered only when a `cron.Service` is passed via `tools.WithCronService`, so the agent is never offered a scheduler that is not running.
- A one-shot cron job's countdown **survives a restart**: `AddJob` stores an absolute `Job.DueAt` (`due_at` in `jobs.json`), so a persisted `delay:30m` fires at the moment it was originally due, and an already-overdue job fires shortly after start instead of being dropped. Legacy jobs with no `due_at` are backfilled as due one duration from load (the old behaviour). Recurring jobs run until deleted; one-shot jobs delete themselves once they fire.
- Session files are written through `writeFileAtomic` (`internal/session/manager.go`) at mode `0600` — never `os.WriteFile` to a fixed `<path>.tmp`. The temp name must be unique per writer (`os.CreateTemp`), because the gateway and a concurrent `agent -m` share `~/.joshbot/sessions` and the `sync.RWMutex` only guards one process. `Load` is deliberately lenient: unparseable lines are skipped, not fatal, and the original file is copied to `<session-id>.jsonl.corrupt`. Do not "fix" that back into a hard error — there is one session per `channel:senderID` and no CLI to delete one, so a fatal parse means that user is permanently broken.
- **A compaction record is stored in the session and must stay singular**: once the history crosses `agents.defaults.compaction_threshold`, `checkAndCompactContext` summarizes it and `applyCompaction` replaces the summarized prefix with one `session.Message` carrying `Compaction: true`, always at index 0. Three rules keep it correct. The write-back happens **after** `reactLoop` returns, not inside it, because `Process` slices `sess.Messages[startSessionLen:]` for the history log and shrinking the session first would slice out of range. `buildMessages` holds the record out of the memory-window truncation — letting the window slide over it silently discards the whole earlier conversation. And the replaced messages are appended to `<session-id>.history.jsonl` via the optional `session.Archiver` interface before they leave the live session; if that append fails the compaction is abandoned, since recomputing a summary is cheaper than destroying history. Note `checkAndCompactContext` runs only **after tool execution** inside the ReAct loop, so a turn that answers without calling a tool never compacts at all — a test scripted without tool calls exercises nothing.
- **Everything joshbot prints or logs is redacted; session files are not**: `internal/redact` wraps the log writer (`internal/log/logger.go`) and `joshbot status`, so a credential appearing in a tool result never reaches the log. Session JSONL is deliberately exempt — rewriting content on save would mangle legitimate text and add a second corruption path, so data at rest relies on the `0600` mode instead; `TestSessionContentIsStoredVerbatim` pins that boundary, and `internal/redact` documents it. Three traps if you extend the patterns. **The scheme is kept and the token is redacted, never the reverse**: an `Authorization` value is `<scheme> <credential>`, and an earlier version recognised only `Bearer|Basic`, so GitHub's own `Authorization: Token <secret>` fell through to the assignment rule, which blanked the word `Token` and published the credential after it. Both the header rule and the assignment rule now capture an optional leading scheme word and preserve it. **A name fragment must end the identifier**, apart from a plural `s` and separator-led segments (`SECRET_KEY_ID`): matching it as a bare substring rewrote `Author:`, `unauthorized:` and `secretariat:`, all routine tool output. That is also why `AUTH` is in `redact.SecretNameFragments` — which widens the shell-environment screen in `internal/tools`, sharing the list on purpose — but is excluded from the assignment rule by `assignmentFragments()`. **And the cheap gate in front of each regex must never be narrower than the regex itself** — gating on the underscored `api_key` spelling silently let `api-key: <secret>` through. All-numeric values are exempt on purpose — `Max tokens: 8192` in `joshbot status` matches TOKEN plus a plural `s`, and no detected key class is bare digits. The assignment gate is a hand-written walk (`hasAssignmentCandidate`) rather than a substring test, because Go's regexp costs ~650ms per megabyte on a case-insensitive alternation and `token`/`key`/`secret` appear constantly in ordinary prose without being assignments; the walk mirrors the regex's name part exactly, so widening one means widening the other.
- **`joshbot sessions` is not a resume feature, and `.jsonl` alone does not identify a session**: sessions are keyed `channel:senderID` and loaded on every inbound message, so there is one per user per channel and nothing to select; the subcommands exist to inspect and clear. Two traps live here. `internal/session` now owns sidecars that also end in `.jsonl` — `.history.jsonl` (compaction archive) and `.jsonl.archive-<ns>` (reset) — so anything enumerating the sessions directory must go through `isSessionFile`, never a bare `strings.HasSuffix(name, ".jsonl")`; the compaction archive was reported as a phantom session named `<id>.history` until that was centralised. And urfave/cli v2 **stops flag parsing at the first positional argument**, so a flag written after an id is silently dropped — `prune <id> --force` declined without deleting, and `show <id> --last 2` printed the whole transcript. `parseSessionArgs` in `cmd/joshbot/sessions_cmd.go` reads those trailing flags — all three of them, including `--older-than`, whose omission made `prune <id> --older-than 30d` report an unknown flag; any new subcommand taking both an argument and a flag needs the same treatment. A third trap: an `Info` for a session that would not scan must carry the file's real mtime, because `PruneOlderThan` compares `UpdatedAt` and a zero time reads as infinitely old — a transient read error was enough to make `--older-than` delete a session of any age. Session IDs are attacker-influenced (the sender half comes from Telegram), so `ValidateSessionID` gates every path-building entry point.
- **The bundled skills are embedded in the binary, not read from disk**: `internal/skills/bundled/` is pulled in with `//go:embed` (`internal/skills/bundled.go`). It used to be the repo-root `skills/` directory found via the *relative* path `filepath.Join("skills")`, which resolves against the process working directory — so the bundled set loaded only when joshbot was run from a checkout of its own source tree, and every real installation reported "No skills found" while `trust.go` claimed they "arrive with the binary". Consequences to keep in mind: a bundled skill's `Path` is now an embed path, not a filesystem path, so its content is cached at discovery and `Delete` refuses it; `Loader.bundledDir` is empty in production and exists only so tests can substitute a fixture directory; and adding a skill means adding a directory under `internal/skills/bundled/`, which requires a rebuild to take effect.
- A bundled skill that names a tool joshbot does not have fails only at runtime, as a confident-sounding error. `TestBundledSkillsOnlyReferenceRegisteredTools` in `internal/tools` catches this — it keys off the phrase ``the `x` tool``, so keep that phrasing when documenting a tool in a skill.

## Website (site/)

Two pages in `site/`:
- `index.html` — Landing page (what/how/why)
- `architecture.html` — Official architecture docs + diagrams

**Both MUST stay in sync with the codebase.** Update when major features, architecture, config, or capabilities change.

## HARD RULE: documentation ships with the change

**Every change that alters observable behaviour updates the docs in the same PR.** Not
a follow-up, not an issue — the same PR. A change is not done until the docs match.

This applies to `*.md` and `*.html` alike:

| If you change | Update |
|---|---|
| A CLI command, flag or its output | `README.md`, `docs/INSTALL.md` |
| A config key, default or its semantics | `README.md`, `docs/INSTALL.md`, `site/architecture.html` |
| A security boundary or its limits | `SECURITY.md`, `AGENTS.md` gotchas |
| Architecture, packages, data flow | `site/architecture.html`, `CLAUDE.md` layout |
| A capability users would notice | `README.md`, `site/index.html`, `CHANGELOG.md` |
| A behaviour that would trip up an agent | `AGENTS.md` gotchas, `CLAUDE.md` gotchas |
| Anything in `internal/tools/` the agent calls | the relevant `internal/skills/bundled/*/SKILL.md` |

Rules that make this stick:

- **Verify, do not assume.** Every documented command, flag, config key and code path
  gets read in the source — or run — before you write it down. Config keys must match
  the `json:`/`mapstructure:` struct tags exactly.
- **No unverifiable numbers.** LOC, test counts, tool counts and binary sizes must be
  measured at the time of writing or removed. A stale number is worse than none: it
  reads as authoritative.
- **Stale claims count as bugs.** Deleting a claim that is no longer true is as
  important as adding one that is. Drift runs both ways.
- **`site/*.html` is not optional.** It is the public face and it drifts fastest,
  because nothing fails when it is wrong.

Prose alone has already failed once: this file said the site MUST stay in sync while
the site sat eight releases out of date. Treat the checklist below as the gate.

## Pre-release checklist

```bash
go build ./cmd/joshbot
go vet ./...        # CI gates on this
gofmt -d .          # MUST be empty
go test -race ./... # MUST pass
# CI also fails if total coverage drops below 45%
```

Then, before tagging — no code change ships without this:

- [ ] `README.md` — commands, flags, config keys and examples match the code
- [ ] `docs/INSTALL.md` — install path, first-run flow and command list are current
- [ ] `site/index.html` / `site/architecture.html` — capabilities, architecture, counts
- [ ] `SECURITY.md` — the security model matches what is actually enforced
- [ ] `CLAUDE.md` / `AGENTS.md` — gotchas cover anything that would surprise an agent
- [ ] `internal/skills/bundled/*/SKILL.md` — bundled skills do not instruct the agent to do something
      the tools no longer permit
- [ ] `CHANGELOG.md` — an entry exists under `[Unreleased]`
- [ ] Any count or size quoted anywhere was re-measured, not carried over

## PR & release

See `.github/PR_RULES.md`. Branch from main (never commit directly), conventional commits,
squash & merge. Releases: push to main → **wait for CI green** → then `git tag vX.Y.Z && git push origin vX.Y.Z`.