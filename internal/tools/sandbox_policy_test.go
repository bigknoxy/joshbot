package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Granting the shared temp directory hands a command every other process's
// temporary files, which is most of what confining reads was for.
//
// This is asserted directly on the policy rather than through a spawned
// command: the helper sets TMPDIR to the private scratch directory before
// building the policy, so os.TempDir() inside the policy would return the
// safe path and an end-to-end test would pass even if the shared /tmp were
// granted. Mutation testing found exactly that hole.
func TestDefaultSandboxPolicy_DoesNotGrantSharedTempDir(t *testing.T) {
	ws := t.TempDir()

	// Force the system temp dir to a known value that is NOT the workspace.
	shared := t.TempDir()
	t.Setenv("TMPDIR", shared)

	p := DefaultSandboxPolicy(ws)

	for _, granted := range p.ReadWritePaths {
		if granted == shared || granted == os.TempDir() {
			t.Errorf("policy grants the shared temp directory %q; a secret written there by any "+
				"other process would be readable", granted)
		}
	}

	// The private scratch directory must be granted, or commands have nowhere
	// to write temporary files and the sandbox is unusable.
	want := SandboxTempDir(ws)
	var found bool
	for _, granted := range p.ReadWritePaths {
		if granted == want {
			found = true
		}
	}
	if !found {
		t.Errorf("policy does not grant the private scratch dir %q; commands need somewhere to write", want)
	}
	if !strings.HasPrefix(want, ws) {
		t.Errorf("scratch dir %q is not inside the workspace %q", want, ws)
	}
}

// $HOME must not be granted wholesale. SSH keys and joshbot's own config live
// there, and they are the things most worth stealing.
func TestDefaultSandboxPolicy_DoesNotGrantHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to check")
	}

	p := DefaultSandboxPolicy(t.TempDir())
	for _, granted := range append(p.ReadWritePaths, p.ReadOnlyPaths...) {
		if granted == home || granted == filepath.Clean(home)+string(filepath.Separator) {
			t.Errorf("policy grants all of $HOME (%q)", granted)
		}
	}
}

// The refusal paths cannot be exercised on a machine whose kernel supports
// Landlock, so the decision is tested directly. A sandbox that quietly becomes
// no sandbox is worse than none: the operator stops thinking about it.
func TestSandboxPreflight(t *testing.T) {
	cases := []struct {
		name      string
		mode      SandboxMode
		available bool
		supported bool
		wantErr   bool
	}{
		// The zero value of SandboxMode is "", not SandboxOff. It must behave
		// as off, or a bare-constructed ShellTool refuses on any host without
		// Landlock (e.g. macOS). Regression guard for #138.
		{"zero value means off", SandboxMode(""), false, false, false},
		{"zero value off on a capable host", SandboxMode(""), true, true, false},
		{"off needs nothing", SandboxOff, false, false, false},
		{"off on a capable host", SandboxOff, true, true, false},
		{"on and fully capable", SandboxWorkspace, true, true, false},
		{"on but not implemented here", SandboxWorkspace, false, false, true},
		{"on, implemented, kernel lacks it", SandboxWorkspace, true, false, true},
		// Separates the two checks. Without this case the availability check
		// is redundant — the support check catches the same inputs — and
		// deleting it goes unnoticed.
		{"on, kernel capable but no implementation", SandboxWorkspace, false, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sandboxPreflight(tc.mode, tc.available, tc.supported)
			if tc.wantErr && err == nil {
				t.Fatal("expected a refusal, got nil — the command would have run unconfined")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

// A bare-constructed ShellTool leaves sandbox at its zero value (""). It must
// take the plain "sh -c" path, not the sandbox helper — otherwise it refuses on
// any host without Landlock. Regression guard for #138; runs on every platform.
func TestShellTool_ZeroValueSandbox_TakesPlainPath(t *testing.T) {
	tool := NewShellTool(time.Second, t.TempDir(), false)
	cmd, err := tool.buildExecCmd(context.Background(), "echo hi", "")
	if err != nil {
		t.Fatalf("bare-constructed tool refused to build a command: %v", err)
	}
	for _, arg := range cmd.Args {
		if arg == SandboxHelperArg {
			t.Fatalf("zero-value sandbox took the helper path: args=%v", cmd.Args)
		}
	}
}

func TestParseSandboxMode(t *testing.T) {
	for _, in := range []string{"", "off", "OFF", " none ", "disabled", "false"} {
		if m, ok := ParseSandboxMode(in); !ok || m != SandboxOff {
			t.Errorf("ParseSandboxMode(%q) = %q,%v; want off,true", in, m, ok)
		}
	}
	for _, in := range []string{"workspace", "WORKSPACE", " on ", "true"} {
		if m, ok := ParseSandboxMode(in); !ok || m != SandboxWorkspace {
			t.Errorf("ParseSandboxMode(%q) = %q,%v; want workspace,true", in, m, ok)
		}
	}
	// A typo must be reported, not silently treated as "off". Someone who
	// misspells the mode believes they enabled containment.
	if _, ok := ParseSandboxMode("workspce"); ok {
		t.Error("a misspelled mode was accepted; the operator would think the sandbox was on")
	}
}
