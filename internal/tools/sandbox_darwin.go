//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// macOS OS-level containment, via Seatbelt (sandbox-exec).
//
// This mirrors the Linux/Landlock design: the same DefaultSandboxPolicy — the
// workspace and its scratch dir writable, build caches writable, system
// directories read-only, $HOME and its credentials unreachable, outbound TCP
// denied by default — expressed as a Sandbox Profile Language (SBPL) policy and
// applied by wrapping the command in `sandbox-exec`.
//
// Unlike Landlock, Seatbelt does not restrict the calling process, so there is
// no re-exec helper here: sandbox-exec forks a confined child directly. The
// profile is deny-by-default; anything not granted is refused by the kernel no
// matter how the command is spelled, which is the whole point — the deny list
// in shell_deny.go cannot be made sound, and this boundary does not depend on
// predicting the command's shape.

// sandboxExecBinary is the system tool that applies a Seatbelt profile. It is
// present on every supported macOS release.
const sandboxExecBinary = "/usr/bin/sandbox-exec"

// SandboxAvailable reports whether this build can enforce anything.
func SandboxAvailable() bool { return true }

// SandboxSupported reports whether the running system can enforce the sandbox.
//
// Seatbelt is a core part of macOS, but sandbox-exec is the specific interface
// used here, so its presence is what determines enforceability.
func SandboxSupported() bool {
	_, err := exec.LookPath(sandboxExecBinary)
	return err == nil
}

// SandboxDescription is used in logs and errors so an operator can tell
// enforcement from a no-op.
func SandboxDescription() string { return "seatbelt (macOS sandbox-exec)" }

// ApplySandbox is not the mechanism on macOS: Seatbelt is applied by wrapping
// the command in sandbox-exec (see newSandboxCommand), not by restricting the
// calling process the way Landlock does. It exists so the cross-platform
// sandbox_helper.go compiles; the re-exec helper is never used on macOS.
func ApplySandbox(p SandboxPolicy) error {
	if p.Mode == SandboxOff {
		return nil
	}
	return fmt.Errorf("ApplySandbox is not used on macOS; containment is applied via %s", sandboxExecBinary)
}

// newSandboxCommand builds the command that runs cmd under Seatbelt.
//
// The profile is passed on the command line with -p (no temp file), and the
// command runs through `sh -c` exactly as the unsandboxed path does. runCommand
// sets the working directory and points TMPDIR at the workspace scratch dir.
func newSandboxCommand(ctx context.Context, t *ShellTool, cmd, workingDir string) (*exec.Cmd, error) {
	ws := t.sandboxWorkspace(workingDir)
	policy := DefaultSandboxPolicy(ws)
	policy.AllowNetwork = t.allowNetwork

	profile := seatbeltProfile(policy)

	return exec.CommandContext(ctx, sandboxExecBinary,
		"-p", profile, "/bin/sh", "-c", cmd), nil
}

// macBaseReadPaths are the system directories a shell needs to read to run
// programs at all on macOS. They are read-only and hold no user credentials;
// $HOME is deliberately absent, so SSH keys, cloud credentials and joshbot's
// own config are unreachable because nothing grants them.
var macBaseReadPaths = []string{
	"/usr", "/bin", "/sbin", "/opt",
	"/System", "/Library", "/Applications",
	"/etc", "/private/etc", "/private/var/select", "/dev",
}

// seatbeltProfile renders a SandboxPolicy as an SBPL profile string.
//
// Reads are granted on the system base set plus every read-only and read-write
// path in the policy; writes are granted only on the read-write paths and the
// device files. Paths are resolved through symlinks first because Seatbelt
// matches against the real path — on macOS /tmp is a symlink to /private/tmp,
// so an unresolved rule would silently never match. Network is denied unless
// the policy allows it.
func seatbeltProfile(p SandboxPolicy) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	// Non-file operations a normal build/test/git command needs. These do not
	// touch the filesystem or network, which are the boundaries that matter
	// here, so granting them broadly keeps the sandbox usable without widening
	// what a command can read, write or reach.
	b.WriteString("(allow process*)\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow ipc*)\n")
	b.WriteString("(allow signal)\n")
	b.WriteString("(allow file-read-metadata)\n")
	// Reading the root directory entry itself is needed to traverse to any
	// absolute path; without it even resolving /private/tmp aborts.
	b.WriteString("(allow file-read* (literal \"/\"))\n")

	readPaths := append([]string{}, macBaseReadPaths...)
	readPaths = append(readPaths, p.ReadOnlyPaths...)
	// Read-write dirs must also be readable, mirroring Landlock's RWDirs.
	readPaths = append(readPaths, p.ReadWritePaths...)
	if subs := subpathRules(readPaths); subs != "" {
		b.WriteString("(allow file-read* " + subs + ")\n")
	}

	if subs := subpathRules(p.ReadWritePaths); subs != "" {
		b.WriteString("(allow file-write* " + subs + ")\n")
	}
	if lits := literalRules(append(p.ReadWriteFiles, "/dev/null", "/dev/tty", "/dev/dtracehelper")); lits != "" {
		b.WriteString("(allow file-write* " + lits + ")\n")
	}

	if p.AllowNetwork {
		b.WriteString("(allow network*)\n")
	} else {
		b.WriteString("(deny network*)\n")
	}

	return b.String()
}

// subpathRules renders existing, symlink-resolved directories as SBPL
// (subpath "...") clauses, skipping anything that is missing or duplicated.
func subpathRules(paths []string) string {
	return pathRules("subpath", paths)
}

// literalRules renders individual files as SBPL (literal "...") clauses.
func literalRules(paths []string) string {
	return pathRules("literal", paths)
}

func pathRules(kind string, paths []string) string {
	var parts []string
	seen := map[string]bool{}
	for _, raw := range existingPaths(paths) {
		resolved := raw
		if r, err := filepath.EvalSymlinks(raw); err == nil && r != "" {
			resolved = r
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		parts = append(parts, fmt.Sprintf("(%s \"%s\")", kind, sbplEscape(resolved)))
	}
	return strings.Join(parts, " ")
}

// sbplEscape escapes a path for inclusion in an SBPL double-quoted string.
func sbplEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}
