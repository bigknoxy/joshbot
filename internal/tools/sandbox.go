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

	// SandboxAuto asks for the strongest containment this platform can
	// actually enforce: workspace confinement where an OS backend exists,
	// and the allowlist-only boundary where one does not.
	//
	// It exists because "workspace" is not portable. Linux has Landlock and
	// macOS has Seatbelt; Windows has neither, and a Linux kernel older
	// than 5.13 has no Landlock either. Asking for "workspace" there is a
	// startup error, which is the right answer for a value the operator
	// typed — but it is the wrong answer for a default, because a default
	// that cannot start on Windows is not a default. "auto" is what the
	// shipped configuration uses; "workspace" stays an explicit request
	// that fails loudly rather than quietly running unconfined.
	SandboxAuto SandboxMode = "auto"
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
	case "auto":
		return SandboxAuto, true
	default:
		return SandboxOff, false
	}
}

// ResolveSandboxMode turns a requested mode into the one this machine can
// actually enforce.
//
// Only "auto" resolves to anything different: it becomes workspace
// confinement where the OS provides a backend and a working kernel, and off
// everywhere else — where the shell tool's allowlist-only fallback is the
// boundary instead. Every other mode is returned unchanged, so an explicit
// "workspace" still fails loudly rather than running unconfined.
func ResolveSandboxMode(mode SandboxMode) SandboxMode {
	return resolveSandboxMode(mode, SandboxAvailable(), SandboxSupported())
}

// resolveSandboxMode is ResolveSandboxMode with the capability answers
// supplied, so the resolution can be tested on any machine.
func resolveSandboxMode(mode SandboxMode, available, supported bool) SandboxMode {
	if mode != SandboxAuto {
		return mode
	}
	if available && supported {
		return SandboxWorkspace
	}
	return SandboxOff
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
	// "auto" is a request, not a mode: resolve it before deciding whether
	// containment can be delivered.
	mode = resolveSandboxMode(mode, available, supported)
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
