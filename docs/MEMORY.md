# Memory - Project Decisions & Learnings

This file captures significant decisions, trade-offs, and lessons learned during development.

---

## 2026-02-21 21:55: Cross-Platform Service Package Build Failures

**Context**: Implementing service management (systemd/launchd) with platform-specific code.

**Failures encountered**:
1. `unsupported.go` and `factory_other.go` both had `//go:build !linux && !darwin` tag, causing duplicate symbol errors
2. `service.go` had a `NewManager()` function that conflicted with factory files
3. Factory files were missing the `NewManager` function declaration

**Root cause**: Incorrect factory pattern implementation - factory function was in the interface file instead of platform-specific files.

**Fix**:
- Removed `NewManager` from `service.go` (interface file)
- Each factory file (`factory_linux.go`, `factory_darwin.go`, `factory_other.go`) exports `NewManager(Config) (Manager, error)`
- Platform implementations (`systemd.go`, `launchd.go`, `unsupported.go`) export `newPlatform(cfg) (*platformManager, error)` (unexported)
- Test cross-platform builds with `GOOS=... GOARCH=... go build ./...` before tagging releases

**Prevention rule**: When using build tags for platform-specific code, never put factory functions in shared interface files.

---

## 2026-02-21 22:00: Running as Root - sudo Not Available

**Context**: Service installation failed with "sudo: executable file not found in $PATH" when running as root.

**Root cause**: The systemd implementation always prefixed commands with `sudo`, but `sudo` doesn't exist when already running as root (uid 0).

**Fix**: Detect root with `os.Getuid() == 0` and skip sudo prefix.

```go
func sudoCmd(cmd string) string {
    if os.Getuid() == 0 {
        return cmd
    }
    return "sudo " + cmd
}
```

**Prevention rule**: Always check `os.Getuid() == 0` before using sudo in scripts/tools that may run as root.

---

## 2026-02-21 22:20: Terminal Escape Sequences in User Input

**Context**: Telegram token validation failed with `net/url: invalid control character in URL` - escape sequences like `\x1b[C` (arrow keys) were captured in the token string.

**Root cause**: When users paste or edit text in terminal prompts, arrow key presses and other control sequences can be captured by the input reader, embedding escape characters in the final string.

**Fix**: Sanitize user input by stripping control characters before validation:
```go
func sanitizeToken(token string) string {
    // Remove control characters (0x00-0x1F and 0x7F) except space
    var result strings.Builder
    for _, r := range token {
        if r >= 0x20 && r != 0x7F {
            result.WriteRune(r)
        }
    }
    return result.String()
}
```

**Prevention rule**: Always sanitize terminal input for control characters when the input will be used in URLs, API calls, or file paths.

---

## 2026-02-21 22:20: Non-Systemd Systems

**Context**: Service install failed with "systemctl: executable file not found in $PATH" on systems without systemd.

**Root cause**: The systemd implementation assumed systemctl exists without checking.

**Fix**: Check for systemctl availability before attempting service operations:
```go
func checkSystemctl() error {
    _, err := exec.LookPath("systemctl")
    if err != nil {
        return ErrSystemdNotDetected
    }
    return nil
}
```

Return a helpful error message explaining alternatives when systemctl is not found.

**Prevention rule**: Always verify external tool availability before using it, and provide helpful error messages with alternatives.

---

## 2026-02-21 22:20: Reconfiguration Without Showing Current Values

**Context**: When user chose "Keep existing data" during onboard, they had to re-type all values even if they wanted to keep them.

**Root cause**: The prompt functions didn't receive or display existing configuration values as defaults.

**Fix**: 
1. Load existing config when reconfiguring
2. Show current values (masked for secrets) as defaults
3. Allow pressing Enter to keep existing values
4. For Telegram: offer Keep/Change/Disable options when already configured

**Prevention rule**: Always show current values as defaults when offering to "keep" or "reconfigure" existing settings.

---

## 2026-02-27 13:45: Tool Config & Security Behavior Documentation Updates

**Context**: Added new tool configuration fields and clarified security behavior for SSRF protection and GitHub Copilot OAuth.

**Decision**:
- Document `tools.shell_allow_list` and `tools.filesystem_allowed_paths` in configuration docs and README.
- Explicitly state SSRF protection behavior for `web_fetch` (blocks localhost/private IPs/metadata hosts).
- Document GitHub Copilot device OAuth flow, token storage, and troubleshooting.

**Reasoning**: These settings and behaviors are safety-critical and frequently referenced during onboarding and troubleshooting.

---

## 2026-02-27 Copilot OAuth Authentication Path Bug

**Context**: Successful OAuth device flow but bot reports "not authenticated" and "provider not configured".

**Root Cause**: 
- `LoadToken()` and `SaveToken()` in `internal/copilot/auth.go` expect a **home directory** (`~`) as input
- They internally append `.joshbot` to create the auth file path: `~/.joshbot/auth.json`
- But callers in `main.go` were passing `config.DefaultHome` which is already `~/.joshbot`
- This resulted in double `.joshbot` path: `~/.joshbot/.joshbot/auth.json`
- Token was saved to wrong location and never found on subsequent loads

**Fix**:
- Added `GetHomeDir()` and `AuthFilePath()` helper functions in `internal/copilot/auth.go`
- Updated all callers in `main.go` to use `copilot.GetHomeDir()` to get the correct path
- Also fixed ignored error in `runAuthCopilot` where `loadConfig()` failure would silently overwrite existing config

**Prevention Rule**: When a function expects a home directory (`~`), never pass a path that already contains `.joshbot`. Always verify the input path format matches the function's documented expectations.

---

## 2026-03-04 Model-Centric Configuration Pattern

**Context**: Simplifying provider configuration by making models the primary configuration unit rather than providers.

**Decision**: 
- Introduced `models_config` section with `models` array and `agent` settings
- Each model has `name`, `model` (with provider prefix), `api_key`, `api_base`
- Provider auto-detected from model prefix (e.g., `groq/llama-3.3-70b` → Groq provider)
- Fallback chains supported via `agent.fallback` array

**Benefits**:
- Simpler configuration: One place to define model + API key + endpoint
- Provider auto-detection reduces boilerplate
- Fallback chains improve resilience
- Backward compatible with legacy `providers` format

**Design Patterns**:
- `DetectProvider(model string) ProviderInfo` - Returns provider name, API format, and default base URL
- `StripProviderPrefix(model string) string` - Removes prefix to get actual model ID
- `ResolveModelConfig(name string)` - Combines model config with detected provider defaults

**Prevention Rule**: When designing config formats, prefer user-centric models over implementation-centric providers. Users think in terms of "which model should I use", not "which provider should I configure".

---

## 2026-03-04 System Prompt Caching Strategy

**Context**: Reducing redundant file I/O on every message by caching the static system prompt.

**Decision**:
- Cache static prompt content in memory
- Use mtime-based invalidation to detect file changes
- Track all source files: AGENTS.md, SOUL.md, USER.md, TOOLS.md, IDENTITY.md, MEMORY.md, skills/*/SKILL.md
- Double-checked locking pattern for thread-safe cache access

**Implementation**:
- `cacheBaseline` struct tracks file existence and max mtime
- `promptCache` struct holds cached content and baseline
- `BuildPromptCached()` checks cache validity before rebuilding
- `InvalidatePromptCache()` for force refresh

**Trade-offs**:
- Memory overhead: Cached prompt stored in memory (~10-50KB typical)
- File system precision: Some filesystems have 1-2 second mtime precision (acceptable for this use case)
- Concurrency: Double-checked locking with RLock/RWMutex ensures thread safety

**Prevention Rule**: When implementing caching, always include a way to invalidate the cache. Prefer content-based or mtime-based invalidation over TTL-based for data that changes infrequently.

---

## 2026-05-17 CI Failure: gofmt Formatting After Release Tag

**Context**: v1.19.0 release was tagged and pushed alongside main commit. CI run failed because `gofmt -l .` returned `internal/learning/learning.go` — vertical-alignment padding in a struct literal.

**Root Cause**: Release tag was pushed at the same time as the main commit, before CI could verify the code. The `gofmt` check (`.github/workflows/ci.yml`) caught the formatting issue, but since the tag was already pushed, it had to be deleted and re-pushed.

**Fix**:
1. Run `gofmt -w internal/learning/learning.go` to fix formatting
2. Added `gofmt -d .` to pre-release checklist in AGENTS.md
3. Documented release process: push main → WAIT for CI green → cut tag
4. Recorded lesson in `tasks/lessons.md`

**Prevention Rule**: NEVER push main commit and release tag simultaneously. Push main first, wait for CI green checkmark, then cut and push the tag. Always run `gofmt -d .` (or `gofmt -l .`) before committing.

---

## 2026-05-18 Env Var Backward Compatibility for Shorthand Names

**Context**: Dogfood testing revealed that `JOSHBOT_OPENROUTER_API_KEY` (single-underscore) was not picked up by the config system, which expects `JOSHBOT_PROVIDERS__OPENROUTER__API_KEY` (double underscores, full path).

**Decision**: Added backward-compatible fallback checks in `applyEnvOverrides()` in `internal/config/config.go`:
- `JOSHBOT_PROVIDERS__OPENROUTER__API_KEY` has priority, falls back to `JOSHBOT_OPENROUTER_API_KEY`
- `JOSHBOT_PROVIDERS__NVIDIA__API_KEY` has priority, falls back to `JOSHBOT_NVIDIA_API_KEY`
- Env var presence also sets `Enabled: true` automatically (no need for config.json entry)

**Reasoning**: The shorthand forms are intuitive and commonly set by users. The `__` (double-underscore) path separator is technically precise but easy to get wrong. Supporting both ensures backward compatibility and better DX.

**Prevention Rule**: When designing env var override systems, always support both the canonical path-based name and common shorthand names for widely-used config values. The `__` separator in env vars is non-obvious — document it clearly.

---

## 2026-05-18 Config Test Environment Isolation with TestMain

**Context**: Adding `JOSHBOT_OPENROUTER_API_KEY` fallback in `applyEnvOverrides` caused `TestLoadSanitizesWhitespace` to fail because the real env var overwrote the test's config value.

**Decision**: Added `TestMain()` to `internal/config/config_test.go` that saves all `JOSHBOT_` env vars, clears them before tests, and restores them after.

**Prevention Rule**: Any config package that reads environment variables must use `TestMain` or equivalent to isolate tests from the user's shell environment. Env-sensitive tests should never assume a clean environment.

---

## 2026-06-04 Core Identity Prompt Optimized (411 → 299 words)

**Context**: The `buildCoreIdentity()` function in `internal/agent/context.go` is the foundational system prompt sent on every LLM interaction. At 411 words / ~300 tokens, it was verbose and would benefit from conciseness.

**Decision**: Replaced with Variant C (Action-Oriented) at 299 words (27% reduction). Uses verb-led directives ("Read before write"), Do/Don't discipline, and flat bullet structure. Removed redundant prose and contradictory patterns. Structure: IDENTITY → WORK DISCIPLINE → TOOL DIRECTIVES → MEMORY RULES → CONVERSATION RULES.

**Verification**: 17 required content checks pass (identity, all 5 tools, memory, coherence rules), 8 banned phrases absent, no contradictions, word count under 350 target. All 14 cache invalidation tests pass — cache logic depends on file mtimes, not prompt content.

**Prompt eval harness** created at `internal/agent/prompt_eval_test.go` with 5 test functions (Eval, Conciseness, NoContradictions, ActionableInstructions) for future prompt changes.

**Remaining concerns**: Other prompt surfaces (subagent system prompt at ~50 tokens, skill extraction prompt at ~200 tokens, context compression prompts at ~25 tokens) could benefit from similar treatment but are much smaller. The memory skill (~1200 chars loaded as full content) is the next highest-leverage surface.

---

## 2026-06-04 All Tool Descriptions Trimmed (~30-40% reduction)

**Context**: Tool schemas dominate every LLM call at ~19KB compact JSON — ~10× larger than `buildCoreIdentity()`. The Description() and Parameter description strings across 22 tools (11 base + 6 filesystem aliases + 5 web aliases) contained verbose prose.

**Files changed**: `internal/tools/filesystem.go`, `shell.go`, `web.go`, `message.go`, `memory_tool.go`, `configure.go`, `chain_tool.go`, `subagent_tool.go`, `skill_tool.go`, `agent_config_tool.go`, `web_alias.go`

**Pattern**: Removed redundant explanations ("Use this to..."), parenthetical elaborations, and empty filler words. Parameter descriptions shortened from phrases like "Skill name (required for create/delete)" to "Skill name (required: create/delete)". Tool descriptions shortened from complete sentences to concise directives.

**Verification**: `go build ./cmd/joshbot` clean, `gofmt -d .` empty, `go test -race ./...` all 19 packages pass with fresh cache.

---

## 2026-06-04 Skill.Always Field Wired Up (was dead code)

**Context**: The `Skill.Always` field in `internal/skills/skills.go` was parsed from SKILL.md frontmatter but never read by any production code. The `skills/memory/SKILL.md` had `always: true` but it did nothing — all skills were loaded as one-line XML summaries regardless.

**Decision**: Modified `LoadSummary()` to check `Always` — skills with `Always == true` now inject their full content (via `GetContent()`) wrapped in `<skill-content name="...">...</skill-content>` after the summary line. Non-always skills keep the existing one-line summary behavior. Removed stale "NOTE: Full content is NO LONGER included" comment.

**Impact**: The memory skill (~1200 chars) is now actually injected into every session's system prompt as intended. Other skills (skill-creator, cron, github) remain as summaries.

**Verification**: `go test ./internal/skills/` — 54 tests pass. `go test ./internal/agent/` — all pass.

---

## 2026-06-04 MEMORY.md Size Capped at 4KB

**Context**: MEMORY.md has no size limit — the learning system appends facts periodically, causing unbounded growth that silently bloats the system prompt.

**Decision**: Added `MaxMemorySize` config field (default 4096 bytes) to `AgentDefaults`. `LoadMemory()` now trims content if it exceeds the limit: splits on `\n---\n`, keeps the header + newest entries until under limit. Trimmed version is written back to disk.

**Files changed**: `internal/config/config.go` (new field + default + validation), `internal/memory/memory.go` (trimming logic + functional option), `cmd/joshbot/main.go` (plumb config), `internal/memory/memory_test.go` (8 new test cases).

**Verification**: `go test -race ./...` — all 23 packages pass. 8 new memory tests cover under-limit, over-limit, no-separator, header-exceeds, empty, and end-to-end scenarios.
