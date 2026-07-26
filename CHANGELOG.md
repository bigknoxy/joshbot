# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- **Two abandoned copies of the install and uninstall scripts** — `scripts/install.sh` and `scripts/uninstall.sh` were forks frozen before the v-prefixed asset-name fix landed in the repo-root copies. The `scripts/` installer tried three naming patterns, none of which matched what `release.yml` actually publishes, and downgraded a checksum mismatch to a warning it continued past; the `scripts/` uninstaller predated systemd/launchd/pipx removal. Nothing referenced either file except a `.goreleaser.yaml` glob, and the advertised `curl | bash` path was never affected — but both were what a reader looking in `scripts/` would find first. Deleted, the glob repointed, and the naming contract between `install.sh` and `.github/workflows/release.yml` is now pinned by tests so the two cannot drift silently again.

## [1.39.1] - 2026-07-25

### Fixed
- **`--config` ignored the file name it was given** — Only the directory was used, so `--config /path/staging.json` loaded `/path/config.json`, and three configs in one directory all resolved to the same file. A path with no `config.json` beside it silently fell back to defaults, leaving the impression the file had been read. The home override was also reverted immediately after loading, so sessions, media, cron, the skills trust store and `Save` still pointed at `~/.joshbot`: joshbot read one file and wrote another. `configure` and `onboard` ignored the flag entirely — `configure --config x.json --api-key ...` reported success while writing the key to `~/.joshbot/config.json`.

## [1.39.0] - 2026-07-25

### Security
- **Workspace skills could install permanent instructions without review** — A `SKILL.md` under the workspace was discovered and injected into the system prompt: its description always, and with `always: true` its entire body inline on every request, surviving restarts. Nothing checked its origin, and `always` was never validated. Because the agent's own `skill_registry` tool writes skills, text steered by a fetched page, a chat message or a document could induce the agent to write standing instructions it would then follow indefinitely; anything else able to write to the workspace — a cloned repo, an extracted archive — had the same effect without involving the model. Workspace skills are now inert until approved with `joshbot skills trust`, approval is recorded outside the workspace and bound to a hash of the file so edits revoke it, and creating a skill never approves it.

### Fixed
- **New files under `cmd/joshbot/` were silently untracked** — The `.gitignore` entry for the built `joshbot` binary was unanchored, so it also matched the `cmd/joshbot` directory and `git add` skipped anything new there. Anchored to the repo root; this recovered 492 lines of existing tests that had never been committed.

## [1.38.0] - 2026-07-25

### Added
- **OS-level sandbox for shell commands** (`tools.shell_sandbox`) — Opt-in containment via Landlock on Linux: filesystem access is confined to the workspace and build caches, and outbound TCP is denied unless `tools.shell_sandbox_allow_network` is set. Neither `$HOME` nor the shared `/tmp` is granted, so SSH keys, cloud credentials and joshbot's own config are unreachable. This is the boundary the deny list in `shell_deny.go` cannot be: screening has to anticipate every dangerous command an attacker might write, while the kernel refuses the access however it was spelled. Off by default because enabling it changes what existing setups can do. It fails closed — an unknown mode, a platform without an implementation, or a kernel without Landlock all produce a startup error rather than an unconfined run.

## [1.37.0] - 2026-07-25

### Security
- **Provider API keys were handed to every shell command** — `exec.Cmd` with a nil `Env` inherits the parent's environment, and `runCommand` never assigned one, so any command the model ran received joshbot's full environment including `JOSHBOT_PROVIDERS__*__API_KEY`. A bare `env` was enough to read them: no filesystem access, no deny-list rule involved, and no filesystem sandbox would have helped. Spawned commands now get an allowlisted environment, screened a second time for credential-shaped names.
- **`config.json` was written world-readable (0644)** — It holds live provider API keys, so every account on the machine could read them, which also made any containment of the shell tool beside the point. Now written 0600, along with the migration backup that copies it verbatim.

### Fixed
- **Every Telegram message was written to stderr in plaintext** — Two debug statements left in since May printed each sender ID and full message text directly to stderr, bypassing the log-level system entirely, so a personal assistant's whole conversation landed in the journal regardless of `log_level`.
- **Stopping the Telegram channel did not stop it** — `Stop()` called telebot's `Close()` (the Bot API session-teardown RPC) instead of `Stop()`, so the long-poller goroutine never exited while the channel logged "Telegram channel stopped". Harmless only because the single call site sits immediately before process exit; anything restarting the channel in-process would have produced two concurrent `getUpdates` pollers, which Telegram rejects.
- **`joshbot status` reported disabled providers as configured** — A provider without `"enabled": true` is silently skipped, but `status` listed it anyway, so the one command you would run to diagnose the problem confirmed the broken config looked fine. Startup now fails with a message naming the actual cause — the missing `enabled` flag or a missing `api_key`, whichever it is — and the README's legacy example no longer omits `enabled`.
- **Memory consolidation stored whatever the model returned** — A refusal, meta-commentary or a wall of prose became a permanent fact in `MEMORY.md`, which is loaded into every subsequent turn's context. Output now passes a deterministic content gate before anything is persisted; a completion yielding no valid facts is logged and discarded.
- **Context compression could silently erase a conversation** — Past 50 messages, a provider returning a choice with empty content made `CompressMessages` return an empty string with a nil error, skipping the deterministic fallback. The assistant would forget everything before that point and carry on. Degenerate summaries are now rejected, and the provider call takes a caller-supplied context so cancellation propagates.

## [1.36.0] - 2026-07-25

### Security
- **SSRF protection in `web_fetch` could be bypassed by any attacker-controlled hostname** — The private-address check only resolved a hostname when the name itself contained a keyword such as `metadata` or `localhost`, so any other name reached any address it pointed at, including loopback, private networks and cloud metadata endpoints. Two further defects made the keyword list ineffective even for the names it covered: a failed DNS lookup counted as safe, and the private-range check excluded link-local, where `169.254.169.254` lives. The combination meant `http://metadata.google.internal/` was allowed despite being listed in the tool's own block list. Reachable in the default configuration — `web_fetch` has no enable flag — including via indirect prompt injection from a page the assistant was asked to read. Addresses are now checked after resolution and again at dial time, which also closes the DNS-rebinding window and covers the search-engine redirect path, which followed `Location` headers with no check at all.

## [1.35.1] - 2026-07-25

### Fixed
- **Long conversations were rejected by the LLM provider** — Once a conversation grew past the token budget, context reduction rebuilt older messages without their tool-call linkage, so joshbot sent `tool` messages carrying no `tool_call_id`. OpenAI-compatible endpoints reject that request with a 400, meaning long sessions failed while short ones worked. The memory window could orphan a tool result the same way by cutting between an assistant tool call and its result.

### Added
- **Behavioural eval harness for the ReAct loop** (`internal/agent/evalharness_test.go`) — Runs the real loop against a scripted provider that records every request, so tests assert the trajectory (what was sent, dispatched and persisted) rather than the reply text. Runs in CI with no network or API key. `assertProtocolInvariants` enforces provider wire-format rules on every scenario. Validated by mutation testing: 9 of 9 deliberately introduced regressions were caught. `internal/agent` coverage 63.5% → 75.3%.

## [1.35.0] - 2026-07-25

### Security
- **`golang.org/x/text` bumped v0.3.7 → v0.30.0 (CVE-2022-32149)** — Denial of service in language tag parsing. The package is reachable from this build.

### Fixed
- **Telegram could hang on long replies containing an unbalanced code fence** — A reply over the 4096-character limit whose ``` fences did not pair up made the message splitter loop forever, so the message was never delivered, no error was reported, and the Telegram sender stopped processing further outbound messages. Long code-heavy answers were the common trigger.
- **Dangerous shell commands could evade the safety deny list** — Padded whitespace (`rm -rf  /`), reordered or split flags (`rm -fr /`, `rm -r -f /`), quote splicing (`r"m" -rf /`), wrapper commands (`sudo`, `env`, `nohup`, `timeout`), command substitution (`$(...)`, backticks), and `$IFS` separators all bypassed the previous substring matcher.
- **Telegram `allow_from` ignored numeric user IDs** — `IsAllowed` matched only usernames and display names, so a user following the README (which documents `"allow_from": ["123456789"]`) was locked out of their own bot. Numeric IDs now match, and they are the only identifier a user cannot change.

### Changed
- **Shell command screening rewritten** — Commands are now split into segments at unquoted separators, unwrapped, and matched against structural rules rather than literal substrings. This also removes false rejections such as `echo shutdown` and `ls | sha256sum`. It remains defence in depth, not a security boundary: isolation still requires an OS-level sandbox.
- **`ShellToolConfig.DenyList` is now additive** — Custom patterns apply on top of the built-in rules instead of replacing them, so configuration can tighten screening but not weaken it.

### Added
- Test coverage for `internal/channels` (47.5% → 59.8%) and a regression suite for shell command screening.
- **Panel review workflow** — A repo-scoped five-expert review (security, evals, experience, growth, Go systems) that analyses, debates and scores a change. Charters live in `.claude/skills/panel-review/references/experts.md` and are harness-portable. See the Panel Review section of `AGENTS.md`.

## [1.34.0] - 2026-06-06

> 1.33.0 and 1.34.0 were tagged from a release branch that was not merged
> back to `main` until 2026-07-25. Their dates therefore predate 1.31.0 and
> 1.32.0, which were released from `main` in the meantime.

### Changed
- **Prompt optimization (GEPA workflow)** — Core identity prompt reduced 457→356 words (22%). Variant A selected: structural cleanup preserves all behavioral guardrails with better organization. Tool descriptions now include tool name prefix for better function-calling disambiguation. Eval harness (all 24/24 weighted checks pass).

## [1.33.0] - 2026-06-05

### Added
- **Prompt optimization** — Core identity prompt reduced 411→299 words (27%). All 22 tool descriptions trimmed ~30-40%. Eval harness at `internal/agent/prompt_eval_test.go`.
- **MEMORY.md size cap** — Configurable `max_memory_size` (default 4KB). Auto-trims oldest entries on overflow.
- **Skill.Always field wired up** — Skills with `always: true` now inject full content into system prompt (was dead code).

### Fixed
- **Model prefix bug** — `StripProviderPrefix()` now called in `Chat()` and `ChatStream()` so provider prefixes (e.g., `nvidia/`) are stripped before sending to API.
## [1.32.0] - 2026-07-24

### Added
- **ConfigureTool for in-chat config management** — New `configure` tool allows listing available models and settings, switching the active model, adjusting settings like temperature and max_tokens, and viewing the current active configuration — all from within a conversation.

### Fixed
- **Model-centric routing fix** — Fixed model routing when using the model-centric config format (`models_config`). Provider is now correctly auto-detected from the model prefix.
- **Identity rewrite** — Updated workspace identity files (`IDENTITY.md`, `SOUL.md`, `USER.md`) for improved system prompt coherence.
- **Conversation coherence improvements** — Enhanced context assembly and prompt optimization for more coherent multi-turn conversations.

## [1.31.0] - 2026-07-24

### Added
- **ConfigureTool** — In-chat configuration management tool with `list`, `get`, `set`, and `save` operations for models, temperature, max_tokens, workspace, and other settings.

### Fixed
- **Model-centric routing** — Fixed model routing logic for the model-centric config format.
- **gofmt formatting** — Fixed formatting in 3 files.

## [1.30.0] - 2026-06-03

### Fixed
- **`<conversation_summary>` still leaking with weaker models** — Renamed internal compression tag from `<conversation_summary>` to `<ctx_compress>`. The old name was too semantically rich: weaker models (e.g., step-3.5-flash) saw "conversation summary" in context and responded to it even with v1.28.1's system prompt removal + output sanitization. The new name is semantically inert — models have no reason to reference it. Dogfooded with 7.5K Kubernetes deep-dive: zero tag leakage.

## [1.29.0] - 2026-06-03

### Added
- **Use case transcripts** — 5 detailed conversation transcripts in `tasks/use-cases.md` covering: Research + Code Generation, Self-Learning + Persistent Memory, Multi-Step Content Pipeline, Bug Investigation + Fix, and Skill Creation. Each includes exact tool schemas, parameters, execution paths, and expected outputs.
- **Filesystem alias tests** — 9 test functions across 35 assertions covering all 6 filesystem aliases (read_file, write_file, edit_file, list_dir, glob, grep).
- **Isolated unit tests** — Direct tests for `parseTasksArg`, `parseStepsArg`, `applyTemplates` with edge cases (empty arrays, overlapping template names, single-quoted JSON, invalid inputs).

### Fixed
- **Subagent maxTokens=500 truncated generated code** — Increased to 4096 so chain execution steps (e.g., code generation of ~100+ line Go files) complete without truncation. Also updated subagent system prompt from "max 500 tokens" to "max 4096 tokens".
- **applyTemplates() variable name collision** — Sorts template variable names by length descending before iterating, preventing `{{a}}` from partially matching inside `{{ab}}`.

## [1.28.1] - 2026-06-03

### Fixed
- **System prompt backfire (pink elephant)** — Removed all mentions of `<conversation_summary>` from the system prompt. The LLM was being primed to think about the tag, causing hallucinations of seeing it in the conversation. Defense is now purely structural (proper XML closing tags + output sanitization).

## [1.28.0] - 2026-06-03

### Fixed
- **`<conversation_summary>` leaking into LLM responses** — Three-layer fix:
  1. Added proper `</conversation_summary>` closing tag (was missing, LLM treated as unclosed XML)
  2. Added system prompt instruction telling LLM to never reference or output internal context tags
  3. Added `sanitizeResponse()` output sanitizer that strips any `<conversation_summary>` tags from responses (defense-in-depth)

## [1.27.0] - 2026-06-02

### Added
- **`chain_execution` tool** — Sequential multi-step subagent execution. Each step's output feeds as context to the next. Supports template substitution (`{{name}}` → prior step output), initial context, graceful step failure (subsequent steps continue), and cancellation. 12 tests.
- **`subagent_config` tool** — CRUD for YAML-based agent profile configs stored in `~/.joshbot/agents/`. Operations: `list`, `get`, `save`, `discover`. Each config defines model, temperature, max_tokens, system_prompt, tools, skills, and tags. 26 tests.
- **Auto-discovery of agent configs** — `~/.joshbot/agents/` directory scanned at startup, `*.yaml`/`*.yml` files loaded as named agent profiles.

### Fixed
- **Single-quote JSON parsing** — `parseTasksArg` now falls back to replacing single quotes with double quotes, handling LLMs that serialize arrays as single-quoted JSON strings.

### Other
- **GitHub Pages deploy** — `site/` directory (landing page + architecture docs) deployed to GitHub Pages on every release. URL set as repo homepage. Pages deploy job added to release workflow.

## [1.26.0] - 2026-06-01

### Fixed
- **Log format string bug** — `defaultLogger` had mismatched format verbs in structured log calls, causing potential panics on certain log levels.

## [1.25.0] - 2026-06-01

### Added
- **Poolside provider** — New provider registration for `poolside/laguna-m.1` with `ExtraBody` support for `chat_template_kwargs`.
- **`onboard --force` stdin hang fix** — Zero stdin pipe detection prevents blocking on first-time setup.

## [1.24.0] - 2026-05-31

### Fixed
- **exa-cli search JSON parsing** — Handles pretty-printed multi-line JSON output from exa-cli search results.

## [1.23.0] - 2026-05-31

### Changed
- **`CompressMessages` refactored** — Extracted `lastNonEmptyContent` helper, returns error on all-empty content instead of silently wrapping empty string in `<conversation_summary>` tags.

## [1.22.0] - 2026-05-31

### Added
- **`parallel_subagent` tool** — Fan-out independent tasks to subagents with configurable concurrency, semaphore-bounded goroutine pool, per-task success/failure reporting. 11 unit + 9 integration tests.
- **Subagent runner** — `internal/subagent/` package wrapping single-turn LLM calls with configurable model, temperature, max tokens, and timeout.

## [1.21.0] - 2026-05-31

### Added
- **Auto-skill-creation pipeline** — `SkillDetector` → `Extractor` → `Loader.Create` fully wired in `main.go` (lines 600-616). Tool usage patterns generate SKILL.md files automatically.

## [1.20.2] - 2026-05-31

### Fixed
- **HTTP 410 error on LLM calls** — Default model `arcee-ai/trinity-large-preview:free` was removed from OpenRouter, causing all API calls to fail with `API error (410)`. Changed default to `openrouter/free` (auto-routes to best available free model), updated all references in config, provider registry, context budget, docs, and tests.
- **Migration typo** — Config migration from v0→v1 wrote misspelled model name `arcee-ai/tranny-large-preview:free` (typo: "tranny") instead of the correct model. Fixed to migrate to `openrouter/free`. Hardenend migration test to assert exact expected model.
- **410 not triggering fallback** — `isFallbackStatusCode()` did not include 410 (Gone), so a deprecated model caused immediate failure instead of triggering fallback to another provider. Added 410 to the fallback status code list.

## [1.20.0] - 2026-05-19

### Added
- **CLI flags for `joshbot config`** — `--provider`, `--api-key`, `--api-base`, `--model`, `--set-default`, `--remove` flags for headless configuration
- **`joshbot version` command** — Shows the current version string
- **`internal/configure/` package** — Shared configurator API used by both CLI flags and interactive wizard, gating CLI/interactive parity with tests

### Fixed
- **Model config bugs** — `setDefaultProvider` no longer overwrites per-provider model with registry default; `configureProvider` auto-default now sets `agents.defaults.model`; NVIDIA provider registration now passes `p.Model` on creation and registration

### Changed
- `internal/config/config.go` — Added `JOSHBOT_NVIDIA_API_KEY` shorthand env var support with auto-`Enabled: true`
- `internal/config/config_test.go` — Added `TestMain()` to isolate tests from env var pollution
- Deduplicated `getProviderDisplayName` and `maskAPIKey` into `internal/configure/` package (removed from main.go)
- Replaced deprecated `strings.Title` with `cases.Title` from `golang.org/x/text`

## [1.19.0] - 2026-05-17

### Added

#### Intelligent Memory System
- **Structured Fact format** — Facts stored with SHA256-based `FactID`, category enum (`user_info`, `preference`, `project`, `decision`, `skill`, `system`), confidence scoring (0.0-1.0), source tracking, and deduplication
- **Memory search tool** (`memory_search`) — Keyword + category + tag search with relevance scoring (recency, confidence, access count, keyword match)
- **Markdown-backed persistence** — Facts stored in MEMORY.md with backward-compatible user-editable format
- **Metadata support** — `UserMetadata` struct for tracking memory version, preferences, and last updated
- **Fact reconciliation** — `ReconcileFacts()` merges new facts with existing memory via upsert + dedup + eviction (max 100 facts)
- **Learning extraction** — `learning.go` now extracts structured facts from conversations with LLM-based summarization
- **New files**: `internal/memory/fact.go`, `search.go`, `metadata.go`, `internal/tools/memory_tool.go`

#### Skill Self-Creation
- **Skill Detection** — Weighted heuristic scoring system (`SkillDetector`) that detects skill-worthy patterns from conversation traces (3+ tool use triggers, repeated command patterns, explicit skill creation requests). Threshold: ≥2.0 confidence
- **LLM-based Skill Extraction** — `Extractor` generates valid SKILL.md files from conversation traces using an LLM prompt
- **Skill Validation** — `ValidateSkill()` checks YAML frontmatter, required fields, name uniqueness, and body content
- **Skill Registry Tool** (`skill_registry`) — Exposes `list`, `create`, and `delete` actions to the ReAct loop
- **Skill creation flow** — `CreateSkill()` writes to `~/.joshbot/workspace/skills/{name}/SKILL.md`, auto-discovered by the skills Loader
- **New files**: `internal/skills/detection.go`, `extraction.go`, `validation.go`, `detection_test.go`, `internal/tools/skill_tool.go`

### Changed
- `internal/agent/agent.go` — Wire skill detection into reactLoop (after each iteration and after processing)
- `internal/agent/context.go` — `BuildSmartPrompt` uses strings.Builder, extracted methods
- `internal/learning/learning.go` — Extracted `saveSummary` and `heuristicFallback` methods
- `internal/memory/memory.go` — `WriteFacts`, `ReconcileFacts`, structured markdown output with categorized sections
- `internal/skills/skills.go` — Added `Create()`, `Delete()`, `List()`, `Invalidate()` methods on Loader
- `internal/tools/registry.go` — Registers SkillRegistryTool alongside defaults

## [1.18.0] - 2026-05-17

### Changed
- **AGENTS.md rewritten** — Accurate LOC count (~16,000 non-test Go), Go 1.24.0, correct interface signatures
- **Code simplifications** — Simplified `cleanCommand`, `normalizeUsername`, `joinParts`, `stripHTML`
- **Schema building deduplication** — Replaced duplicated schema building in `toolToProviderTool` with `GenerateSchema`
- **Dead code removal** — Removed `FormatToolResult`, `FormatAssistantToolCalls`, `stringsSplitN`
- **Regex compilation fix** — Eliminated regex in `extractStatusCode`, fixed false positive on port numbers
- **Provider normalization** — Simplified `normalizeProviderName` with case-insensitive lookup

### Added
- Unit tests for `scanStatusCode`, `scanAnyStatusCode`, `normalizeProviderName`

## [1.17.1] - 2026-04-02

### Fixed

- **Broken MCP package removed** — `internal/mcp/` referenced undefined types from MCP SDK v1.4.0, blocking `go vet` and CI
- **Debug output leaks** — replaced 5 `fmt.Printf("[DEBUG]...")` statements in copilot auth with structured `log.Debug()`
- **Checksum verification bypass** — `install.sh` now exits on checksum mismatch instead of continuing with potentially corrupted binary

### Added

- **Smoke tests for 5 zero-coverage packages** — heartbeat (7 tests), skills (14 tests), service (4 tests), pkg/bus (1 test), copilot (6 tests)
- **SECURITY.md** — vulnerability reporting process, security best practices, and architecture notes
- **VERSION file** — proper version tracking for releases

### Changed

- **uninstall.sh rewritten** — now handles Go binary removal, systemd/launchd services, and legacy pipx installation
- **docker-compose.yml** — removed deprecated `version: '3.8'` key
- **CHANGELOG.md** — backfilled missing v1.13-v1.15 entries

## [1.17.0] - 2026-03-05

### Added

#### Async Tools for Long-Running Operations
- **AsyncTool interface** - Tools can implement async execution for background tasks
- **Auto-detection** - Commands like `python`, `npm run`, `make`, `docker build` automatically run async
- **Explicit control** - `async: true` parameter to force background execution
- **Callback notifications** - Background task completion delivered via message bus or CLI
- **Timeout prevention** - Operations can run longer than 60 seconds without timeout errors
- **Better UX**: "Started in background, I'll notify you when done."

#### Debug Logging
- **`--debug` flag** for agent and gateway commands to enable detailed troubleshooting logs
- **HTTP response logging** - Log status codes and model information
- **LLM response details** - Log content length, tool calls, and finish reasons
- **Empty content detection** - Warn when LLM returns empty responses after tool execution

## [1.16.0] - 2026-03-04

### Added

#### Model-Centric Configuration
- **New `models_config` format** - Simplified model configuration with provider auto-detection
- **Fallback chains** - Configure multiple models; automatically try next if primary fails
- **Provider auto-detection** - Model prefixes (`anthropic/`, `groq/`, `ollama/`, etc.) automatically set API base URL
- **Supported providers**: Anthropic, OpenAI, Groq, Ollama, OpenRouter, NVIDIA NIM, DeepSeek, Google Gemini, Cerebras
- **Environment variable support**: `JOSHBOT_MODELS_CONFIG__MODELS__0__NAME`, etc.
- **Backward compatible** - Legacy `providers` format still supported

#### System Prompt Caching
- **Intelligent caching** - Static system prompt cached in memory, reducing file I/O on every message
- **mtime-based invalidation** - Cache automatically rebuilds when source files change
- **Tracked files**: AGENTS.md, SOUL.md, USER.md, TOOLS.md, IDENTITY.md, memory/MEMORY.md, skills/*/SKILL.md
- **Force refresh** - `InvalidatePromptCache()` for programmatic cache clearing

### Changed

- Configuration now supports both model-centric and provider-centric formats
- Faster response times due to reduced file I/O

## [1.15.0] - 2026-03-01

### Added

#### GitHub Copilot Integration
- **GitHub Copilot LLM provider** with OAuth device code flow authentication
- **Token management** with automatic refresh and expiry detection
- **Streaming support** for Copilot chat completions
- **Model listing** via Copilot API

#### Subagent System
- **Spawn tool** for creating isolated subagent tasks
- **Subagent isolation** with separate contexts and memory
- **Background execution** for long-running operations

## [1.14.0] - 2026-02-28

### Added

#### MCP (Model Context Protocol) Support
- **MCP server connections** via HTTP/SSE and stdio transports
- **Dynamic tool discovery** from connected MCP servers
- **Tool registration** with joshbot's tool registry

#### Cron Scheduling
- **Recurring task scheduler** with cron expression support
- **Automatic task execution** in background subagents
- **Task persistence** across restarts

### Fixed

- Memory consolidation race conditions during idle periods
- Session file corruption on abrupt shutdowns

## [1.13.0] - 2026-02-27

### Added

#### Heartbeat System
- **Periodic health checks** for self-monitoring
- **Automatic maintenance tasks** during idle periods
- **Memory consolidation** triggered by heartbeat

#### Learning System
- **Cross-session learning** from user feedback
- **Pattern recognition** for common tasks
- **Knowledge graph** for persistent entity relationships

### Changed

- Improved error messages for provider connection failures
- Better handling of rate limits across all providers

## [1.12.1] - 2026-02-25

### Fixed

- **Model not updating when changing providers** - `joshbot config` now correctly updates the default model when switching providers, preventing 404 errors with NVIDIA NIM
- **Missing tool_call_id field** - Tool result messages now correctly include `tool_call_id`, fixing "missing field tool_call_id" errors with strict providers like Arcee AI

## [1.12.0] - 2026-02-24

### Added

#### Web Fetch Enhancement
- **Exa crawl integration** for web_fetch tool to handle JavaScript-rendered pages
- Improved content extraction from dynamic websites

### Fixed

- **Version display** - Release binaries now show actual version instead of "dev"
  - Fixed ldflags in GoReleaser, CI workflow, and Dockerfile
- **Status command** - Telegram and Workspace restricted now show "enabled"/"disabled" instead of "(exists)"

## [1.11.0] - 2026-02-24

### Added

#### Enhanced Ollama Integration
- **Model listing in configure wizard** - Fetches and displays available Ollama models
- **`--model` flag** for `agent` and `onboard` commands to override model at runtime
- **Configurable timeout** for Ollama provider (default 300s for CPU-only)
- **CPU tips** displayed after Ollama configuration

### Changed

- **No fallback on Ollama 404** - "Model not found" errors require user to `ollama pull <model>`
- Improved error handling with provider-aware fallback logic

## [1.1.0] - 2026-02-21

### Added

#### Interactive Telegram Setup
- Telegram setup wizard integrated into `joshbot onboard` (Step 4)
- Guides users through @BotFather bot creation
- Validates bot token via `getMe` API before saving
- Optional allowed usernames configuration
- Auto-saves to config without manual editing

#### Service Management
- `joshbot service install`: Install joshbot as a system service
- `joshbot service uninstall`: Remove the system service
- `joshbot service status`: Check service status
- **Systemd support** (Linux): Service installed to `/etc/systemd/system/joshbot.service`
- **Launchd support** (macOS): Service installed to `~/Library/LaunchAgents/com.joshbot.plist`
- Auto-start on boot with proper logging

#### Enhanced Onboard Flow
- Step 1: API key setup
- Step 2: Personality selection
- Step 3: Model selection
- Step 4: Telegram setup (optional)
- Step 5: Service installation (recommended for Telegram users)
- Explains why service install is needed for Telegram bots

### Changed

- Onboard now offers to start gateway automatically after Telegram setup
- Telegram token validation happens during setup (not at runtime)

## [1.0.0] - 2026-02-21

### Migration Notes

This release marks a complete rewrite of joshbot from Python to Go. The new Go implementation
offers improved performance, simpler deployment, and a more robust architecture. 

**Key changes:**
- Configuration from previous Python version is **not** compatible
- Memory files (MEMORY.md, HISTORY.md) in `~/.joshbot/` remain compatible
- Sessions in `~/.joshbot/sessions/` remain compatible
- Skills in `workspace/skills/` remain compatible
- Run `./joshbot onboard` to set up fresh configuration

### Added

#### Core Architecture
- Complete Go implementation (~3,600 LOC) with goroutine-based concurrency
- Message bus architecture decoupling chat channels from the agent loop
- ReAct agent loop with tool execution and reflection cycles (max 20 iterations)
- Multi-provider LLM support via OpenRouter-compatible APIs

#### Memory System
- **MEMORY.md**: Persistent long-term memory, always included in context
- **HISTORY.md**: Searchable event log (grep-based) for recent context
- Self-learning memory consolidation during idle periods
- Context compression for small models with token budgeting

#### Skills System
- Skill discovery from `workspace/skills/{skill}/SKILL.md` files
- Progressive loading: summary on first use, full content after first execution
- YAML frontmatter for skill metadata (name, triggers, description)

#### Tools
- Filesystem tool: read, write, list, glob, grep operations
- Shell tool: command execution with safety deny-list
- Web tool: search and fetch capabilities
- Message tool: send messages to Telegram/CLI
- Spawn tool: create subagents for isolated long-running tasks
- Cron tool: schedule recurring tasks

#### Proactive Behavior
- **Cron scheduling service**: Background scheduler for periodic tasks
- **Heartbeat**: Periodic health checks and self-maintenance tasks
- Subagent runner for background task isolation

#### Channels
- **Telegram**: Full bot integration with commands and inline queries
- **CLI**: Interactive terminal mode with readline support

#### CLI & Configuration
- `joshbot onboard`: First-time setup wizard
- `joshbot agent`: Interactive CLI mode
- `joshbot gateway`: Telegram + all channels mode
- `joshbot status`: Show configuration and status
- `--force` flag: Force fresh onboarding (skips existing config check)
- `--keep-data` flag: Preserve memory and sessions during re-onboarding
- Configuration via `~/.joshbot/config.json` with `JOSHBOT_` env var prefix

### Changed

- Default model: `anthropic/claude-3.5-sonnet` via OpenRouter
- Default log level: WARNING (cleaner output)
- Session format: JSONL files in `~/.joshbot/sessions/`
- Architecture: Channel-based message bus (publish/subscribe pattern)

### Removed

- Python runtime dependency
- litellm library (replaced with native HTTP client)
- pip/pipx installation (Go binary distribution)
- Python virtual environment management
