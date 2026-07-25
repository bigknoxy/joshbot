package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// The re-exec helper.
//
// Landlock restricts the calling process irreversibly, and every process it
// later spawns. Applying it inside joshbot would therefore sandbox the agent
// itself — permanently, including the parts that legitimately read config,
// memory and sessions outside the workspace.
//
// So the shell tool re-execs its own binary with a hidden subcommand. That
// short-lived process restricts itself, runs the one command, and exits. The
// restriction dies with it.
//
// Go gives no pre-exec hook that would let us restrict only the child of a
// normal fork/exec (SysProcAttr cannot run arbitrary code between fork and
// exec), which is why a helper process is the mechanism rather than a
// convenience.

// SandboxHelperArg is the hidden subcommand name. Double underscore marks it
// as internal: it is not a user-facing command and is not listed in help.
const SandboxHelperArg = "__sandbox-exec"

// ErrSandboxHelperUsage is returned when the helper is invoked with the wrong
// argument shape, which means a bug on the calling side rather than user error.
var ErrSandboxHelperUsage = errors.New("sandbox helper: expected <workspace> <allow-network 0|1> <command>")

// RunSandboxHelper is the body of the hidden subcommand. It applies the
// sandbox to itself and then runs the command, returning the exit code to
// pass on.
//
// It fails closed: if containment was requested and cannot be applied, the
// command does not run. A sandbox that silently degrades into no sandbox is
// worse than none, because the operator believes they are protected.
func RunSandboxHelper(args []string) (int, error) {
	if len(args) != 3 {
		return 2, ErrSandboxHelperUsage
	}
	workspace := args[0]
	allowNet, err := strconv.ParseBool(args[1])
	if err != nil {
		return 2, fmt.Errorf("sandbox helper: bad allow-network value %q: %w", args[1], err)
	}
	command := args[2]

	if !SandboxAvailable() {
		return 2, fmt.Errorf("sandbox helper: %s", SandboxDescription())
	}
	if !SandboxSupported() {
		return 2, fmt.Errorf("sandbox helper: %s is built in but the running kernel does not provide it; "+
			"refusing to run the command unconfined after containment was requested", SandboxDescription())
	}

	// Create the private scratch directory before restricting, since afterwards
	// its parent may be the only writable place and creating it would be
	// subject to the very rules we are about to install.
	tmpDir := SandboxTempDir(workspace)
	if tmpDir != "" {
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			return 2, fmt.Errorf("sandbox helper: create scratch dir: %w", err)
		}
		os.Setenv("TMPDIR", tmpDir)
	}

	policy := DefaultSandboxPolicy(workspace)
	policy.AllowNetwork = allowNet
	if err := ApplySandbox(policy); err != nil {
		return 2, fmt.Errorf("sandbox helper: %w", err)
	}

	// From here the process is confined, and so is everything it spawns.
	//
	// Yes, this is `sh -c` on an untrusted string. That is the shell tool's
	// entire purpose — the command IS the payload, and passing argv directly
	// would not implement the feature. It is also exactly why this file
	// exists: an arbitrary command string cannot be made safe by screening
	// its text, so the kernel refuses the access instead. The confinement
	// above is the mitigation; it is applied before this line for that reason.
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if workspace != "" {
		if _, statErr := os.Stat(workspace); statErr == nil {
			cmd.Dir = workspace
		}
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}
