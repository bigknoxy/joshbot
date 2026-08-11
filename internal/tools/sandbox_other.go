//go:build !linux && !darwin

package tools

import (
	"context"
	"fmt"
	"os/exec"
)

// No OS-level containment is implemented for this platform.
//
// The honest position is to say so. A silent no-op here would be worse than
// having no sandbox at all: an operator who set sandbox mode would believe
// commands were confined when nothing was enforcing it.

// SandboxAvailable reports whether this build can enforce anything.
func SandboxAvailable() bool { return false }

// SandboxSupported reports whether the running system enforces the sandbox.
func SandboxSupported() bool { return false }

// SandboxDescription is used in logs and errors so an operator can tell
// enforcement from a no-op.
func SandboxDescription() string { return "unavailable (not linux)" }

// ApplySandbox refuses rather than pretending. Callers decide whether that is
// fatal; the shell tool reports it and runs unconfined, which is the same
// behaviour as before the sandbox existed.
func ApplySandbox(p SandboxPolicy) error {
	if p.Mode == SandboxOff {
		return nil
	}
	return fmt.Errorf("sandbox mode %q requested but no OS-level containment is implemented on this platform", p.Mode)
}

// newSandboxCommand cannot build a contained command on a platform with no
// sandbox. It is never reached in practice — buildExecCmd runs sandboxPreflight
// first, which fails when SandboxAvailable() is false — but it exists so the
// package compiles and fails loudly if that ordering ever changes.
func newSandboxCommand(_ context.Context, _ *ShellTool, _, _ string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("no OS-level containment is implemented on this platform")
}
