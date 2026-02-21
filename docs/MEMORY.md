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
