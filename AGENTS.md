# AGENTS.md - Coding Agent Guidelines for joshbot

## Project Overview

joshbot is a lightweight personal AI assistant (~19,640 LOC Go non-test, 597 test functions across 48 test files) with self-learning memory, auto-skill-creation from tool usage patterns, and Telegram integration. Architecture: goroutine-based message bus decoupling chat channels from a ReAct agent loop backed by multi-provider LLM via OpenRouter-compatible APIs.

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
cmd/joshbot/main.go            -- CLI entry (urfave/cli/v2), service wiring, ~3,704 LOC
  internal/
    agent/agent.go             -- ReAct loop (max 20 iterations)
    agent/context.go           -- System prompt assembly (identity files + memory + skills)
    bus/bus.go                 -- Channel-based message bus (Inbound/OutboundMessage in bus.go)
    channels/channel.go        -- Channel interface (Telegram is the only implementation)
    channels/telegram.go       -- Telegram long-polling channel (telebot)
    config/config.go           -- JSON config, env overrides (JOSHBOT_ prefix)
    configure/configure.go     -- Config wizard, provider selection, non-interactive configure
    context/context.go         -- Context propagation, registry, budget manager
    copilot/auth.go            -- GitHub Copilot auth flow
    cron/cron.go               -- Cron scheduler
    heartbeat/heartbeat.go     -- HEARTBEAT.md unchecked-task checker
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
  pkg/                         -- Incomplete refactor from internal/ (only bus + channels exist here)
    bus/                       -- Duplicates internal/bus
    channels/                  -- Duplicates internal/channels
```

> **IMPORTANT**: Edit `internal/` not `pkg/`. The `pkg/` directory is an incomplete parallel refactor. All production code imports from `internal/`.

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
 Telegram)                          |
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
  "channels": { "telegram": { "enabled": false, "token": "", "allow_from": [] } }
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
- **Telegram command menu comes from `botCommands`**: the `botCommands` slice in `internal/channels/telegram.go` is both what `SetCommands` publishes and what the unknown-command fallback treats as known. telebot routes a registered command to its own handler and only falls through to `OnText` when nothing claimed it, so `handleMessage` sees unknown commands only. Adding a `bot.Handle("/x", ...)` without adding it to `botCommands` still leaves `/x` invisible in the UI and absent from the fallback's "available commands" list. Keep the two in step.
- **CLI stdin blocking**: `bufio.NewReader(os.Stdin).ReadString('\n')` blocks on stdin and can't be interrupted by context cancellation
- **Session key format**: `"channel:senderID"` (e.g., `"cli:cli-user"`, `"telegram:johndoe"`). Computed from `Channel:SenderID` — no explicit SessionKey field
- **Session writes go through `writeFileAtomic`, and `Load` is deliberately lenient**: `internal/session/manager.go` writes the JSONL, its `.meta.json` sidecar and any quarantine file at `0600` (they hold full conversation content and tool output — treat them like the credential store). The temp file name must be unique per writer; `os.CreateTemp` provides that. The old `<path>.tmp` was a fixed name shared by every process using `~/.joshbot/sessions`, so a running `gateway` and a concurrent `agent -m` wrote into the same temp file and the surviving rename published a torn mix — the `sync.RWMutex` never crossed process boundaries. On the read side, an unparseable line is **skipped, not fatal**: the parseable messages load, the original bytes are copied to `<session-id>.jsonl.corrupt`, and one warning is logged. Do not restore the hard error. There is exactly one session per `channel:senderID` and no CLI to delete one, so a fatal parse means that Telegram user gets an error on every subsequent message with no recovery short of `rm`.
- **OutboundMessage**: Uses `ChannelID` (not `ChatID`) for routing responses back to the correct chat
- **Workspace identity files**: `IDENTITY.md`, `SOUL.md`, `USER.md`, `AGENTS.md`, `TOOLS.md` loaded into system prompt via XML tags
- **Service cross-platform**: `internal/service/` uses build tags (`factory_linux.go`, `factory_darwin.go`, `factory_other.go`) — each must export the same function signature. Also has `systemd.go`, `launchd.go`, `openrc.go`, `unsupported.go`
- **Cron schedules are durations, not cron expressions**: despite the package name, `internal/cron` understands only `delay:<duration>` (fire once) and `every:<duration>` (repeat). The `cron` tool accepts `30m`, `2h`, `1d`, `1h30m` and rejects `0 9 * * *` with an error naming the accepted format. There is no calendar scheduling, so "every weekday at 9am" cannot be expressed — say so rather than approximating it silently with `every:24h`. `AddJob` validates the schedule and refuses anything it cannot run, so a bad job fails at creation instead of being stored and never firing.
- **A one-shot cron job's countdown survives the process**: `AddJob` converts a `delay:` duration into an absolute `Job.DueAt` (`json:"due_at"`, always present; a zero value means none recorded) and persists it, so a restart waits out the *remaining* time, not a fresh full duration. A job that came due while joshbot was stopped fires after `overdueGrace` (50ms) rather than being dropped. Jobs written before `due_at` existed have none: `loadLocked` backfills them as due one duration from load — the old behaviour, chosen so an old `jobs.json` does not fire everything at once — and saves the backfill so the deadline does not slide on each reload. `every:` jobs leave `DueAt` zero and keep plain `time.Ticker` semantics with no drift correction. One-shot jobs retire themselves once they fire (`retireJob`) — without that they replay on every boot forever. Recurring jobs run until deleted.
- **The `cron` tool is registered only when a scheduler exists**: `tools.WithCronService(svc, defaultChannel)` gates it, so the agent is never offered a tool whose jobs nothing would deliver. This is the wiring step whose absence was issue #90 — the skill taught a `cron` command that no tool implemented.
- **A skill naming a tool that does not exist fails only at runtime**: nothing at build time connects `skills/*/SKILL.md` to the registry, which is how a cron skill survived for months with no cron tool. `TestBundledSkillsOnlyReferenceRegisteredTools` (`internal/tools/skill_tool_drift_test.go`) closes that loop by matching the phrase ``the `x` tool`` in bundled skills against every tool name in the package. Keep that phrasing when a skill introduces a tool, or the lint will not see it. "the `gh` CLI tool" deliberately does not match — `gh` is an external binary.
- **`StripProviderPrefix` must not touch poolside model IDs**: joshbot routes on a model prefix (`groq/…` → Groq) and strips it before sending, because for most providers the prefix is joshbot's own routing hint. Poolside is the exception — its published IDs *are* `poolside/laguna-s-2.1`, so stripping produces a name the API rejects with `404 {"error":"please check the model you provided"}`. `prefixesPartOfModelID` in `internal/config/config.go` holds that exception; add to it when onboarding any provider whose `/v1/models` listing includes the prefix in the `id`. Check the provider's real listing before assuming — this bug shipped for months and made poolside unusable, including its own registered default `poolside/laguna-m.1`. Both the streaming and non-streaming call sites go through this one function, so fixing it there covers both.
- **The interactive CLI is not a `Channel`**: `runAgentLoop` in `cmd/joshbot/main.go` drives it directly, with a `> ` prompt. `internal/channels` contains only the `Channel` interface and `TelegramChannel`. A dead `cli.go` implementing `Channel` used to sit there with no callers; it was deleted after it misled a diagnosis of #104 into the wrong file. Do not reintroduce a CLI channel unless you also wire it up.
- **Never leave a blocking read inside a `select` with `default:`**: that pattern made `joshbot agent` unkillable (issue #104) — shutdown was checked only between reads, so a signal arriving at the prompt was never seen. Read on a goroutine and select over input, `done` and `ctx.Done()` together. Related: `signal.Notify` **disables Go's default termination** for every signal it registers, so a handler that consumes one signal and returns leaves the process deaf to SIGINT/SIGTERM forever. `setupGracefulShutdown` now loops and exits immediately on a second signal; keep it that way.
- **A Telegram parse-entity rejection is silent data loss, not a normal error**: sending with a parse mode set returns `400 ... can't parse entities` whenever the text contains malformed Markdown/HTML, which LLM output produces routinely (a stray `_`, an unclosed backtick, a bare `<tag>`). `isRetryable` has no case for it, so before the fallback existed the reply was abandoned and the user saw nothing at all. `Send` now retries each part once with `ParseMode` cleared; `isParseEntityError` matches Telegram's specific description substrings, case-insensitively. Do **not** widen it to match bare `400` — that would silently downgrade `chat not found` and similar real failures to unformatted sends and hide them.
- **`pkg/` is stale**: `pkg/` duplicates `internal/bus` and `internal/channels` — do not edit unless purposely finishing the refactor
- **Web tool refuses non-public addresses at dial time**: `NewWebTool` installs `guardedDialControl` on its transport (`internal/tools/web.go`), so the client rejects any connection to loopback, RFC1918, link-local or other non-public addresses regardless of which code path issued the request. Two consequences: an `httptest` server (which listens on `127.0.0.1`) cannot be reached through `WebTool.httpClient`, so test HTTP paths by extracting the decision into a function and testing that directly; and `WebToolConfig.SearchAPI` cannot point at a LAN or localhost search engine. Do not weaken the guard to make a test pass — it is the enforcement point that survives DNS rebinding, and `validateURLForSSRF` alone does not.
- **`--config` names a file, and the home follows it**: `config.LoadFrom(path)` loads exactly that path and anchors `DefaultHome` to its directory, so sessions, media, cron, the skills trust store and `Save` all agree with the config that was loaded. Resolve the config file through `config.ConfigPath()`, never `filepath.Join(DefaultHome, "config.json")` — that ignores an explicitly chosen file name. CLI commands must read config via `loadConfig(c.Path("config"))`, not `config.Load()`, or the flag silently does not reach them. A missing path is an error, never a fallback to defaults.
- **Workspace skills require operator approval**: a `SKILL.md` under `<workspace>/skills/` becomes part of the agent's instructions — its description always, and with `always: true` its whole body on every request, permanently. So workspace skills are inert until approved via `joshbot skills trust <name>`. Approval lives in `~/.joshbot/skills.trust`, **outside the workspace**, so a command confined to the workspace cannot approve skills for itself, and binds to a SHA-256 of the file, so editing an approved skill revokes it. `Loader.Create` (the agent's `skill_registry` tool) writes but never approves — if writing implied trust the gate would be decorative. Bundled skills ship with the binary and are exempt. Do not "helpfully" auto-approve on first run.
- **Shell sandbox is opt-in and fails closed**: `tools.shell_sandbox` (`"off"` default, `"workspace"` to enable) confines commands with Landlock — filesystem plus outbound TCP denial. Linux only. An unknown value, a non-Linux platform, or a kernel without Landlock all produce a startup error rather than an unconfined run; do not "fix" that by falling back to off, because an operator who thinks containment is on and is wrong is worse off than one who never enabled it. Landlock is irreversible and inherits to children, so it is applied in the `__sandbox-exec` re-exec helper and must never be applied in joshbot's own process. The default policy grants neither `$HOME` nor the shared `/tmp` — commands get a private scratch dir at `<workspace>/.joshbot-tmp` with `TMPDIR` pointed at it. Test containment by asserting the kernel's refusal (`internal/tools/sandbox_linux_test.go`), never by checking command text.
- **Spawned commands get an allowlisted environment**: `runCommand` assigns `execCmd.Env = sanitizedEnv()` (`internal/tools/shell_env.go`). A nil `Env` inherits this process's, which hands every provider API key to any command the model runs — readable with a bare `env`, so no sandbox and no deny-list rule would catch it. If a command legitimately needs a variable, add it to `shellEnvAllowlist`; do not switch the mechanism to a deny-list, because a miss there is a leaked credential. Note the allowlist is screened again by `isSecretEnvName`, so a credential-shaped name cannot ride in on a broad prefix rule.
- **`restrict_to_workspace` does not confine what a command touches**: it only validates the `working_dir` *parameter* (`internal/tools/shell.go:120-130`, `340-350`). A command body is still free to read anything the process can — `cat ~/.ssh/id_rsa` works with `restrict` on. Treat the flag as "where the command starts", not "what it can reach". Real confinement needs an OS boundary (issue #75).
- **SSRF checks belong on the resolved address, never the hostname**: an attacker controls their own DNS, so a name carries no signal about where it points. `validateURLForSSRF` resolves every hostname and fails closed when the lookup fails. Use `isBlockedIP`, not `isPrivateIP`, for "is this safe to reach" — `isPrivateIP` covers only RFC1918-style ranges and excludes link-local, which is where every cloud metadata endpoint lives.
- **Agent progress callback is carried per-request via the context, not on the Agent struct**: `agent.WithSink(ctx, fn)` attaches a `ProgressFunc` to the `context.Context` passed to `Process`. The ReAct loop in `internal/agent/agent.go` reads it via `progressFromContext(ctx)` — nil means zero behavior change for callers that don't opt in (Telegram, non-interactive `agent -m`, tests). `cmd/joshbot/main.go`'s `runAgentLoop` attaches the sink only when `isTTY(output)` is true, to drive the interactive CLI's tool-call lines (`⏺ tool(args)` / `⎿ ok (1.2s)`) and "thinking..." spinner. Because the sink lives on the context, concurrent `Process` calls on the same `Agent` never cross-deliver events (issue #115).
- **Non-TTY detection lives in `cmd/joshbot/main.go` as the `isTTY` package var**, not a plain function — this is deliberate so tests can override it (`isTTY = func(io.Writer) bool { return true }`, restored via `t.Cleanup`) instead of depending on whether the test process happens to have a real terminal attached. Production detection type-asserts the `io.Writer` to `*os.File` and calls `github.com/mattn/go-isatty` (already an indirect dependency, promoted to direct — pure Go, no cgo); anything that isn't a `*os.File` (a `bytes.Buffer` in tests, a pipe) is correctly treated as non-TTY without special-casing. Both the tool-call progress lines (`⏺ tool(args)` / `⎿ ok (1.2s)`) and the "thinking..." spinner are gated on this — never print `\r` or ANSI clear codes when it's false, or piped/scripted `joshbot agent` output (and `scripts/verify-local.sh`) breaks.
- **The spinner goroutine's lifetime is exactly one `startSpinner`/`stopSpinner` pair** (`cliProgress` in `cmd/joshbot/main.go`), scoped around a single blocking `agentInstance.Process(ctx, msg)` call inside `runAgentLoop`'s per-message loop — not around the whole loop. `stopSpinner` closes a cancel channel and then blocks on a "done" channel the goroutine closes on exit, so it cannot return before the goroutine has actually stopped; there is no separate leak-detection mechanism, that join is the whole guarantee. Both the spinner and the tool-progress printer (`onToolEvent`) share one `sync.Mutex` on `cliProgress`, because the spinner ticks from its own goroutine while `onToolEvent` is invoked synchronously from inside `Process` on the caller's goroutine — without the shared lock their writes to `output` interleave mid-line.

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
- [ ] `skills/*/SKILL.md` — no instruction the tools no longer permit
- [ ] `CHANGELOG.md` — entry under `[Unreleased]`
- [ ] Every quoted count or size re-measured, not carried over

## Release Process
1. Push changes to main first
2. **WAIT** for CI to pass (green checkmark on the commit) — do not push the tag until CI is green
3. Only then cut the release tag with `git tag vX.Y.Z && git push origin vX.Y.Z`
4. Monitor both the CI workflow and the Release workflow until all jobs are green
