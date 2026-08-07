package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OS-level containment for shell commands.
//
// The deny list in shell_deny.go screens command text. It cannot be made
// sound: it has to predict every dangerous shape an attacker might write, and
// the attacker only has to find one it did not predict. This is the boundary
// that does not depend on predicting anything — the kernel refuses the access
// regardless of how the command was spelled.
//
// Linux only, via Landlock. Everything else gets a no-op that says so rather
// than pretending. See sandbox_linux.go and sandbox_other.go.

// SandboxMode selects how much containment a spawned command gets.
type SandboxMode string

const (
	// SandboxOff runs commands with the process's own access. This is the
	// default: turning containment on changes what existing setups can do,
	// so it is opt-in until an operator says otherwise.
	SandboxOff SandboxMode = "off"

	// SandboxWorkspace confines filesystem writes to the workspace and a
	// small set of caches, allows reads of system directories, and denies
	// outbound network by default.
	SandboxWorkspace SandboxMode = "workspace"
)

// ParseSandboxMode maps a config string to a mode, defaulting to off for
// anything unrecognised so a typo cannot silently disable containment an
// operator asked for — an unknown value returns ok=false and the caller
// reports it.
func ParseSandboxMode(s string) (SandboxMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "false", "none", "disabled":
		return SandboxOff, true
	case "workspace", "on", "true":
		return SandboxWorkspace, true
	default:
		return SandboxOff, false
	}
}

// SandboxPolicy is the concrete access a sandboxed command gets.
type SandboxPolicy struct {
	Mode SandboxMode
	// ReadOnlyPaths and ReadWritePaths are directories. Missing paths are
	// skipped rather than failing: a ruleset naming a nonexistent directory
	// would otherwise make the whole sandbox refuse to apply.
	ReadOnlyPaths  []string
	ReadWritePaths []string
	// ReadWriteFiles are individual files, chiefly device nodes. Without
	// /dev/null in particular, essentially every command fails.
	ReadWriteFiles []string
	// AllowNetwork permits outbound TCP. Off by default: exfiltrating a
	// secret is the payoff for most attacks that get this far, and a
	// filesystem-only sandbox does nothing to stop it.
	AllowNetwork bool
}

// DefaultSandboxPolicy builds the policy for a workspace-confined command.
//
// The read-only set covers what a shell needs to run programs at all. The
// read-write set covers the workspace plus the build caches that make the tool
// useful — a sandbox that cannot run `go build` would just be turned off.
// Notably absent: the whole of $HOME. SSH keys, cloud credentials and
// joshbot's own config are unreachable because nothing grants them.
func DefaultSandboxPolicy(workspace string) SandboxPolicy {
	p := SandboxPolicy{
		Mode: SandboxWorkspace,
		ReadOnlyPaths: []string{
			"/usr", "/bin", "/sbin", "/lib", "/lib64", "/lib32",
			"/etc", "/opt", "/proc", "/sys/devices/system/cpu",
		},
		ReadWriteFiles: []string{
			"/dev/null", "/dev/zero", "/dev/full",
			"/dev/random", "/dev/urandom", "/dev/tty",
		},
	}

	if workspace != "" {
		p.ReadWritePaths = append(p.ReadWritePaths, workspace)
	}

	// Deliberately NOT os.TempDir(). /tmp is shared and world-writable, so
	// granting it wholesale hands the command every other process's temp
	// files — including anything sensitive that lands there — which defeats
	// the point of confining reads at all. Commands get a private scratch
	// directory instead, and TMPDIR is pointed at it. See SandboxTempDir.
	if tmp := SandboxTempDir(workspace); tmp != "" {
		p.ReadWritePaths = append(p.ReadWritePaths, tmp)
	}

	// Build caches, so the common commands still work. These are writable
	// because the toolchains write to them; they hold no credentials.
	for _, env := range []string{"GOCACHE", "GOMODCACHE", "CARGO_HOME", "npm_config_cache"} {
		if v := os.Getenv(env); v != "" {
			p.ReadWritePaths = append(p.ReadWritePaths, v)
		}
	}
	// Toolchain installs, read-only.
	for _, env := range []string{"GOROOT", "RUSTUP_HOME", "JAVA_HOME", "PYENV_ROOT", "NVM_DIR"} {
		if v := os.Getenv(env); v != "" {
			p.ReadOnlyPaths = append(p.ReadOnlyPaths, v)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		// Default cache locations when the env vars are unset. Granting
		// ~/.cache is deliberate and bounded; granting ~ would not be.
		p.ReadWritePaths = append(p.ReadWritePaths,
			filepath.Join(home, ".cache"),
			filepath.Join(home, "go", "pkg", "mod"),
		)
	}

	return p
}

// sandboxPreflight reports why containment cannot be enforced, or nil if it
// can.
//
// Both the shell tool and the helper must refuse rather than run unconfined
// when a sandbox was asked for and cannot be delivered, so the decision lives
// in one place. Taking the two capability answers as parameters keeps it
// testable on any machine — otherwise the refusal paths could only be
// exercised on a kernel that happens to lack Landlock.
func sandboxPreflight(mode SandboxMode, available, supported bool) error {
	// The zero value of SandboxMode is "", not SandboxOff. A bare-constructed
	// ShellTool (no SetSandbox / NewShellToolFromConfig) leaves the field empty,
	// and empty must mean off — otherwise the tool takes the sandbox path and
	// refuses on any platform without Landlock (e.g. macOS). Treat "" as off
	// here to match ParseSandboxMode, which already normalizes "" to off.
	if mode == SandboxOff || mode == "" {
		return nil
	}
	if !available {
		return fmt.Errorf("shell sandbox is set to %q but %s; set it to \"off\" to run without containment",
			mode, SandboxDescription())
	}
	if !supported {
		return fmt.Errorf("shell sandbox is set to %q but the running kernel does not provide %s; "+
			"refusing to run unconfined", mode, SandboxDescription())
	}
	return nil
}

// SandboxTempDir returns the private scratch directory a sandboxed command
// gets as its TMPDIR.
//
// Plenty of ordinary commands need somewhere to write temporary files, so
// refusing that outright would make the sandbox unusable. Giving them a
// directory of their own — inside the workspace they can already write —
// serves the same purpose without exposing the shared /tmp.
func SandboxTempDir(workspace string) string {
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, ".joshbot-tmp")
}

// existingPaths filters out paths that are not present, so a policy naming a
// directory this machine does not have still applies.
func existingPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
