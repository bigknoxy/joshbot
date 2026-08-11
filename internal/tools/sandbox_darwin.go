//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
//
// Note that -p puts the whole policy — including the absolute workspace path —
// in the process argv, so any local user can read it with `ps`. The policy is
// not a secret (it grants nothing to whoever reads it) but the workspace path
// it discloses is; see SECURITY.md.
func newSandboxCommand(ctx context.Context, t *ShellTool, cmd, workingDir string) (*exec.Cmd, error) {
	ws := t.sandboxWorkspace(workingDir)

	// A path that does not exist yet is silently dropped from the profile (see
	// existingPaths), so a workspace created after startup — or never created
	// at all — would produce a profile with no write grant and every command
	// would fail with an obscure "Operation not permitted". Create it instead,
	// and report the real reason if that is impossible.
	if ws != "" {
		if err := os.MkdirAll(ws, 0o700); err != nil {
			return nil, fmt.Errorf("sandbox workspace %q cannot be created, so no write access could be granted: %w", ws, err)
		}
		if tmp := SandboxTempDir(ws); tmp != "" {
			if err := os.MkdirAll(tmp, 0o700); err != nil {
				return nil, fmt.Errorf("sandbox scratch dir %q cannot be created: %w", tmp, err)
			}
		}
	}

	policy := DefaultSandboxPolicy(ws)
	policy.AllowNetwork = t.allowNetwork
	if cache := goBuildCache(); cache != "" {
		policy.ReadWritePaths = append(policy.ReadWritePaths, cache)
	}

	profile := seatbeltProfile(policy)

	return exec.CommandContext(ctx, sandboxExecBinary,
		"-p", profile, "/bin/sh", "-c", cmd), nil
}

// goBuildCache returns the Go build cache directory to grant write access to.
//
// DefaultSandboxPolicy grants $GOCACHE, but on macOS that variable is almost
// never exported: the toolchain defaults it to ~/Library/Caches/go-build, which
// is *not* under ~/.cache and so was granted by nothing. The result was that
// `go build` under the sandbox failed on a stock machine. Ask the toolchain
// first and fall back to the documented default; the answer is resolved once
// per process because it costs a subprocess.
var goBuildCacheOnce struct {
	sync.Once
	path string
}

func goBuildCache() string {
	goBuildCacheOnce.Do(func() {
		if v := os.Getenv("GOCACHE"); v != "" {
			goBuildCacheOnce.path = v
			return
		}
		if gobin, err := exec.LookPath("go"); err == nil {
			out, err := exec.Command(gobin, "env", "GOCACHE").Output()
			if p := strings.TrimSpace(string(out)); err == nil && p != "" {
				goBuildCacheOnce.path = p
				return
			}
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			goBuildCacheOnce.path = filepath.Join(home, "Library", "Caches", "go-build")
		}
	})
	return goBuildCacheOnce.path
}

// macMachServices is the allowlist backing `(allow mach-lookup ...)`.
//
// Each entry is a bootstrap service name a plain shell/build/test command
// needs. Every one of these is a read-only or advisory service: none of them
// will open, write or fetch a resource on the caller's behalf, which is the
// property that makes allowlisting them safe while a blanket mach-lookup is
// not. Adding an entry is a security decision — justify it here.
var macMachServices = []string{
	// libinfo/Open Directory: getpwuid, getgrgid, user and group lookups. dyld
	// and the C library call these during process start; without them a
	// surprising number of tools abort before main().
	"com.apple.system.opendirectoryd.libinfo",
	"com.apple.system.opendirectoryd.membership",
	"com.apple.system.DirectoryService.libinfo_v1",
	"com.apple.system.DirectoryService.membership_v1",
	// Unified logging. Many system libraries log unconditionally on startup and
	// treat a failed lookup as fatal. Write-only sink; nothing is read back.
	"com.apple.system.logger",
	"com.apple.logd",
	"com.apple.diagnosticd",
	// Notification broadcast (notify(3)). Advisory only.
	"com.apple.system.notification_center",
	// Code-signature and trust evaluation, used when the loader validates a
	// binary and when TLS verifies a certificate chain. These only *evaluate*
	// material handed to them; they do not fetch files for the caller.
	"com.apple.trustd",
	"com.apple.trustd.agent",
	"com.apple.SecurityServer",
	"com.apple.SystemConfiguration.configd",
}

// machLookupRules renders macMachServices as SBPL (global-name "...") clauses.
func machLookupRules() string {
	parts := make([]string, 0, len(macMachServices))
	for _, s := range macMachServices {
		parts = append(parts, fmt.Sprintf("(global-name \"%s\")", sbplEscape(s)))
	}
	return strings.Join(parts, " ")
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
	// Non-file operations a normal build/test/git command needs.
	//
	// process-exec and process-fork are what let a shell run programs at all;
	// process-info* lets a command see its own and its children's state (ps,
	// wait, getrusage). The broader `(allow process*)` also covered
	// process-exec-interpreter and codesigning-status changes, which are not
	// needed here.
	b.WriteString("(allow process-exec)\n")
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow process-info*)\n")
	b.WriteString("(allow sysctl-read)\n")
	// mach-lookup is an ALLOWLIST, deliberately.
	//
	// An unrestricted `(allow mach-lookup)` is a known way to hollow out a
	// Seatbelt profile: XPC services reachable over the bootstrap namespace run
	// outside this profile and will act on the sandboxed process's behalf, so a
	// command that cannot open a file directly can often ask a daemon to do it.
	// That would undercut the deny-by-default file and network claims above.
	// Only the services a plain build/test/git command genuinely needs are
	// named here; anything else is refused by `(deny default)`.
	b.WriteString("(allow mach-lookup " + machLookupRules() + ")\n")
	// POSIX shared memory / semaphores. Unlike mach-lookup this does not reach
	// a service that runs outside the profile.
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
