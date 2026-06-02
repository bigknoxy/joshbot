# joshbot — Architect's Guide

joshbot is a self-hosted Go personal AI assistant (~14.5K LOC non-test, 520 test functions in 46 files). Single binary, zero runtime deps.

## Key facts

- **Go 1.24.0**, module `github.com/bigknoxy/joshbot`
- **~16MB binary**, ~30ms startup
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

## Important gotchas

- `internal/` is the source of truth. `pkg/` is a stale incomplete refactor — do not edit.
- All CLI commands work non-interactively (agent -m, onboard --force, configure --provider --api-key, uninstall --force, etc.)
- ExtraBody support for providers needing custom JSON body fields (poolside chat_template_kwargs, etc.)

## Pre-release checklist

```bash
go build ./cmd/joshbot
gofmt -d .          # MUST be empty
go test -race ./... # MUST pass
```