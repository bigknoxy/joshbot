//go:build linux

package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/landlock-lsm/go-landlock/landlock"
)

// SandboxAvailable reports whether this build can enforce anything.
func SandboxAvailable() bool { return true }

// SandboxDescription is used in logs and errors so an operator can tell
// enforcement from a silent no-op.
func SandboxDescription() string { return "landlock (linux)" }

// ApplySandbox restricts the CALLING process, irreversibly, for the rest of
// its life — and every process it goes on to spawn.
//
// That is why this is called from the re-exec helper and never from joshbot's
// long-lived process: applying it there would sandbox the agent itself,
// permanently, including the parts that legitimately need to read config and
// memory outside the workspace.
//
// Landlock is deny-by-default over what the ruleset grants; anything not named
// here is refused by the kernel no matter how the command is written.
func ApplySandbox(p SandboxPolicy) error {
	if p.Mode == SandboxOff {
		return nil
	}

	rules := []landlock.Rule{}
	if ro := existingPaths(p.ReadOnlyPaths); len(ro) > 0 {
		rules = append(rules, landlock.RODirs(ro...))
	}
	if rw := existingPaths(p.ReadWritePaths); len(rw) > 0 {
		rules = append(rules, landlock.RWDirs(rw...))
	}
	if files := existingPaths(p.ReadWriteFiles); len(files) > 0 {
		rules = append(rules, landlock.RWFiles(files...))
	}
	if len(rules) == 0 {
		return fmt.Errorf("sandbox policy granted nothing; refusing to apply a ruleset that would block everything")
	}

	// V5 covers filesystem plus TCP restriction (network arrived at ABI v4).
	// BestEffort degrades on older kernels rather than failing outright —
	// see sandboxEffective for why that degradation must be reported.
	cfg := landlock.V5.BestEffort()

	if err := cfg.RestrictPaths(rules...); err != nil {
		return fmt.Errorf("restrict filesystem: %w", err)
	}

	if !p.AllowNetwork {
		// No ConnectTCP/BindTCP rules at all means deny every outbound TCP
		// connection. On a kernel below ABI v4 BestEffort drops this
		// silently, which is exactly the case sandboxEffective warns about.
		if err := cfg.RestrictNet(); err != nil {
			return fmt.Errorf("restrict network: %w", err)
		}
	}

	return nil
}

// SandboxSupported reports whether the running kernel has Landlock at all.
//
// This matters because BestEffort on a kernel without Landlock succeeds while
// restricting nothing — the difference between "contained" and "silently wide
// open" is invisible from the return value alone.
//
// The check reads the kernel's active LSM list rather than applying a probe
// ruleset: Landlock is irreversible, so any probe that actually called
// RestrictPaths would permanently restrict whichever process ran it.
func SandboxSupported() bool {
	data, err := os.ReadFile("/sys/kernel/security/lsm")
	if err != nil {
		// securityfs may not be mounted. Unknown, not proven absent.
		return false
	}
	for _, lsm := range strings.Split(strings.TrimSpace(string(data)), ",") {
		if lsm == "landlock" {
			return true
		}
	}
	return false
}
