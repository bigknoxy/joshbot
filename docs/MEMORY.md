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
