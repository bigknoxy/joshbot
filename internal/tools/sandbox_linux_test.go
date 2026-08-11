//go:build linux

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests prove real containment: a spawned command genuinely cannot read
// or write outside the permitted set. They assert on the kernel's refusal, not
// on any string we screened — screening is what shell_deny.go does, and its
// limits are why this exists.
//
// The test binary doubles as the sandbox helper. TestMain re-enters as the
// helper when invoked with SandboxHelperArg, which is how the shell tool's
// re-exec path can be exercised without building a separate binary.

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == SandboxHelperArg {
		code, err := RunSandboxHelper(os.Args[2:])
		if err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// sandboxedTool returns a shell tool that re-execs this test binary as its
// helper, skipping the test if the kernel cannot enforce anything.
func sandboxedTool(t *testing.T, workspace string, allowNet bool) *ShellTool {
	t.Helper()
	if !SandboxSupported() {
		t.Skip("kernel does not provide landlock; containment cannot be asserted here")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	tool := NewShellTool(30*time.Second, workspace, false)
	tool.SetSandbox(SandboxWorkspace, allowNet)
	tool.helperPath = self
	return tool
}

func TestSandbox_ReadInsideWorkspaceIsAllowed(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "ok.txt"), []byte("workspace content"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := sandboxedTool(t, ws, false)
	res := tool.Execute(context.Background(), map[string]any{
		"command": "cat " + filepath.Join(ws, "ok.txt"),
	})
	if res.Error != nil {
		t.Fatalf("reading inside the workspace failed: %v", res.Error)
	}
	if !strings.Contains(res.Output, "workspace content") {
		t.Errorf("expected the file contents, got %q", res.Output)
	}
}

// The point of the whole exercise: a secret outside the workspace is
// unreachable no matter how the command is written.
func TestSandbox_ReadOutsideWorkspaceIsDenied(t *testing.T) {
	ws := t.TempDir()
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "credentials.txt")
	if err := os.WriteFile(secret, []byte("SUPER-SECRET-VALUE"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := sandboxedTool(t, ws, false)

	// Several spellings of the same intent. A text screen would have to
	// anticipate each; the kernel does not care which was used.
	for _, cmd := range []string{
		"cat " + secret,
		"cat < " + secret,
		"head -c 100 " + secret,
		"sh -c 'cat " + secret + "'",
		"cp " + secret + " " + filepath.Join(ws, "stolen.txt"),
	} {
		t.Run(cmd[:min(len(cmd), 24)], func(t *testing.T) {
			res := tool.Execute(context.Background(), map[string]any{"command": cmd})
			combined := res.Output
			if res.Error != nil {
				combined += res.Error.Error()
			}
			if strings.Contains(combined, "SUPER-SECRET-VALUE") {
				t.Fatalf("the secret was readable: %q", combined)
			}
		})
	}

	// And the copy attempt must not have produced a readable copy.
	if data, err := os.ReadFile(filepath.Join(ws, "stolen.txt")); err == nil {
		if strings.Contains(string(data), "SUPER-SECRET-VALUE") {
			t.Fatal("the secret was copied into the workspace")
		}
	}
}

func TestSandbox_WriteOutsideWorkspaceIsDenied(t *testing.T) {
	ws := t.TempDir()
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "target.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := sandboxedTool(t, ws, false)
	tool.Execute(context.Background(), map[string]any{
		"command": "echo tampered > " + victim,
	})

	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("victim file: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("file outside the workspace was modified: %q", data)
	}
}

func TestSandbox_WriteInsideWorkspaceIsAllowed(t *testing.T) {
	ws := t.TempDir()
	tool := sandboxedTool(t, ws, false)

	res := tool.Execute(context.Background(), map[string]any{
		"command": "echo written > " + filepath.Join(ws, "new.txt"),
	})
	if res.Error != nil {
		t.Fatalf("writing inside the workspace failed: %v", res.Error)
	}
	data, err := os.ReadFile(filepath.Join(ws, "new.txt"))
	if err != nil {
		t.Fatalf("expected the file to exist: %v", err)
	}
	if strings.TrimSpace(string(data)) != "written" {
		t.Errorf("got %q", data)
	}
}

// Exfiltration is the payoff for most attacks that get this far, so a
// filesystem-only sandbox would miss the point.
func TestSandbox_NetworkIsDeniedByDefault(t *testing.T) {
	ws := t.TempDir()
	addr := startLocalListener(t)

	denied := sandboxedTool(t, ws, false)
	res := denied.Execute(context.Background(), map[string]any{
		"command": "curl -s -m 5 -o /dev/null -w '%{http_code}' http://" + addr + "/ ; echo \" exit=$?\"",
	})
	if strings.Contains(res.Output, "http=200") || strings.Contains(res.Output, "200 exit=0") {
		t.Fatalf("outbound connection succeeded with network denied: %q", res.Output)
	}

	// Control: the same command with network allowed must reach the listener,
	// otherwise this test would pass even if nothing were enforced.
	allowed := sandboxedTool(t, ws, true)
	ctrl := allowed.Execute(context.Background(), map[string]any{
		"command": "curl -s -m 5 -o /dev/null -w '%{http_code}' http://" + addr + "/ ; echo \" exit=$?\"",
	})
	if !strings.Contains(ctrl.Output, "200") {
		t.Skipf("control run could not reach the listener either (%q); the denial above proves nothing", ctrl.Output)
	}
}

func TestSandbox_HelperRejectsBadUsage(t *testing.T) {
	if code, err := RunSandboxHelper([]string{"only-one-arg"}); err == nil || code == 0 {
		t.Errorf("expected a usage error, got code=%d err=%v", code, err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
