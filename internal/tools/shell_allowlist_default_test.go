package tools

import (
	"strings"
	"testing"
	"time"
)

// The panel's compromise for issue #150: on a platform with no OS-level
// containment, the deny list is bypassable and must not be the sole boundary,
// so the shell tool falls back to allowlist-only when the operator supplied no
// explicit allowlist. On platforms that do provide a sandbox (Linux/Landlock,
// macOS/Seatbelt) the unrestricted default is preserved and containment is
// opt-in.
func TestDefaultAllowlist_GatedOnSandboxAvailability(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false)

	// "printf" is deliberately not in defaultUnsandboxedAllowlist.
	res := tool.Execute(nil, map[string]any{"command": "printf hi"})

	blockedByAllowlist := res.Error != nil && strings.Contains(res.Error.Error(), "allowlist")

	if SandboxAvailable() {
		if blockedByAllowlist {
			t.Fatalf("a sandbox-capable platform must not impose the default allowlist; got %v", res.Error)
		}
	} else {
		if !blockedByAllowlist {
			t.Fatalf("a platform with no sandbox must fall back to allowlist-only; command was not blocked (err=%v)", res.Error)
		}
	}
}

// An explicit operator allowlist always wins, on every platform, and is never
// replaced by the default fallback.
func TestExplicitAllowlist_NotOverriddenByDefault(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), false, "printf")
	res := tool.Execute(nil, map[string]any{"command": "printf hi"})
	if res.Error != nil && strings.Contains(res.Error.Error(), "allowlist") {
		t.Fatalf("explicitly allowlisted command was blocked: %v", res.Error)
	}
}
