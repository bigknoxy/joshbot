# Bug Fix Plan: Version Display and Status Output

## Goal
Fix two bugs in `joshbot status` command:
1. Version shows "dev" instead of actual release version
2. Telegram and Workspace restricted show "(exists)" instead of meaningful boolean status

---

## Bug 1: Version Shows "dev"

### Root Cause
The `Version` variable in `cmd/joshbot/main.go:75` defaults to `"dev"`. GoReleaser is configured to inject the version via ldflags at `.goreleaser.yaml:31`:
```
-X github.com/bigknoxy/joshbot/cmd/joshbot.Version={{.Version}}
```

The install script downloads pre-built binaries from GitHub releases. If the downloaded binary shows "dev", the ldflags injection didn't work during release.

### Root Cause (VERIFIED)
The GoReleaser ldflags path is **incorrect** at `.goreleaser.yaml:31`:
```yaml
- -X github.com/bigknoxy/joshbot/cmd/joshbot.Version={{.Version}}
```

**Should be:**
```yaml
- -X main.Version={{.Version}}
```

Go's ldflags require `main.Version` for variables in the main package, not the full module path.

**Verified locally:**
```bash
# Wrong path - doesn't work:
go build -ldflags "-X github.com/bigknoxy/joshbot/cmd/joshbot.Version=v0.1.0" ./cmd/joshbot
./joshbot status | grep Version  # Shows: dev

# Correct path - works:
go build -ldflags "-X main.Version=v0.1.0" ./cmd/joshbot
./joshbot status | grep Version  # Shows: v0.1.0
```

### Fix
Update `.goreleaser.yaml:31` to use correct ldflags path, then trigger a new release.

---

## Bug 2: statusBool() Used Incorrectly

### Root Cause
`statusBool()` at `main.go:2432-2437` returns:
- `"(exists)"` for `true`
- `"(missing)"` for `false`

This is semantically correct for file/directory existence, but wrong for boolean config settings.

### Current Usage (Wrong)
- Line 1915: `statusBool(cfg.Channels.Telegram.Enabled)` → "(exists)"
- Line 1916: `statusBool(cfg.Tools.RestrictToWorkspace)` → "(exists)"

### Fix
Add new helper `boolStatus()` for config booleans and use it:

```go
func boolStatus(b bool) string {
    if b {
        return "enabled"
    }
    return "disabled"
}
```

Update lines 1915-1916:
```go
fmt.Printf("Telegram:       %s\n", boolStatus(cfg.Channels.Telegram.Enabled))
fmt.Printf("Workspace restricted: %s\n", boolStatus(cfg.Tools.RestrictToWorkspace))
```

Keep `statusBool()` for file existence checks.

---

## Implementation Checklist

- [ ] **Bug 1: Fix ldflags in GoReleaser**:
  - [ ] Edit `.goreleaser.yaml:31`: change to `-X main.Version={{.Version}}`
  - [ ] Verify locally: `go build -ldflags "-X main.Version=test" ./cmd/joshbot && ./joshbot status | grep Version`

- [ ] **Bug 2: Fix statusBool misuse**:
  - [ ] Add `boolStatus()` helper at `main.go:~2438`
  - [ ] Update lines 1915-1916 to use `boolStatus()` instead of `statusBool()`

- [ ] **Verification**:
  - [ ] Run `go fmt ./... && go vet ./... && go test ./...`
  - [ ] Test: `./joshbot status` shows correct version and "enabled"/"disabled"

- [ ] **Release** (for Bug 1 to take effect):
  - [ ] Commit changes
  - [ ] Tag and trigger GoReleaser
  - [ ] Verify new release binary has correct version

---

## Acceptance Criteria
1. `joshbot status` shows actual version (e.g., "v0.1.0") when built with ldflags
2. Telegram status shows "enabled" or "disabled"
3. Workspace restricted shows "enabled" or "disabled"
