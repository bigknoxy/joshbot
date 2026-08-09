# AGENTS.md - Coding Agent Guidelines for joshbot

## Project Overview

joshbot is a lightweight personal AI assistant (~30,100 LOC Go non-test, 1,330 test functions across 114 test files — measured 2026-08-10) with self-learning memory, auto-skill-creation from tool usage patterns, and Telegram and Discord integration. Architecture: goroutine-based message bus decoupling chat channels from a ReAct agent loop backed by multi-provider LLM via OpenRouter-compatible APIs.

Module: `github.com/bigknoxy/joshbot`. Go 1.24.0.

### ALL CLI commands work non-interactively

| Command | Non-interactive | Notes |
|---------|----------------|-------|
| `agent -m "..."` | ✅ | Single message, exits after response |
| `onboard --force` | ✅ | `--force` skips all prompts (backup + defaults) |
| `configure --provider --api-key ...` | ✅ | All flags |
| `configure --list` | ✅ | |
| `status` | ✅ | |
| `version` | ✅ | |
| `update` | ✅ | |
| `uninstall --force` | ✅ | |

## Build & Run

No Makefile. Standard Go tooling:

```bash
go build -o joshbot ./cmd/joshbot     # Build
go install ./cmd/joshbot               # Install
go run ./cmd/joshbot agent             # Dev mode

# Runtime commands
./joshbot onboard     # First-time setup wizard
./joshbot agent       # Interactive CLI mode
./joshbot agent -m "..."  # Single message mode
./joshbot agent --debug  # CLI mode with debug logging
./joshbot gateway     # Telegram + all channels
./joshbot gateway --debug # Gateway with debug logging
./joshbot status      # Show config/status
./joshbot configure   # Re-run config wizard
./joshbot update      # Self-update
./joshbot configure --provider openrouter --api-key sk-or-...  # Non-interactive
./joshbot uninstall --force  # Non-interactive removal

# Docker
docker build -t joshbot .
docker run -it joshbot gateway
```

Docker: `docker build -t joshbot . && docker run -it joshbot gateway`

## Testing

```bash
go test ./...                                          # All tests
go test -race ./...                                    # With race detector
go test -v ./internal/tools -run TestShell             # Single test
```

Tests colocated with `_test.go` suffix. Integration tests in `tests/` plus `internal/integration/`. Python test scripts in `tests/` (legacy from migration).

### Behavioural eval harness (`internal/agent`)

`evalharness_test.go` runs the **real ReAct loop** against a scripted provider that
replays a fixed sequence of model turns and records every request. No network, no
API key, no clock dependence — it runs in normal CI.

```bash
go test ./internal/agent -run 'TestEval_' -v
```

Add a scenario by declaring a `trajectoryScenario` (scripted turns, tool outputs,
seeded history, budget/window knobs) and passing it to `runTrajectoryEval`. Assert
on the **trajectory** — `res.requests` (what was sent to the model), `res.invocations`
(what was dispatched), `res.sess` (what was persisted) — not on the reply text.

Always call `assertProtocolInvariants(t, res)`. It enforces the wire-format rules an
OpenAI-compatible provider rejects with a 400: every tool message carries a
`tool_call_id`, every id answers a call announced by an earlier assistant message, and
every announced call receives a result. Two shipped bugs were found this way, both of
which broke only long conversations. Keep this call in every new scenario so evals
added later inherit the checks.

Do not confuse this with `prompt_eval_test.go`, `prompt_optimizer_test.go` and
`prompt_variants_test.go` — those are a **prompt lint**. They score the system prompt
string with `strings.Contains` and never invoke the agent, a model, or a tool. They
cannot catch behavioural regressions.

## Linting & Formatting

```bash
go fmt ./...
go vet ./...
go mod tidy
```

## Code Architecture

```
cmd/joshbot/main.go            -- CLI entry (urfave/cli/v2), service wiring, ~4,914 LOC
  internal/
    agent/agent.go             -- ReAct loop (max 20 iterations)
    agent/context.go           -- System prompt assembly (identity files + memory + skills)
    bus/bus.go                 -- Channel-based message bus (Inbound/OutboundMessage in bus.go)
    channels/channel.go        -- Channel interface (Telegram and Discord implement it)
    channels/telegram.go       -- Telegram long-polling channel (telebot)
    channels/discord.go        -- Discord gateway-websocket channel (discordgo)
    config/config.go           -- JSON config, env overrides (JOSHBOT_ prefix)
    configure/configure.go     -- Config wizard, provider selection, non-interactive configure
    context/context.go         -- Context propagation, registry, budget manager
    copilot/auth.go            -- GitHub Copilot auth flow
    cron/cron.go               -- Cron scheduler
    heartbeat/heartbeat.go     -- HEARTBEAT.md unchecked-task checker
    mcp/client.go              -- stdio MCP client (handshake, tools/list, tools/call)
    mcp/manager.go             -- MCP server lifecycle; imports tools under a namespaced name
    integration/               -- Integration tests
    learning/learning.go       -- Learning/summary extraction
    log/logger.go              -- Structured logging (charmbracelet/log + lipgloss)
    memory/memory.go           -- MEMORY.md + HISTORY.md management
    providers/                 -- Provider interface + LiteLLM, Ollama, Multiprovider
    service/                   -- Systemd/launchd service install (cross-platform factory)
    session/                   -- JSON-persisted conversation sessions
    skills/skills.go           -- Skill discovery from SKILL.md files
    subagent/subagent.go       -- Restricted subagent runner
    tools/                     -- Tool interface + Registry + filesystem, shell, web, message, async
```

> **IMPORTANT**: All production code lives under `internal/`. A `pkg/` directory once held an abandoned parallel refactor of `bus` and `channels`; it was deleted in the 2026-08 audit sweep.

## Key Interfaces

```go
// internal/tools/tool.go
type Tool interface {
    Name() string
    Description() string
    Parameters() []Parameter
    Execute(ctx interface{}, args map[string]any) ToolResult
}

// internal/providers/provider.go
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
    Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error)
    Name() string
    Config() Config
}
```

### Imports

- **Group imports** by stdlib, third-party, then local (separated by blank lines)
- **Use import aliases** for clarity when needed (e.g., `ctxpkg "github.com/bigknoxy/joshbot/internal/context"`)
- **Avoid blank imports** except for drivers/side effects

### Error Handling

- **Return errors as values** - don't panic in library code
- **Wrap errors with context** using `fmt.Errorf("operation failed: %w", err)`
- **Tools return error strings** (`return fmt.Sprintf("Error: File not found: %s", path)`) - don't return errors that require handling
- **Graceful degradation** with fallbacks
- **Check errors explicitly** - don't ignore return values

### Naming Conventions

| Element          | Convention         | Example                          |
|------------------|--------------------|----------------------------------|
| Packages         | lowercase, single  | `tools`, `bus`, `config`         |
| Types            | PascalCase         | `Agent`, `WebFetchTool`          |
| Functions/methods| PascalCase (exported), camelCase (unexported) | `BuildSystemPrompt`, `parseResponse` |
| Private fields   | camelCase          | `cfg`, `running`                 |
| Constants        | PascalCase or UPPER_SNAKE_CASE | `MaxOutput`, `MAX_QUEUE_SIZE` |
| Interfaces       | PascalCase + "er" suffix | `Provider`, `ToolExecutor` |

### Data Modeling

- **Structs** for data types (InboundMessage, LLMResponse, Session, etc.)
- **Embed interfaces** for composition
- **Use struct tags** for JSON/field mapping (`json:"field_name"`)
- **Functional options pattern** for complex configuration

### Interfaces & Extension Points

- **Small, focused interfaces** (prefer 1-3 methods)
- **Interface segregation** - define interfaces where they're used
- **Registry pattern** for tools (`Registry`) and providers
- **Functional options** for flexible construction (`Option func(*Type)`)

### Concurrency Patterns

- **Goroutines** for concurrent operations
- **Channels** for message bus (`chan InboundMessage`, `chan OutboundMessage`)
- **sync.Mutex/sync.RWMutex** for shared state
- **sync.WaitGroup** for goroutine coordination
- **context.Context** for cancellation and timeouts
- **select** for multiplexing channel operations

### Logging

- **charmbracelet/log** for structured logging (`log.Info`, `log.Debug`, `log.Warn`, `log.Error`)
- `log.Debug()` for routine operations, LLM request/response details, tool execution results
- `log.Info()` for significant events (tool execution, service start/stop)
- `log.Warn()` for recoverable issues, empty content detection
- `log.Error()` for failures

**Debug Mode:** Use `--debug` flag to enable DebugLevel logging:
```bash
joshbot agent --debug
joshbot gateway --debug
```

Debug logging provides visibility into:
- LLM response details: content_length, content_preview, tool_calls_count, finish_reason
- HTTP response status codes and model information
- Tool execution results with result_length and preview
- Empty content warnings with model and iteration info for troubleshooting

### String Formatting

- **fmt.Sprintf** for formatted strings
- **String concatenation** with `+` for simple cases
- **strings.Builder** for building complex strings efficiently

### Documentation

- **Package comments** at top of file (starts with "Package X ...")
- **Exported types/functions** must have doc comments
- **Example functions** for usage documentation (`ExampleTool_Execute`)

## Architecture Quick Reference

```
channels/ --> bus/MessageBus --> agent/Agent --> providers/LiteLLMProvider
(CLI,         (chan-based)      (ReAct loop)    (HTTP -> LLM API)
 Telegram,                          |
 Discord)
                               tools/Registry (filesystem, shell,
                                                 web, message)
                                      |
                               skills/SkillDetector
                                   → Extractor → Loader.Create
                                   (auto-skill-creation pipeline)
```

- **Message bus** decouples channels from agent via `InboundMessage`/`OutboundMessage` channels
- **ReAct loop**: LLM -> tool calls -> reflect -> repeat (max 20 iterations)
- **Memory**: `MEMORY.md` (always in context) + `HISTORY.md` (grep-searchable event log)
- **Skills**: Markdown files with YAML frontmatter, progressive loading (summary -> full content)
- **Sessions**: JSONL files in `~/.joshbot/sessions/`
- **Config**: `~/.joshbot/config.json`, JSON-validated, env vars with `JOSHBOT_` prefix
- **Prompt caching**: Static system prompt cached with mtime-based invalidation (`internal/agent/context.go`)
- **Model-centric config**: Provider auto-detected from model prefix, fallback chains supported (`internal/config/config.go`)

## Key Files

| File | Purpose |
|------|---------|
| `cmd/joshbot/main.go` | CLI entry point, service wiring, onboard flow |
| `internal/agent/agent.go` | Core ReAct agent loop, message processing |
| `internal/agent/context.go` | System prompt assembly with caching |
| `internal/memory/memory.go` | MEMORY.md + HISTORY.md management |
| `internal/skills/skills.go` | Skill discovery and progressive loading |
| `internal/tools/tool.go` | Tool interface (implement this to add new tools) |
| `internal/tools/registry.go` | Tool registration and execution |
| `internal/tools/shell.go` | Shell exec; `isDenied` delegates screening to `shell_deny.go` |
| `internal/tools/shell_deny.go` | Command screening: quote-aware segmentation, wrapper stripping, structural rules. Defence in depth, not a security boundary |
| `internal/channels/telegram.go` | Telegram channel implementation |
| `internal/channels/discord.go` | Discord channel implementation (discordgo gateway) |
| `internal/mcp/` | stdio MCP client and server manager |
| `internal/config/config.go` | All configuration structs, model-centric config, provider detection |
| `internal/bus/bus.go` | Channel-based message bus |
| `internal/providers/provider.go` | Provider interface, Config struct (includes ExtraBody), types |
| `internal/providers/litellm.go` | OpenAI-compatible HTTP provider (supports ExtraBody via marshalBody) |
| `internal/providers/registry.go` | Provider registration (poolside, groq, nvidia, etc.) |
| `internal/context/context.go` | Context compaction, compression, budget management |
| `internal/skills/skills.go` | Skill discovery, progressive loading |
| `internal/skills/detection.go` | Skill auto-detection from tool usage patterns |

## Adding New Components

- **New tool**: Create in `internal/tools/`, implement `Tool` interface, register via `tools.RegistryWithDefaults()`
- **New channel**: Create in `internal/channels/`, implement channel logic, register in `main.go` gateway cmd
- **New skill**: Create `workspace/skills/{name}/SKILL.md` with YAML frontmatter (auto-discovered)

## Panel Review (agent-assisted code review)

A repo-scoped review workflow that runs five expert perspectives over a change,
has them debate, and produces a scored verdict. Useful before merging anything
that touches tool execution, config, or public surface.

**Files** (none of this is joshbot runtime code — it configures coding agents):

| Path | Role |
|---|---|
| `.claude/skills/panel-review/SKILL.md` | The workflow: frame → analyse → debate → score → report |
| `.claude/skills/panel-review/references/experts.md` | **Canonical charters for all five experts** |
| `.claude/skills/panel-review/references/scoring.md` | Rubric, default weights, report template |
| `.claude/agents/panel-*.md` | Claude Code agent-type wrappers that point at `experts.md` |

**The five experts**: `panel-agent-security` (prompt injection, tool abuse, trust
boundaries), `panel-llm-evals` (is the behaviour verifiable, would a silent
regression be caught), `panel-agent-experience` (onboarding, config, over-blocking,
failure legibility), `panel-oss-growth` (adoption, positioning, doc drift),
`panel-go-systems` (goroutine lifecycle, loop termination, build tags, coverage).

**Running it from any harness.** `references/experts.md` is the single source of
truth and deliberately does not depend on Claude Code's agent registry, so the
panel is portable:

1. Read `SKILL.md` for the workflow and `references/experts.md` for the charters.
2. Write the framing block (subject, artifacts, decision, constraints) once and
   give the identical block to every expert.
3. Spawn five subagents **concurrently**, each given only the framing block plus
   its own `## panel-<name>` section from `experts.md`. Keeping them isolated in
   this round is the point — experts who see each other's drafts converge early
   and the debate round stops being useful. If your harness has no subagents, work
   the five charters sequentially yourself, writing each verdict down before
   reading the next charter.
4. Give every expert the other four reports and require each to challenge at least
   one finding and to concede or hold on challenges against it.
5. Score with `references/scoring.md` (weighted composite, 0–10 per expert, high is
   good, `N/A` allowed) and report using its template. Any blocking concern
   overrides the composite.

**Claude Code shortcut**: invoke the `panel-review` skill, or spawn
`subagent_type: panel-agent-security` and so on. The agent registry is read at
session start, so newly added `.claude/agents/*.md` need a new session before they
resolve — fall back to a general-purpose agent pointed at `experts.md`.

**Scaling**: one or two experts for a focused single-lane question, all five for a
normal change, five plus a second debate round for direction-setting decisions.
Say which experts you skipped and why.

Charters carry joshbot-specific grounding (known weak points, file paths, the
OpenClaw CVE threat model). Verify those claims against the code before citing
them — the repo moves faster than the charter, and a finding built on a stale
charter is a false finding.

## Config

Config at `~/.joshbot/config.json`. Env var overrides with `JOSHBOT_` prefix (e.g., `JOSHBOT_PROVIDERS__OPENROUTER__API_KEY`). Model-name-based provider routing: `claude` → Anthropic, `gpt` → OpenAI, `gemini` → Gemini, `poolside` → Poolside. Default fallback: OpenRouter.

**Legacy Provider Format:**

**Legacy Provider Format:**
```json
{
  "agents": { "defaults": { "workspace": "~/.joshbot/workspace", "model": "openrouter/anthropic/claude-sonnet-4-20250514", "max_tool_iterations": 20 } },
  "providers": { "openrouter": { "api_key": "" }, "anthropic": {}, ... },
  "channels": { "telegram": { "enabled": false, "token": "", "allow_from": [] }, "discord": { "enabled": false, "token": "", "allow_from": [] } }
}
```

**Model-Centric Format (Recommended):**
```json
{
  "models_config": {
    "models": [
      { "name": "smart", "model": "anthropic/claude-sonnet-4", "api_key": "sk-ant-..." },
      { "name": "fast", "model": "groq/llama-3.3-70b-versatile", "api_key": "gsk_..." },
      { "name": "code", "model": "poolside/laguna-m.1", "api_key": "ps-...", "extra_body": { "chat_template_kwargs": { "enable_thinking": false } } }
    ],
    "agent": { "model": "smart", "fallback": ["fast"] }
  },
  "channels": {
    "telegram": { "enabled": false, "token": "", "allow_from": [] }
  }
}
```

**Registered Providers:** `openrouter`, `openai`, `nvidia`, `groq`, `ollama`, `anthropic`, `poolside`, `azure`, `custom`, `litellm`

**ExtraBody support:** Some providers (e.g., poolside) require extra JSON body fields. Use `extra_body` in model config or `ProviderConfig`:
```json
{ "extra_body": { "chat_template_kwargs": { "enable_thinking": false } } }
```
This is merged into the chat completion request body via `marshalBody()` in `internal/providers/litellm.go`.

## Gotchas

- **Telegram message limit**: Telegram Bot API enforces 4096 char max per message (not retryable on failure). `Send` splits longer content via `splitMessage` in `internal/channels/telegram.go`, closing and reopening markdown code fences across the boundary. Parts may exceed `maxLen` by up to 4 bytes because of the reopened fence. `splitMessage` indexes by byte, not rune.
- **Telegram chat actions expire after 5 seconds**: one `sendChatAction` is not enough for an agent turn that runs 30-90s. `startTyping`/`stopTyping` in `internal/channels/telegram.go` keep one keep-alive goroutine per chat (keyed by `Recipient()` under `mu`), re-sending every `typingInterval` (4s). `Send` stops the keep-alive once a recipient is resolved, and every goroutine also selects on `stopCh`, so shutdown reaps them. Do not go back to a one-shot send.
- **Telegram command menu comes from `botCommands`**: the `botCommands` slice in `internal/channels/telegram.go` is both what `SetCommands` publishes and what the unknown-command fallback treats as known. telebot routes a registered command to its own handler and only falls through to `OnText` when nothing claimed it, so `handleMessage` sees unknown commands only. Adding a `bot.Handle("/x", ...)` without adding it to `botCommands` still leaves `/x` invisible in the UI and absent from the fallback's "available commands" list. Keep the two in step. Commands whose behaviour lives in the agent (`/status`, `/model`, `/personality`, `/compact`) are registered through the `forwardedCommands` slice: `setupHandlers` registers a shared `handleCommandForward` for each, which re-checks the allowlist (telebot dispatches known commands outside `handleMessage`, so the gate must be applied there too) and forwards the raw `msg.Text` to the bus with `is_command: true` metadata. `TestTelegramChannel_CommandMenuAndHandlersInStep` enforces the menu⇄handler parity.
- **Telegram token validation retries transport failures and must never leak the token**: `ValidateToken` in `internal/channels/telegram.go` is the network boundary onboard uses. Three rules keep it safe. A malformed token is rejected **offline** by `validateTokenFormat` (a `^\d+:[A-Za-z0-9_-]{30,}$` shape check) before any request — a garbage-token unit test must not need network. Transport failures (dial, TLS handshake timeout, timeout, connection reset while reading the body) are retried up to 3 times and wrapped with the exported sentinel `ErrTelegramNetwork`; a definite API rejection (400/401/404) is never retried. And the transport error must stay unwrapped to its cause: `http.Client.Do` returns a `*url.Error` whose string form is `Get "https://api.telegram.org/bot<token>/getMe": ...`, which prints the credential into setup output — the reported failure was exactly this, a valid token plus one TLS hiccup printing the full token to the terminal and aborting setup. The `io.ReadAll` of the body is *also* a network boundary — a reset or timeout that lands after the headers arrived must be wrapped in `ErrTelegramNetwork` too, or a mid-body drop is reported as a rejected token and never retried. On the wizard side (`setupTelegram` in `cmd/joshbot/main.go`), validation runs through the stub-able `validateTelegramToken` package var, re-prompts once on failure, and on persistent failure **keeps an existing working token** rather than returning nil — returning nil made `runOnboard` save the config with Telegram disabled and silently disconnected a live bot. The same return-nil hazard exists when the user aborts a token *change* with `cancel`/empty input: with an existing token present that must also return the existing configuration, not nil. `onboard --force` is genuinely non-interactive now: it reuses the existing API key without reading stdin, so do not reintroduce a prompt in the force path.

The same keep-existing hazard covers the **API key prompt**, not just the token: `promptProviderAPIKey` in `cmd/joshbot/main.go` shows "Enter new API key (or press Enter to keep current)" when a key exists, and pressing Enter must return that existing key. An earlier version returned `""` on Enter, and `runOnboard`'s `if apiKey != ""` gate then skipped the whole provider-config block, so a keep-current reconfigure of a working NVIDIA install saved a config with only the disabled `openrouter` default and `joshbot agent` died with "no providers enabled: 1 provider(s) found in config (openrouter)". Empty input with an existing key is "keep it", not "drop the provider" — same contract as the Telegram token. (`TestRunOnboard_Interactive_KeepCurrentAPIKey` pins it.)
- **CLI stdin blocking**: `bufio.NewReader(os.Stdin).ReadString('\n')` blocks on stdin and can't be interrupted by context cancellation
- **Session key format**: `"channel:senderID"` (e.g., `"cli:cli-user"`, `"telegram:johndoe"`). Computed from `Channel:SenderID` — no explicit SessionKey field
- **Per-session model and personality live in the `.meta.json` sidecar, and `/model --global` mutates the config in place**: `Session.ModelOverride` and `Session.Personality` are persisted via the meta sidecar (`internal/session/manager.go`), not the JSONL message stream — they ride alongside `ConversationTopic`/`ConversationContext`. Resolution order is session override, then the runtime global override (the `a.modelName` field, guarded by `a.modelMu sync.RWMutex` because it is read on every model resolution), then the config default. `handleModelCommand`/`handlePersonalityCommand` save the session; `/model <name> --global` sets `a.modelName` under the write lock and calls `config.Save(a.cfg)`. Do not read `a.modelName` or `a.cfg.Agents.Defaults.Model` without holding the read lock — a concurrent `/model --global` writes `a.cfg` under the write lock. `/new` clears both overrides but preserves `ConversationContext`. The meta sidecar is **removed, not rewritten**, when every meta field is empty — a stale sidecar would re-inject a cleared override or personality on the next Load (pinned by `TestMetaSidecarRemovedWhenCleared`).
- **`/model --global` writes the whole config while holding `modelMu`, and the cfg-reading helpers come in locked and unlocked variants**: `setGlobalModelAndPersist` must hold the write lock across `config.Save`'s marshal (a concurrent `/model` list on another session reads the same fields), and `modelList`/`resolveModelSpec`/`modelForSession` must use the `...Locked` variants when they already hold the read lock — `sync.RWMutex` is not reentrant, so calling the plain `getModelName()` from inside a locked `modelList` deadlocks. `TestRace_ModelListVsGlobal` (throwaway, removed) proved the write path raced the marshal before this fix; keep the lock across the whole `Save`.
- **The interactive CLI editor must never be offered unless stdin is a real `*os.File` terminal**: `runAgentLoop` in `cmd/joshbot/main.go` activates the editor only when `input.(*os.File)` succeeds, `isatty.IsTerminal(fd)` is true **and** `isTTY(output)` is true; otherwise it keeps the buffered `> ` prompt path. `main_test.go`'s runAgentLoop tests pass `bytes.Buffer`/`blockingReader` as input (never `*os.File`), so they exercise the buffered path and must keep passing untouched. The editor owns exactly **one** reader goroutine for its whole lifetime (`startReader`/`close` in `editor.go`) — a per-`ReadLine` goroutine would outlive its call, stay blocked in the next terminal read, and swallow the first bytes of the following line; `TestEditor_HistoryNavigation` would catch a regression here. `editor.close()` (via `defer`) stops the goroutine, which may still be blocked in `ReadKey` until a byte arrives or the descriptor closes — the same acceptance as the buffered reader goroutine in `runAgentLoop`.
- **The editor path must NOT also spawn the `bufio` reader goroutine**: both read the same fd 0, and a terminal delivers each byte to exactly one blocked reader, so the two would nondeterministically steal keystrokes from each other. The `lines` goroutine is therefore created only when `editor == nil` (inside the `else` path of the TTY check) — keep it that way. The editor's `ReadLine` selects on `ctx.Done()` for shutdown, so it needs no separate `done`/`lines` plumbing.

- **Session writes go through `writeFileAtomic`, and `Load` is deliberately lenient**: `internal/session/manager.go` writes the JSONL, its `.meta.json` sidecar and any quarantine file at `0600` (they hold full conversation content and tool output — treat them like the credential store). The temp file name must be unique per writer; `os.CreateTemp` provides that. The old `<path>.tmp` was a fixed name shared by every process using `~/.joshbot/sessions`, so a running `gateway` and a concurrent `agent -m` wrote into the same temp file and the surviving rename published a torn mix — the `sync.RWMutex` never crossed process boundaries. On the read side, an unparseable line is **skipped, not fatal**: the parseable messages load, the original bytes are copied to `<session-id>.jsonl.corrupt`, and one warning is logged. Do not restore the hard error. There is exactly one session per `channel:senderID` and no CLI to delete one, so a fatal parse means that Telegram user gets an error on every subsequent message with no recovery short of `rm`.
- **A compaction record is stored in the session and must stay singular**: once the history crosses `agents.defaults.compaction_threshold`, `checkAndCompactContext` summarizes it and `applyCompaction` replaces the summarized prefix with one `session.Message` carrying `Compaction: true`, always at index 0. Three rules keep it correct. The write-back happens **after** `reactLoop` returns, not inside it, because `Process` slices `sess.Messages[startSessionLen:]` for the history log and shrinking the session first would slice out of range. `buildMessages` holds the record out of the memory-window truncation — letting the window slide over it silently discards the whole earlier conversation. And the replaced messages are appended to `<session-id>.history.jsonl` via the optional `session.Archiver` interface before they leave the live session; if that append fails the compaction is abandoned, since recomputing a summary is cheaper than destroying history. Note `checkAndCompactContext` runs only **after tool execution** inside the ReAct loop, so a turn that answers without calling a tool never compacts at all — a test scripted without tool calls exercises nothing.
- **Everything joshbot prints or logs is redacted; session files are not**: `internal/redact` wraps the log writer (`internal/log/logger.go`) and `joshbot status`, so a credential appearing in a tool result never reaches the log. Session JSONL is deliberately exempt — rewriting content on save would mangle legitimate text and add a second corruption path, so data at rest relies on the `0600` mode instead; `TestSessionContentIsStoredVerbatim` pins that boundary, and `internal/redact` documents it. Three traps if you extend the patterns. **The scheme is kept and the token is redacted, never the reverse**: an `Authorization` value is `<scheme> <credential>`, and an earlier version recognised only `Bearer|Basic`, so GitHub's own `Authorization: Token <secret>` fell through to the assignment rule, which blanked the word `Token` and published the credential after it. Both the header rule and the assignment rule now capture an optional leading scheme word and preserve it. **A name fragment must end the identifier**, apart from a plural `s` and separator-led segments (`SECRET_KEY_ID`): matching it as a bare substring rewrote `Author:`, `unauthorized:` and `secretariat:`, all routine tool output. That is also why `AUTH` is in `redact.SecretNameFragments` — which widens the shell-environment screen in `internal/tools`, sharing the list on purpose — but is excluded from the assignment rule by `assignmentFragments()`. **And the cheap gate in front of each regex must never be narrower than the regex itself** — gating on the underscored `api_key` spelling silently let `api-key: <secret>` through. All-numeric values are exempt on purpose — `Max tokens: 8192` in `joshbot status` matches TOKEN plus a plural `s`, and no detected key class is bare digits. The assignment gate is a hand-written walk (`hasAssignmentCandidate`) rather than a substring test, because Go's regexp costs ~650ms per megabyte on a case-insensitive alternation and `token`/`key`/`secret` appear constantly in ordinary prose without being assignments; the walk mirrors the regex's name part exactly, so widening one means widening the other.
- **`joshbot sessions` is not a resume feature, and `.jsonl` alone does not identify a session**: sessions are keyed `channel:senderID` and loaded on every inbound message, so there is one per user per channel and nothing to select; the subcommands exist to inspect and clear. Two traps live here. `internal/session` now owns sidecars that also end in `.jsonl` — `.history.jsonl` (compaction archive) and `.jsonl.archive-<ns>` (reset) — so anything enumerating the sessions directory must go through `isSessionFile`, never a bare `strings.HasSuffix(name, ".jsonl")`; the compaction archive was reported as a phantom session named `<id>.history` until that was centralised. And urfave/cli v2 **stops flag parsing at the first positional argument**, so a flag written after an id is silently dropped — `prune <id> --force` declined without deleting, and `show <id> --last 2` printed the whole transcript. `parseSessionArgs` in `cmd/joshbot/sessions_cmd.go` reads those trailing flags — all three of them, including `--older-than`, whose omission made `prune <id> --older-than 30d` report an unknown flag; any new subcommand taking both an argument and a flag needs the same treatment. A third trap: an `Info` for a session that would not scan must carry the file's real mtime, because `PruneOlderThan` compares `UpdatedAt` and a zero time reads as infinitely old — a transient read error was enough to make `--older-than` delete a session of any age. Session IDs are attacker-influenced (the sender half comes from Telegram), so `ValidateSessionID` gates every path-building entry point.
- **OutboundMessage**: Uses `ChannelID` (not `ChatID`) for routing responses back to the correct chat
- **Workspace identity files**: `IDENTITY.md`, `SOUL.md`, `USER.md`, `AGENTS.md`, `TOOLS.md` loaded into system prompt via XML tags
- **Service cross-platform**: `internal/service/` uses build tags (`factory_linux.go`, `factory_darwin.go`, `factory_other.go`) — each must export the same function signature. Also has `systemd.go`, `launchd.go`, `openrc.go`, `unsupported.go`
- **Cron schedules are durations, not cron expressions**: despite the package name, `internal/cron` understands only `delay:<duration>` (fire once) and `every:<duration>` (repeat). The `cron` tool accepts `30m`, `2h`, `1d`, `1h30m` and rejects `0 9 * * *` with an error naming the accepted format. There is no calendar scheduling, so "every weekday at 9am" cannot be expressed — say so rather than approximating it silently with `every:24h`. `AddJob` validates the schedule and refuses anything it cannot run, so a bad job fails at creation instead of being stored and never firing.
- **A one-shot cron job's countdown survives the process**: `AddJob` converts a `delay:` duration into an absolute `Job.DueAt` (`json:"due_at"`, always present; a zero value means none recorded) and persists it, so a restart waits out the *remaining* time, not a fresh full duration. A job that came due while joshbot was stopped fires after `overdueGrace` (50ms) rather than being dropped. Jobs written before `due_at` existed have none: `loadLocked` backfills them as due one duration from load — the old behaviour, chosen so an old `jobs.json` does not fire everything at once — and saves the backfill so the deadline does not slide on each reload. `every:` jobs leave `DueAt` zero and keep plain `time.Ticker` semantics with no drift correction. One-shot jobs retire themselves once they fire (`retireJob`) — without that they replay on every boot forever. Recurring jobs run until deleted.
- **The `cron` tool is registered only when a scheduler exists**: `tools.WithCronService(svc, defaultChannel)` gates it, so the agent is never offered a tool whose jobs nothing would deliver. This is the wiring step whose absence was issue #90 — the skill taught a `cron` command that no tool implemented.
- **`go run` is detected by the go-build cache, never by `/tmp`**: `runningFromGoRun` in `cmd/joshbot/main.go` is the single source for this. Three call sites — `update`, `uninstall` and `detectRunningContext` — each carried their own `strings.Contains(exePath, "/tmp/")`, which is not a property of `go run` at all: it made a joshbot installed anywhere under `/tmp` permanently unable to update or uninstall itself, and reported the cause as `go run`. A guard like this fixed in one place is not fixed; grep for the other copies.
- **The bundled skills are embedded in the binary, not read from disk**: `internal/skills/bundled/` is pulled in with `//go:embed` (`internal/skills/bundled.go`). It used to be the repo-root `skills/` directory found via the *relative* path `filepath.Join("skills")`, which resolves against the process working directory — so the bundled set loaded only when joshbot was run from a checkout of its own source tree, and every real installation reported "No skills found" while `trust.go` claimed they "arrive with the binary". Consequences to keep in mind: a bundled skill's `Path` is now an embed path, not a filesystem path, so its content is cached at discovery and `Delete` refuses it; `Loader.bundledDir` is empty in production and exists only so tests can substitute a fixture directory; and adding a skill means adding a directory under `internal/skills/bundled/`, which requires a rebuild to take effect.
- **A skill naming a tool that does not exist fails only at runtime**: nothing at build time connects `internal/skills/bundled/*/SKILL.md` to the registry, which is how a cron skill survived for months with no cron tool. `TestBundledSkillsOnlyReferenceRegisteredTools` (`internal/tools/skill_tool_drift_test.go`) closes that loop by matching the phrase ``the `x` tool`` in bundled skills against every tool name in the package. Keep that phrasing when a skill introduces a tool, or the lint will not see it. "the `gh` CLI tool" deliberately does not match — `gh` is an external binary.
- **`StripProviderPrefix` must not touch poolside model IDs**: joshbot routes on a model prefix (`groq/…` → Groq) and strips it before sending, because for most providers the prefix is joshbot's own routing hint. Poolside is the exception — its published IDs *are* `poolside/laguna-s-2.1`, so stripping produces a name the API rejects with `404 {"error":"please check the model you provided"}`. `prefixesPartOfModelID` in `internal/config/config.go` holds that exception; add to it when onboarding any provider whose `/v1/models` listing includes the prefix in the `id`. Check the provider's real listing before assuming — this bug shipped for months and made poolside unusable, including its own registered default `poolside/laguna-m.1`. Both the streaming and non-streaming call sites go through this one function, so fixing it there covers both.
- **The interactive CLI is not a `Channel`**: `runAgentLoop` in `cmd/joshbot/main.go` drives it directly, with a `> ` prompt. `internal/channels` contains only the `Channel` interface and `TelegramChannel`. A dead `cli.go` implementing `Channel` used to sit there with no callers; it was deleted after it misled a diagnosis of #104 into the wrong file. Do not reintroduce a CLI channel unless you also wire it up.
- **Never leave a blocking read inside a `select` with `default:`**: that pattern made `joshbot agent` unkillable (issue #104) — shutdown was checked only between reads, so a signal arriving at the prompt was never seen. Read on a goroutine and select over input, `done` and `ctx.Done()` together. Related: `signal.Notify` **disables Go's default termination** for every signal it registers, so a handler that consumes one signal and returns leaves the process deaf to SIGINT/SIGTERM forever. `setupGracefulShutdown` now loops and exits immediately on a second signal; keep it that way.
- **A Telegram parse-entity rejection is silent data loss, not a normal error**: sending with a parse mode set returns `400 ... can't parse entities` whenever the text contains malformed Markdown/HTML, which LLM output produces routinely (a stray `_`, an unclosed backtick, a bare `<tag>`). `isRetryable` has no case for it, so before the fallback existed the reply was abandoned and the user saw nothing at all. `Send` now retries each part once with `ParseMode` cleared; `isParseEntityError` matches Telegram's specific description substrings, case-insensitively. Do **not** widen it to match bare `400` — that would silently downgrade `chat not found` and similar real failures to unformatted sends and hide them.
- **Telegram allowlist is deny-by-default**: `TelegramChannel.IsAllowed` returns `false` on an empty `allowSet`, not `true` — a shell-capable bot must fail closed. `Start` logs a loud, actionable warning naming `channels.telegram.allow_from` when the allowlist is empty. Do not "restore" the old allow-all-when-empty convenience; it was the one-click-RCE shape flagged in #140. Tests that exercise message handling on an empty allowlist must pass an allowlisted sender (`newTestTelegramChannel("<id-or-username>")`).
- **Discord has its own retry classifier and its own stop-channel lifecycle**: `isDiscordRetryable` (`internal/channels/discord.go`) is deliberately separate from Telegram's `isRetryable` — Discord reports permanent failures as `discordgo.RESTError` codes (50007 cannot send to this user, 50001 missing access, 10003 unknown channel, 10013 unknown user, 50013 missing permissions), which Telegram's string matching would retry through the full backoff inside the single `consumeOutbound` goroutine, and its unclassified-error log line names the wrong channel. 429 retries, any other 4xx does not. Separately, `stopCh` belongs to one Start/Stop cycle: `Start` allocates a fresh one and `Stop` closes it under `mu` behind a `stopClosed` latch. Reusing a single channel made a restarted channel deliver nothing (its `consumeOutbound` returned at once, every `Send` aborted its retries) and panicked the process on the second `Stop`. Read it through `stopChan()`, never the field, or the reallocation is a data race.

- **`isRetryable` treats Telegram 403s as permanent**: `bot was blocked`, `user is deactivated`, `message to reply`, and `chat not found` all return `false` — retrying them just burns backoff inside the single `consumeOutbound` goroutine and stalls every queued message behind the doomed send. Unknown errors still default to retry but log at Debug so an unclassified error surfaces. This is orthogonal to `isParseEntityError`, which drives the plain-text fallback in `Send` — keep both.
- **Web tool refuses non-public addresses at dial time**: `NewWebTool` installs `guardedDialControl` on its transport (`internal/tools/web.go`), so the client rejects any connection to loopback, RFC1918, link-local or other non-public addresses regardless of which code path issued the request. Two consequences: an `httptest` server (which listens on `127.0.0.1`) cannot be reached through `WebTool.httpClient`, so test HTTP paths by extracting the decision into a function and testing that directly; and `WebToolConfig.SearchAPI` cannot point at a LAN or localhost search engine. Do not weaken the guard to make a test pass — it is the enforcement point that survives DNS rebinding, and `validateURLForSSRF` alone does not.
- **`--config` names a file, and the home follows it**: `config.LoadFrom(path)` loads exactly that path and anchors `DefaultHome` to its directory, so sessions, media, cron, the skills trust store and `Save` all agree with the config that was loaded. Resolve the config file through `config.ConfigPath()`, never `filepath.Join(DefaultHome, "config.json")` — that ignores an explicitly chosen file name. CLI commands must read config via `loadConfig(c.Path("config"))`, not `config.Load()`, or the flag silently does not reach them. A missing path is an error, never a fallback to defaults.
- **Workspace skills require operator approval**: a `SKILL.md` under `<workspace>/skills/` becomes part of the agent's instructions — its description always, and with `always: true` its whole body on every request, permanently. So workspace skills are inert until approved via `joshbot skills trust <name>`. Approval lives in `~/.joshbot/skills.trust`, **outside the workspace**, so a command confined to the workspace cannot approve skills for itself, and binds to a SHA-256 of the **entire skill directory tree** (every regular file's relative path + content, walked in sorted order), so editing, adding, or removing any file in the directory — not just `SKILL.md`, but a sibling script it tells the agent to run — revokes approval. A legacy digest from an older joshbot that hashed only `SKILL.md` no longer matches and safely revokes (re-inspect and re-trust) rather than crashing. `Loader.Create` (the agent's `skill_registry` tool) writes but never approves — if writing implied trust the gate would be decorative. Bundled skills ship with the binary and are exempt. Do not "helpfully" auto-approve on first run.
- **Shell sandbox is opt-in, fails closed, and is now per-platform**: `tools.shell_sandbox` (`"off"` default, `"workspace"` to enable) confines commands — filesystem plus outbound TCP denial, `$HOME` and the shared temp unreachable, private scratch at `<workspace>/.joshbot-tmp` with `TMPDIR` pointed at it. Enforcement mechanism differs by build target and each lives in its own `sandbox_<os>.go` behind `SandboxAvailable()`/`SandboxSupported()`/`newSandboxCommand()`: **Linux** = Landlock, applied in the `__sandbox-exec` re-exec helper (Landlock is irreversible and inherits to children, so it must never be applied in joshbot's own process); **macOS** = Seatbelt, the command wrapped in `/usr/bin/sandbox-exec` with a deny-by-default SBPL profile built from the same `DefaultSandboxPolicy` (no re-exec helper — `ApplySandbox` is unused on darwin; paths are symlink-resolved because Seatbelt matches real paths and `/tmp`→`/private/tmp`); **other** = no OS sandbox, so enabling it is a startup error. An unknown value or a host that cannot enforce is a startup error, never a silent unconfined run — do not "fix" that by falling back to off. On platforms with **no** sandbox available (`!SandboxAvailable()`, e.g. Windows/BSD) the shell tool **defaults to allowlist-only** (`defaultUnsandboxedAllowlist` in `shell.go`) when no `tools.shell_allow_list` is set, because the deny list alone is bypassable; Linux/macOS keep the unrestricted default. Test containment by asserting the kernel's refusal (`sandbox_linux_test.go`, `sandbox_darwin_test.go`), never by checking command text.
- **Only one chat channel may be enabled at a time.** `bus.OutboundChannel()` returns one shared `chan OutboundMessage` and every channel implementation's `consumeOutbound` reads it competitively, so Telegram and Discord running together each receive roughly half the other's replies — no error, no log line, just answers arriving in the wrong service. Documented in `internal/channels/discord.go`, README's Discord Setup and `site/architecture.html`. The fix is per-channel fan-out in the bus; do not "fix" it in a channel.
- **The shell allowlist screens the whole command, not the first word, and refuses chaining.** `checkAllowList` (`internal/tools/shell.go`) rejects any command containing `;`, `&`, `|`, a newline, a carriage return, a backtick, `$(`, `<(`, `>(`, `$'`, `>` or `<` while an allowlist is in force, because the command reaches `sh -c` unchanged and `echo hi; id` passed a first-word match. **Redirection belongs in that list and was missing**: `echo PWNED > /outside/escaped.txt` passed both the allowlist and the deny list and the shell wrote the file. `>` and `<` are matched as substrings, which covers `>>`, `2>`, `&>`, `<<` and `<>` without enumerating them. It does not exempt quoted occurrences on purpose — a boundary that depends on a quoting parser matching the shell's exactly is not a boundary, and `ls | wc -l` being refused costs a caller one extra call. `defaultUnsandboxedAllowlist` also no longer contains `find`, `go`, `git`, `rg` or `sort`: each launches a program of the caller's choosing (`find -exec sh -c`, `go run`, `git -c core.pager=...`, `rg --pre PROGRAM` once per file, `sort --compress-program=PROGRAM` on spill). Screening flag values instead was rejected — it has to be exhaustive to hold. The criterion for anything added to that list is "cannot, by itself, execute a second program of the caller's choosing"; `TestDefaultUnsandboxedAllowlistHasNoExecutors` pins it by asserting each known executor is both absent from the list and unreachable at runtime. Both `Execute` and `ExecuteAsync` go through the one helper — adding a third entry point means calling it too.
- **`EvalSymlinks` cannot tell a dangling symlink from a missing file**, so `resolveSymlinks` (`internal/tools/path_guard.go`) must resolve every re-appended component, not just the nearest existing ancestor: `ws/evil -> /outside/x` with no `/outside/x` returned the lexical path, passed `isWithinBase`, and was then written. Resolution narrows but does not close the TOCTOU window either — a component can still be swapped between the check and the open — and `O_NOFOLLOW` was not enough on its own, because it constrains **only the last component** while the kernel still follows every intermediate one. Replacing `ws/sub` with a symlink to `/outside` after the check put the write at `/outside/file.txt`, and read paths used bare `os.ReadFile` with no `O_NOFOLLOW` at all. **Every filesystem-tool path now opens through the one helper in `internal/tools/openat.go`** — `openInRoot` / `mkdirAllIn` / `safeReadFile` / `safeWriteFile` / `readDirIn`, built on Go 1.24's `os.Root`, which holds the containment root as a descriptor and resolves each component with `openat(2)`. `writeNoFollow` is gone. Three things to keep in mind. `os.Root.MkdirAll` and `ReadDir` are Go 1.25 and this module targets 1.24, so `mkdirAllIn` walks the levels itself — and it must `Lstat` before treating `EEXIST` as benign, since `Mkdir` on a name that is already a symlink also returns `EEXIST`. `relInRoot` absolutizes both sides, because `filepath.Rel` cannot relate a relative path (`NewFilesystemTool(".", …)`) to an absolute root. And `containmentRoot` (`filesystem.go`) picks the root — workspace, matching `allowedPaths` entry, else the workspace base, i.e. fail-closed — so a new operation must call it rather than pass a path straight to `os`.
- **An allowlist entry's *shape* decides which field it may match**, on Telegram as on Discord: an all-digits entry matches only the numeric user ID, a non-numeric entry only the username/display name. Matching every entry against every field is an authentication bypass — display names are attacker-chosen and unvalidated, so a stranger could set theirs to the operator's numeric ID.
- **`mach-lookup` in the macOS Seatbelt profile is an allowlist (`macMachServices`), not a blanket grant** — an unrestricted `(allow mach-lookup)` reaches XPC services running outside the profile that will act on the sandboxed process's behalf, which hollows out the file and network rules. Adding a service name is a security decision and gets justified in place. Two related traps in `sandbox_darwin.go`: `GOCACHE` is usually unset on macOS (the toolchain defaults it to `~/Library/Caches/go-build`, not under `~/.cache`), so it is resolved via `go env GOCACHE` and granted explicitly or `go build` fails under the sandbox; and a path that does not exist yet is silently dropped from the profile by `existingPaths`, so `newSandboxCommand` creates the workspace and its scratch dir up front rather than rendering a profile with no write grant. Note the profile is passed with `-p`, so it (and the workspace path) is visible in `ps` to any local user.
- **A bus or channel object that can be restarted must not keep a per-run channel in a per-object field.** `MessageBus.Start` hands each goroutine the context it created rather than letting them read `mb.ctx`, which a later `Start` reassigns while the previous run's un-joined goroutines are still reading it; `DiscordChannel` allocates a fresh `stopCh` per `Start` and latches the close, because a channel-lifetime `stopCh` stayed closed after the first `Stop` (making `consumeOutbound` return instantly and every `Send` abort its retries) and a second `Stop` panicked the process. `mcp.Client` has the same shape: `doneCh` belongs to the connected process, is snapshotted by `call`, and is closed by the `readLoop` that was handed it — a per-client channel left every call after a failed handshake reporting "server stopped" forever.
- **The heartbeat's publish pattern and its check-off pattern must be the same regex.** They were two, and every line they disagreed on (`-[ ] task`, `* [ ]task`) published on every tick forever. `internal/heartbeat` now has exactly one `uncheckedRE` and a `parseTask` that returns both the task text and the rewritten line. A task is checked off only after `bus.Send` accepts it — flipping the box on a dropped send loses the task silently — and `HEARTBEAT.md` is rewritten with a `writeFileAtomic` using a uniquely-named temp file, since the read-modify-write spans a tick and a user may be editing the file inside it.
- **MCP servers are wired into `cmd/joshbot` now.** `setupComponents` calls `registerMCPServers`, and every long-lived entry point defers `closeMCPServers` to reap the child processes; the manager is a package var because exactly one setup runs per invocation. Startup is fail-soft by design — a server that will not start is logged and skipped, never a startup abort. Docs claiming MCP is "not yet wired" are stale.
- **Guard a discipline you cannot reach behaviourally at the source level, and prove the detector fires.** `runGateway`'s bus handler is an anonymous closure that only a full gateway stand-up would exercise, so `TestGatewayHandlerHasNoDirectConsoleWrites` (`cmd/joshbot/audit_sweep_test.go`) parses `main.go` with `go/parser` and fails on any `os.Stdout`/`os.Stderr` write inside it — a direct write bypasses both the redacting log writer and the configured level. The version before it called `joshlog.Debug` directly, which asserted the logging library's level filtering and would have stayed green with the leak put right back. A companion test runs the same detector over a fixture containing the regression, so the guard cannot silently stop detecting anything.
- **`SandboxMode` zero value is `""`, and `""` means off**: `SandboxMode` is a string, so a bare-constructed `ShellTool` (via `NewShellTool*` without `SetSandbox`/`NewShellToolFromConfig`) leaves `sandbox` empty, not `SandboxOff`. Empty must be treated as off, or the tool takes the sandbox path and `sandboxPreflight` refuses on any host without Landlock (macOS, where releases are cut) — which silently reddened the shell/security regression tests (#138). Both the fast-path check in `buildExecCmd` and `sandboxPreflight` normalize `"" → off`, `SetSandbox` maps `""` to `SandboxOff`, and the constructor initializes the field. Keep all four: `""` behaving as anything but off is a regression. CI runs a `macos-latest` leg (build + test, no coverage gate) so a non-Linux-only break is caught.
- **Channel allowlist entries are matched by shape, not against every field**: `internal/channels/discord.go` partitions `allow_from` at construction — an all-digits entry lands in `allowIDs` and matches only the numeric user ID, anything else lands in `allowNames` and matches only username / global display name. Matching every entry against every field was an auth bypass: global display names are free-form and not unique, so a stranger could set theirs to the operator's snowflake and be admitted. **Telegram still has the flat-set shape** (`IsAllowed` in `internal/channels/telegram.go`), where a numeric entry can be matched by a name built from `firstName`/`lastName`; it is pre-existing and should get the same partitioning.
- **`HashSkillFile` covers symlinks by name and raw target, never dereferenced**: reading through a link would let a skill dir hash whatever it points at, so the digest folds in the link name plus `os.Readlink`'s raw target. Adding, repointing or removing a symlink inside an approved skill therefore revokes trust like any other file change.
- **Spawned commands get an allowlisted environment**: `runCommand` assigns `execCmd.Env = sanitizedEnv()`. The allowlist and the credential screen live in **`internal/childenv`** (`childenv.Sanitized` / `childenv.IsSecretName` / `allowlist` / `allowPrefixes`), not in `internal/tools` — MCP server processes need the identical screen and `internal/tools` imports `internal/mcp`, so keeping it here would mean duplicating it there. `internal/tools/shell_env.go` is now thin aliases; `cmd.Env` is never left nil on either path. A nil `Env` inherits this process's, which hands every provider API key to any command the model runs — readable with a bare `env`, so no sandbox and no deny-list rule would catch it. If a command legitimately needs a variable, add it to `childenv`'s `allowlist`/`allowPrefixes`; do not switch the mechanism to a deny-list, because a miss there is a leaked credential. Note the allowlist is screened again by `isSecretEnvName`, so a credential-shaped name cannot ride in on a broad prefix rule.
- **`restrict_to_workspace` does not confine what a command touches**: it only validates the `working_dir` *parameter* (`internal/tools/shell.go:120-130`, `340-350`). A command body is still free to read anything the process can — `cat ~/.ssh/id_rsa` works with `restrict` on. Treat the flag as "where the command starts", not "what it can reach". Real confinement needs an OS boundary (issue #75).
- **SSRF checks belong on the resolved address, never the hostname**: an attacker controls their own DNS, so a name carries no signal about where it points. `validateURLForSSRF` resolves every hostname and fails closed when the lookup fails. Use `isBlockedIP`, not `isPrivateIP`, for "is this safe to reach" — `isPrivateIP` covers only RFC1918-style ranges and excludes link-local, which is where every cloud metadata endpoint lives.
- **`agent.Process` reports LLM failures in band, so every non-interactive caller must translate them back**: it returns `"Error processing request: ..."` as reply *text* with a nil error, because a chat channel has to show the user something. `agent -m` exited 0 over a completely unreachable provider until `agentReplyError` (`cmd/joshbot/main.go`) turned that prefix back into an error — text mode exits 1, JSON mode sets `"is_error": true` on the result document and writes a `{"type":"error",...}` document to stderr. Any new non-interactive entry point calling `Process` needs the same translation; treating the string as a normal answer is the bug.
- **`providers.ListModels` refuses an empty `APIBase` instead of defaulting to OpenRouter**: the old fallback to `https://openrouter.ai/api/v1` meant credential validation for *any* other or unknown provider dialled openrouter.ai and printed "✓ validated". It now errors, which is what lets `configure.ValidateProviderCredentials` report "could not verify ... no API base URL configured" for the providers with no fixed endpoint (azure, custom, litellm). Do not reintroduce the default.
- **Agent progress callback is carried per-request via the context, not on the Agent struct**: `agent.WithSink(ctx, fn)` attaches a `ProgressFunc` to the `context.Context` passed to `Process`. The ReAct loop in `internal/agent/agent.go` reads it via `progressFromContext(ctx)` — nil means zero behavior change for callers that don't opt in (Telegram, non-interactive `agent -m`, tests). `cmd/joshbot/main.go`'s `runAgentLoop` attaches the sink only when `isTTY(output)` is true, to drive the interactive CLI's tool-call lines (`⏺ tool(args)` / `⎿ ok (1.2s)`) and "thinking..." spinner. Because the sink lives on the context, concurrent `Process` calls on the same `Agent` never cross-deliver events (issue #115).
- **Non-TTY detection lives in `cmd/joshbot/main.go` as the `isTTY` package var**, not a plain function — this is deliberate so tests can override it (`isTTY = func(io.Writer) bool { return true }`, restored via `t.Cleanup`) instead of depending on whether the test process happens to have a real terminal attached. Production detection type-asserts the `io.Writer` to `*os.File` and calls `github.com/mattn/go-isatty` (already an indirect dependency, promoted to direct — pure Go, no cgo); anything that isn't a `*os.File` (a `bytes.Buffer` in tests, a pipe) is correctly treated as non-TTY without special-casing. Both the tool-call progress lines (`⏺ tool(args)` / `⎿ ok (1.2s)`) and the "thinking..." spinner are gated on this — never print `\r` or ANSI clear codes when it's false, or piped/scripted `joshbot agent` output (and `scripts/verify-local.sh`) breaks.
- **The spinner goroutine's lifetime is exactly one `startSpinner`/`stopSpinner` pair** (`cliProgress` in `cmd/joshbot/main.go`), scoped around a single blocking `agentInstance.Process(ctx, msg)` call inside `runAgentLoop`'s per-message loop — not around the whole loop. `stopSpinner` closes a cancel channel and then blocks on a "done" channel the goroutine closes on exit, so it cannot return before the goroutine has actually stopped; there is no separate leak-detection mechanism, that join is the whole guarantee. Both the spinner and the tool-progress printer (`onToolEvent`) share one `sync.Mutex` on `cliProgress`, because the spinner ticks from its own goroutine while `onToolEvent` is invoked synchronously from inside `Process` on the caller's goroutine — without the shared lock their writes to `output` interleave mid-line.

- **MCP servers are started during component setup**: `internal/mcp` is a stdio JSON-RPC 2.0 client (newline-delimited messages, not LSP Content-Length framing) with lazy start and clean process reaping. `tools.RegisterMCPTools(ctx, reg, cfg.MCP)` connects enabled servers, enumerates `tools/list`, and registers each as `mcp__<server>__<tool>` (namespacing is a security control — it makes shadowing `shell`/`filesystem` impossible, since built-ins never carry the prefix and the registry refuses duplicate names). It returns a `*mcp.Manager` whose `Close()` MUST be called at shutdown to reap the child processes. Three bounds keep a server from taking the process with it: a result is truncated at `mcpMaxOutputChars` (4,000), a single message read is capped at `maxMessageBytes` (4 MiB) by `readLine` — `bufio.Reader.ReadBytes` grows without limit, so an unbounded reply was a heap exhaustion — and the child gets `childenv.Sanitized` rather than a nil `cmd.Env`, so it never inherits provider API keys. The import direction is `tools → mcp` and `tools → config`; `internal/mcp` must never import `internal/tools` or it creates a cycle. Config lives at `mcp.servers.<name>` (`Command`/`Args`/`Env`/`Enabled`), and `config.json`'s out-of-workspace location is the trust boundary (see SECURITY.md). `setupComponents` in `cmd/joshbot/main.go` calls `registerMCPServers` right after `RegistryWithDefaults`; the manager is held in a package var and reaped by `closeMCPServers`, which `runAgent` and `runGateway` defer. Startup is fail-soft — a server that will not start or list tools is logged and skipped, never a startup abort. Two client invariants: the read-loop done channel is **per process**, allocated in `Connect` and closed by the readLoop it was handed (a per-Client channel left a failed handshake permanently "stopped" even after a later successful Connect), and `dispatch` deletes the pending entry before replying and sends non-blocking, so a server answering the same id twice cannot wedge the read loop.

## Provider Enabled Flag

Providers require `"enabled": true` in config to activate. This is a required boolean field:

```json
{
  "providers": {
    "nvidia": {
      "api_key": "nvapi-...",
      "enabled": true
    }
  }
}
```

Environment variables also set `enabled: true` automatically when a provider is configured via env var.

## Env Var Format

All config values can be overridden with `JOSHBOT_` prefix env vars using `__` as path separator:

```bash
# Canonical format (use provider routing key):
export JOSHBOT_PROVIDERS__OPENROUTER__API_KEY="sk-or-..."
export JOSHBOT_PROVIDERS__NVIDIA__API_KEY="nvapi-..."

# Model-centric format:
export JOSHBOT_MODELS_CONFIG__AGENT__MODEL="smart"
export JOSHBOT_MODELS_CONFIG__MODELS__0__NAME="fast"
export JOSHBOT_MODELS_CONFIG__MODELS__0__MODEL="groq/llama-3.3-70b-versatile"
export JOSHBOT_MODELS_CONFIG__MODELS__0__API_KEY="gsk_..."
```

Shorthand forms like `JOSHBOT_OPENROUTER_API_KEY` and `JOSHBOT_NVIDIA_API_KEY` are also accepted for backward compatibility.

## Website (site/)

Two pages live in `site/`:
- `index.html` — Landing page: explains what joshbot is, how it works, use cases, install
- `architecture.html` — Official design docs: system architecture diagram, data flow, code map, key patterns, component details, config

**Both pages MUST be updated when:**
- New major features or tools are added
- Architecture changes (new packages, new patterns)
- Configuration format or provider support changes
- Use cases or capabilities significantly expand

The header nav on both pages must be kept in sync (same links, same active state logic).

## HARD RULE: documentation ships with the change

**A change that alters observable behaviour updates the docs in the same PR.** Not a
follow-up, not an issue. A change is not done until the docs match. See the table in
`CLAUDE.md` for which file covers what.

Non-negotiable parts:

- **Verify before writing.** Read the source — or run the command — for every documented
  command, flag, config key or behaviour. Config keys must match the `json:`/`mapstructure:`
  struct tags exactly.
- **No unverifiable numbers.** LOC, test counts, tool counts, binary sizes: measure them
  when you write them, or leave them out. A stale number reads as authoritative.
- **Removing a stale claim matters as much as adding a true one.** Drift runs both ways.
- **`site/index.html` and `site/architecture.html` are in scope.** They drift fastest
  because nothing fails when they are wrong.

This rule exists because the softer version did not work: `CLAUDE.md` already said the
site MUST stay in sync while the site sat eight releases out of date.

## Pre-Release Checklist

```bash
go build ./cmd/joshbot
gofmt -d .                     # MUST return empty (no formatting diffs)
rm -rf ~/.joshbot
go test -race ./...
./joshbot agent -m "hello"    # Verify response
./joshbot status               # Verify config
```

Documentation gate — a release does not go out with any of these unchecked:

- [ ] `README.md`, `docs/INSTALL.md` — commands, flags, config keys, examples current
- [ ] `site/index.html`, `site/architecture.html` — capabilities, architecture, counts
- [ ] `SECURITY.md` — matches what is actually enforced
- [ ] `CLAUDE.md`, `AGENTS.md` — gotchas cover anything that would surprise an agent
- [ ] `internal/skills/bundled/*/SKILL.md` — no instruction the tools no longer permit
- [ ] `./scripts/test-install.sh` passes — not in CI (it downloads real artifacts), so it only runs if you run it
- [ ] `CHANGELOG.md` — entry under `[Unreleased]`
- [ ] Every quoted count or size re-measured, not carried over

## Release Process
1. Push changes to main first
2. **WAIT** for CI to pass (green checkmark on the commit) — do not push the tag until CI is green
3. Only then cut the release tag with `git tag vX.Y.Z && git push origin vX.Y.Z`
4. Monitor both the CI workflow and the Release workflow until all jobs are green
