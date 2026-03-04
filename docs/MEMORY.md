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
