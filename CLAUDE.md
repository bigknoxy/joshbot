# joshbot — Architect's Guide

joshbot is a self-hosted Go personal AI assistant (~19.6K LOC non-test, ~850 test functions across 55 files). Single binary, zero runtime deps.

**`AGENTS.md` (repo root) is the full agent guide** — key interfaces, code style, naming, concurrency and logging patterns, complete gotchas list. Read it before non-trivial work. This file is the quick index.

## Key facts

- **Go 1.24.0**, module `github.com/bigknoxy/joshbot`
- **~17MB binary**, ~30ms startup
- **Architecture**: goroutine message bus → ReAct agent loop → multi-provider LLM
- **Channels**: CLI (readline) + Telegram (long-polling, telebot)
- **Providers**: openrouter, openai, nvidia, groq, ollama, anthropic, poolside, azure, custom, litellm
- **Config**: `~/.joshbot/config.json`, env vars with `JOSHBOT_` prefix, two formats (legacy + model-centric)
- **Memory**: MEMORY.md (always in context) + HISTORY.md (appended event log) + fact extraction + consolidation
- **Skills**: SKILL.md files with YAML frontmatter, auto-discovered from workspace, progressive loading
- **Tools**: filesystem, shell (deny-listed), web (exa-cli/DuckDuckGo/MCP), message, skill_registry, memory_search
- **Sessions**: JSONL files at `~/.joshbot/sessions/`, keyed `channel:senderID`
- **Cron**: Scheduled jobs via cron.yml in workspace
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
`channels` (cli.go, telegram.go), `providers` (registry.go = provider routing keys),
`tools`, `memory`, `skills`, `session`, `context`, `config`, `cron`, `heartbeat`,
`subagent`, `service` (build-tagged per-OS), `learning`, `integration`.

## Important gotchas

- `internal/` is the source of truth. `pkg/` is a stale incomplete refactor — do not edit.
- All CLI commands work non-interactively (agent -m, onboard --force, configure --provider --api-key, uninstall --force, etc.)
- ExtraBody support for providers needing custom JSON body fields (poolside chat_template_kwargs, etc.)
- Providers need `"enabled": true` in config to activate — omitting it silently disables them.
- Env var nesting uses `__`: `JOSHBOT_PROVIDERS__OPENROUTER__API_KEY`. Shorthand `JOSHBOT_OPENROUTER_API_KEY` still works.
- Session key is computed from `Channel:SenderID` — there is no `SessionKey` field.
- `internal/service/` is build-tagged (`factory_linux.go` / `factory_darwin.go` / `factory_other.go`) — all must export the same signature.
- Telegram hard-fails over 4096 chars; joshbot does not split messages.

## Website (site/)

Two pages in `site/`:
- `index.html` — Landing page (what/how/why)
- `architecture.html` — Official architecture docs + diagrams

**Both MUST stay in sync with the codebase.** Update when major features, architecture, config, or capabilities change.

## Pre-release checklist

```bash
go build ./cmd/joshbot
go vet ./...        # CI gates on this
gofmt -d .          # MUST be empty
go test -race ./... # MUST pass
# CI also fails if total coverage drops below 45%
```

## PR & release

See `.github/PR_RULES.md`. Branch from main (never commit directly), conventional commits,
squash & merge. Releases: push to main → **wait for CI green** → then `git tag vX.Y.Z && git push origin vX.Y.Z`.