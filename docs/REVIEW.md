# joshbot Review Report

Generated: 2026-02-14

---

## Documentation Review (README.md)

### GAPS — Completely Missing

| # | Issue | Severity |
|---|-------|----------|
| 1 | **No uninstall instructions** — no guidance on `pip uninstall`, cleaning `~/.joshbot/`, or env vars | HIGH |
| 2 | **No LICENSE file** — says MIT but no actual LICENSE file exists | HIGH |
| 3 | **No voice transcription setup** — advertised feature but setup path (Groq key) is hidden | HIGH |
| 4 | **No troubleshooting / FAQ section** — common failures undocumented | MEDIUM |
| 5 | **No upgrade instructions** | MEDIUM |
| 6 | **No proxy config docs** — `TelegramConfig.proxy` exists but isn't in README | MEDIUM |
| 7 | **`custom` and `vllm` providers undocumented** — self-hosting users have no guidance | MEDIUM |
| 8 | **No CONTRIBUTING guide** | MEDIUM |
| 9 | **`api_base` / `extra_headers` provider options undocumented** | MEDIUM |
| 10 | **Heartbeat feature advertised but never explained** | MEDIUM |
| 11 | **`gateway` config section (`host`/`port`) not documented** | LOW |
| 12 | **No `.env.example` file** | LOW |
| 13 | **No CHANGELOG** | LOW |

### ISSUES — Incorrect or Misleading

| # | Issue | Status |
|---|-------|--------|
| 1 | **Line 12 garbled text** — "Provider LLM**- **Multi-" mangled markdown | |
| 2 | **Line 23 placeholder URL** — `yourusername/joshbot` should be `bigknoxy/joshbot` | |
| 3 | **No venv in install instructions** — risky bare-metal pip install | |
| 4 | **`allow_from` values unclear** — should say "Telegram user ID as string (number)" | |
| 5 | **Empty `allow_from: []` silently allows everyone** — security-critical default undocumented | |
| 6 | **Docker onboarding gap** — no guidance on interactive `onboard` in container context | |
| 7 | **Config example incomplete** — missing `proxy`, `gateway`, `api_base`, `workspace`, `max_tool_iterations` | |
| 8 | **Prerequisites buried at bottom** — Python 3.11+ requirement after Docker section | |

---

## QA Review

### CRITICAL (5 issues)

| # | Issue | File | Status |
|---|-------|------|--------|
| 1 | **Shell tool deny-list trivially bypassed** — base64, env vars, pipes, subshells, command chaining all work | `tools/shell.py` | |
| 2 | **Filesystem symlink bypass** — symlink inside workspace -> read any file on system | `tools/filesystem.py` | |
| 3 | **Message bus silently drops messages** — if all handlers throw, message is lost with no retry/dead-letter | `bus/queue.py` | |
| 4 | **Memory consolidation partial failure** — MEMORY.md writes but HISTORY.md fails = inconsistent state | `agent/loop.py` | |
| 5 | **API keys set as env vars** — could leak via debug logging or child processes | `providers/litellm_provider.py` | |

### HIGH (6 issues)

| # | Issue | File | Status |
|---|-------|------|--------|
| 6 | **Shell tool ignores workspace restriction** — `cd / && ls` bypasses `cwd` restriction | `tools/shell.py` | |
| 7 | **Telegram allowlist checked after initial processing** — metadata could leak | `channels/telegram.py` | |
| 8 | **Session cache grows unbounded** — no LRU eviction, memory exhaustion risk | `session/manager.py` | |
| 9 | **Cron recurring job stops permanently on error** — no retry/reschedule | `cron/service.py` | |
| 10 | **Default `restrict_to_workspace: false`** — new users have no file isolation | `config/schema.py` | |
| 11 | **Heartbeat re-triggers same tasks** — HEARTBEAT.md never cleared after processing | `heartbeat/service.py` | |

### MEDIUM (6 issues)

| # | Issue | File | Status |
|---|-------|------|--------|
| 12 | Dead code / confusing `if False` in skills_summary_fn | `main.py` | |
| 13 | Race condition in EditFileTool (TOCTOU between read and write) | `tools/filesystem.py` | |
| 14 | Bus handler exception breaks other handlers in the chain | `bus/queue.py` | |
| 15 | No timeout on LLM calls during memory consolidation | `agent/loop.py` | |
| 16 | CLI PromptSession never properly closed | `channels/cli.py` | |
| 17 | No input validation (empty/huge messages) | `agent/loop.py` | |

### LOW (3 issues)

| # | Issue | File | Status |
|---|-------|------|--------|
| 18 | Markdown-to-HTML regex edge cases in Telegram | `channels/telegram.py` | |
| 19 | Inconsistent error return types across tools | various | |
| 20 | No input validation on user messages | `agent/loop.py` | |

---

## Recommended Test Coverage Priority

```
tests/
├── test_shell_security.py       # CRITICAL - bypass vectors
├── test_filesystem_security.py  # CRITICAL - symlink attacks
├── test_message_bus.py          # CRITICAL - message loss
├── test_memory_consolidation.py # HIGH - partial failure
├── test_session_manager.py      # HIGH - corruption, memory
├── test_config_loading.py       # MEDIUM - corrupted config
└── test_integration.py          # Full flow tests
```

---

## What's Done Well

- Clear 3-step Quick Start flow
- Multi-provider config examples with concrete JSON
- Memory system explanation (MEMORY.md vs HISTORY.md lifecycle)
- Progressive skill loading levels well-explained
- Shell safety documentation builds trust
- Environment variable convention with examples
- Clean architecture section
- Complete tools table (all 10 verified)
- Chat commands table matches implementation
