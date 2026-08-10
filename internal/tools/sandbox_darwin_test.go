//go:build darwin

package tools

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests assert the kernel's refusal under Seatbelt, not the text of any
// command — the whole reason the sandbox exists is that command text cannot be
// screened soundly. They run the shell tool's real Execute path so buildExecCmd,
// the sandbox-exec wrapper and the TMPDIR handling are all exercised together.

func newSandboxedShell(t *testing.T, workspace string, allowNetwork bool) *ShellTool {
	t.Helper()
	tool := NewShellToolWithMaxOutput(30*time.Second, workspace, false, 1_000_000)
	tool.SetSandbox(SandboxWorkspace, allowNetwork)
	return tool
}

func runShell(t *testing.T, tool *ShellTool, command string) string {
	t.Helper()
	res := tool.Execute(nil, map[string]any{"command": command})
	if res.Error != nil {
		return "ERROR: " + res.Error.Error()
	}
	return res.Output
}

func TestSeatbelt_AvailableAndSupported(t *testing.T) {
	if !SandboxAvailable() {
		t.Fatal("expected Seatbelt to be available on darwin")
	}
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present on this host")
	}
}

func TestSeatbelt_WorkspaceWritableSystemReadable(t *testing.T) {
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present")
	}
	ws := t.TempDir()
	tool := newSandboxedShell(t, ws, false)

	out := runShell(t, tool, "echo contained > proof.txt && cat proof.txt && ls /usr/bin >/dev/null && echo SYSREAD_OK")
	if !strings.Contains(out, "contained") || !strings.Contains(out, "SYSREAD_OK") {
		t.Fatalf("expected a workspace write and a system read to succeed, got:\n%s", out)
	}
	// The file must really be in the workspace, proving the write landed.
	if _, err := os.Stat(filepath.Join(ws, "proof.txt")); err != nil {
		t.Fatalf("workspace file was not written: %v", err)
	}
}

func TestSeatbelt_ReadOutsideWorkspaceDenied(t *testing.T) {
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present")
	}
	// A secret in a directory the sandbox never grants. It sits under a
	// different temp subtree than the workspace, so nothing in the policy names
	// it.
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "id_rsa")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := newSandboxedShell(t, t.TempDir(), false)
	out := runShell(t, tool, "cat "+secret)
	if strings.Contains(out, "TOP-SECRET-KEY") {
		t.Fatalf("sandbox failed to contain a read outside the workspace; leaked secret:\n%s", out)
	}
	if !strings.Contains(out, "Operation not permitted") {
		t.Fatalf("expected the kernel to refuse the read, got:\n%s", out)
	}
}

func TestSeatbelt_WriteOutsideWorkspaceDenied(t *testing.T) {
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present")
	}
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "pwned")

	tool := newSandboxedShell(t, t.TempDir(), false)
	_ = runShell(t, tool, "echo x > "+target)
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("sandbox allowed a write outside the workspace: %s exists", target)
	}
}

// The bypass the deny list provably cannot catch: an interpreter re-execing a
// shell. The kernel contains it regardless.
func TestSeatbelt_InterpreterExecBypassContained(t *testing.T) {
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present")
	}
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("python3 not present")
	}
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "creds")
	if err := os.WriteFile(secret, []byte("LEAKME"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := newSandboxedShell(t, t.TempDir(), false)
	// python3 -c os.execv('/bin/sh', ...) — os.execv is not in shell_deny's
	// execSinks, so the deny list waves this through. The sandbox must not.
	cmd := `python3 -c "import os; os.execv('/bin/sh',['sh','-c','cat ` + secret + `'])"`
	out := runShell(t, tool, cmd)
	if strings.Contains(out, "LEAKME") {
		t.Fatalf("interpreter exec bypass leaked a secret past the sandbox:\n%s", out)
	}
}

func TestSeatbeltProfile_DenyNetworkByDefault(t *testing.T) {
	p := DefaultSandboxPolicy(t.TempDir())
	prof := seatbeltProfile(p)
	if !strings.Contains(prof, "(deny network*)") {
		t.Fatalf("default profile must deny network, got:\n%s", prof)
	}
	if !strings.Contains(prof, "(deny default)") {
		t.Fatalf("profile must be deny-by-default, got:\n%s", prof)
	}

	p.AllowNetwork = true
	if got := seatbeltProfile(p); !strings.Contains(got, "(allow network*)") {
		t.Fatalf("AllowNetwork profile must allow network, got:\n%s", got)
	}
}

func TestSeatbeltProfile_ResolvesSymlinks(t *testing.T) {
	// /tmp is a symlink to /private/tmp on macOS; the rule must name the real
	// path or Seatbelt silently never matches it.
	p := SandboxPolicy{Mode: SandboxWorkspace, ReadWritePaths: []string{"/tmp"}}
	prof := seatbeltProfile(p)
	if !strings.Contains(prof, "/private/tmp") {
		t.Fatalf("expected symlink-resolved /private/tmp in profile, got:\n%s", prof)
	}
}

// --- runtime network containment ---
//
// TestSeatbeltProfile_DenyNetworkByDefault only proves the rendered SBPL text
// contains "(deny network*)". A profile Seatbelt rejects, or one where a later
// clause overrides it, would still contain that substring — so the boundary was
// asserted against a string and not against the kernel. These tests fetch from
// a real local HTTP server under a real sandbox-exec, the way every other
// boundary in this file is proved.

// networkSentinelServer starts a loopback HTTP server serving a unique body.
func networkSentinelServer(t *testing.T) (url, sentinel string) {
	t.Helper()
	sentinel = "NETWORK-SENTINEL-9f3a"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sentinel))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, sentinel
}

func requireCurl(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/curl"); err != nil {
		t.Skip("/usr/bin/curl not present")
	}
}

func TestSeatbelt_NetworkDeniedAtRuntime(t *testing.T) {
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present")
	}
	requireCurl(t)
	url, sentinel := networkSentinelServer(t)

	tool := newSandboxedShell(t, t.TempDir(), false)
	out := runShell(t, tool, "/usr/bin/curl -s -m 5 "+url+" || echo FETCH_FAILED")
	if strings.Contains(out, sentinel) {
		t.Fatalf("sandbox with AllowNetwork=false fetched a URL; body leaked:\n%s", out)
	}
	if !strings.Contains(out, "FETCH_FAILED") && !strings.Contains(out, "ERROR:") {
		t.Fatalf("expected the fetch to fail, got:\n%s", out)
	}
}

func TestSeatbelt_NetworkAllowedAtRuntime(t *testing.T) {
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present")
	}
	requireCurl(t)
	url, sentinel := networkSentinelServer(t)

	// The positive control: the same fetch, same command, same sandbox, network
	// granted. Without this the denial test above would also pass if curl were
	// broken, the server unreachable, or the profile rejected outright.
	tool := newSandboxedShell(t, t.TempDir(), true)
	out := runShell(t, tool, "/usr/bin/curl -s -m 5 "+url)
	if !strings.Contains(out, sentinel) {
		t.Fatalf("sandbox with AllowNetwork=true failed to fetch %s, got:\n%s", url, out)
	}
}

// $HOME is granted by nothing in the profile (only two bounded cache dirs
// beneath it), so a file sitting in the home directory must be unreadable even
// though the command knows exactly where it is.
func TestSeatbelt_HomeUnreadableAtRuntime(t *testing.T) {
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	sentinel := filepath.Join(home, ".ssh_config_sentinel")
	if err := os.WriteFile(sentinel, []byte("HOME-SENTINEL-4c1b"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := newSandboxedShell(t, t.TempDir(), false)
	out := runShell(t, tool, "cat "+sentinel)
	if strings.Contains(out, "HOME-SENTINEL-4c1b") {
		t.Fatalf("sandbox read a file in $HOME:\n%s", out)
	}
	if !strings.Contains(out, "Operation not permitted") {
		t.Fatalf("expected the kernel to refuse the read, got:\n%s", out)
	}
}

// A workspace that does not exist yet must not silently produce a profile with
// no write grant; newSandboxCommand creates it (see the pathRules note there).
func TestSeatbelt_MissingWorkspaceIsCreatedNotDropped(t *testing.T) {
	if !SandboxSupported() {
		t.Skip("sandbox-exec not present")
	}
	ws := filepath.Join(t.TempDir(), "created", "later")
	tool := newSandboxedShell(t, ws, false)

	out := runShell(t, tool, "echo ok > w.txt && cat w.txt")
	if !strings.Contains(out, "ok") {
		t.Fatalf("a workspace created at command time must be writable, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(ws, "w.txt")); err != nil {
		t.Fatalf("write did not land in the workspace: %v", err)
	}
}

// The mach-lookup allowlist must stay an allowlist: a blanket grant reaches XPC
// services that act outside the profile.
func TestSeatbeltProfile_MachLookupIsAllowlisted(t *testing.T) {
	prof := seatbeltProfile(DefaultSandboxPolicy(t.TempDir()))
	if strings.Contains(prof, "(allow mach-lookup)\n") {
		t.Fatal("profile grants unrestricted mach-lookup")
	}
	if !strings.Contains(prof, `(global-name "com.apple.system.opendirectoryd.libinfo")`) {
		t.Fatalf("expected an allowlisted mach service, got:\n%s", prof)
	}
}
