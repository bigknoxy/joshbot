package tools

import (
	"strings"
	"testing"
	"time"
)

// The read-only permission tier denies the shell entirely: every command is
// refused with CodeDeny before the allowlist, the deny list and the approval
// gate, so an interactive approver cannot allow a command while the tier is
// in force.
func TestShellDenied_RefusesEveryCommand(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), true)
	tool.SetShellDenied(true)

	for _, cmd := range []string{"echo hi", "true"} {
		res := tool.Execute(nil, map[string]any{"command": cmd})
		if res.Error == nil {
			t.Fatalf("command %q was not denied by the shell-off tier", cmd)
		}
		if ErrorCode(res.Error) != CodeDeny {
			t.Errorf("command %q: denial error code = %v, want %v", cmd, ErrorCode(res.Error), CodeDeny)
		}
		if !strings.Contains(res.Error.Error(), "read-only permission tier") {
			t.Errorf("command %q: denial message does not name the tier: %v", cmd, res.Error)
		}
	}
}

func TestShellDenied_Toggle(t *testing.T) {
	tool := NewShellTool(5*time.Second, t.TempDir(), true)
	if tool.shellDenied {
		t.Fatal("shellDenied defaults to true")
	}
	tool.SetShellDenied(true)
	if !tool.shellDenied {
		t.Fatal("SetShellDenied(true) did not set")
	}
	tool.SetShellDenied(false)
	if tool.shellDenied {
		t.Fatal("SetShellDenied(false) did not clear")
	}
}
