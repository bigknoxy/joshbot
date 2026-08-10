package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ShellTool provides shell execution capabilities.
type ShellTool struct {
	timeout        time.Duration
	workspace      string
	restrict       bool
	denyList       []string // Extra patterns, applied on top of the built-in rules
	allowList      []string // If non-empty, only these commands are allowed
	maxOutputChars int      // Maximum characters to truncate output to

	// sandbox controls OS-level containment of spawned commands. See
	// sandbox.go; SandboxOff reproduces the behaviour from before it existed.
	sandbox      SandboxMode
	allowNetwork bool
	// helperPath is the binary re-executed to apply the sandbox. Defaults to
	// this executable; tests point it at their own binary.
	helperPath string
}

// SetSandbox enables OS-level containment for spawned commands. allowNetwork
// permits outbound TCP, which is off by default because exfiltrating what was
// read is the usual payoff for an attack that reaches this far.
func (t *ShellTool) SetSandbox(mode SandboxMode, allowNetwork bool) {
	if mode == "" {
		mode = SandboxOff
	}
	t.sandbox = mode
	t.allowNetwork = allowNetwork
}

// NewShellTool creates a new ShellTool.
func NewShellTool(timeout time.Duration, workspace string, restrict bool, allowList ...string) *ShellTool {
	return NewShellToolWithMaxOutput(timeout, workspace, restrict, 4000, allowList...)
}

// defaultUnsandboxedAllowlist is the allowlist a shell tool falls back to when
// the build target has no OS-level containment at all (see
// NewShellToolWithMaxOutput). It is deliberately restricted to commands that
// read or inspect state and cannot, by themselves, execute an arbitrary second
// program — no shells, no general-purpose interpreters, no download tools.
//
// The deny list in shell_deny.go is provably incomplete (an interpreter can be
// made to spawn a shell, a script can be written then run), so on a platform
// where neither Landlock nor Seatbelt can back it up, an allowlist is the only
// boundary that is not trivially bypassable. Operators who need more configure
// tools.shell_allow_list explicitly, or run on a platform with a sandbox.
// Commands deliberately absent, each of which was on this list until it was
// noticed that it launches a program of the caller's choosing: `find` (-exec sh
// -c), `go` (go run, and go test's build directives), and `git` (git -c
// core.pager='sh -c …', git -c alias.x='!sh …'). An operator who needs them can
// still name them in tools.shell_allow_list, having chosen to.
// `rg` and `sort` were removed for the same property: `rg --pre PROGRAM` runs
// PROGRAM once per file, and `sort --compress-program=PROGRAM` execs it when
// the sort spills to disk.
//
// Screening flag *values* instead of removing the binary was considered and
// rejected: the boundary would then depend on knowing every exec-capable option
// of every tool, across GNU/BSD/busybox variants and future releases, and a
// boundary that has to be exhaustive to hold is not a boundary. Removal is
// shaped like the property being enforced.
//
// Every remaining entry was re-audited against the criterion: none of `ls`,
// `cat`, `pwd`, `echo`, `head`, `tail`, `wc`, `grep`, `uniq`, `diff`, `stat`,
// `file`, `date`, `whoami`, `uname`, `hostname`, `which`, `tree`, `du`, `df` or
// `gofmt` has an option that names a program to run. Anything added here must
// be checked the same way; TestDefaultUnsandboxedAllowlistHasNoExecutors pins
// the known escapes.
var defaultUnsandboxedAllowlist = []string{
	"ls", "cat", "pwd", "echo", "head", "tail", "wc", "grep",
	"uniq", "diff", "stat", "file", "date", "whoami", "uname",
	"hostname", "which", "tree", "du", "df", "gofmt",
}

// checkAllowList screens a command against the allowlist, returning an error
// when it may not run. An empty allowlist means no allowlist and permits
// everything (the deny list and, where available, the sandbox still apply).
//
// Two rules, in order. The command is passed to `sh -c` unchanged, so matching
// only the first word is not enough: `git --version; sh -c 'echo pwned'` and
// `echo hi; id` both begin with an allowed word and both run a second program
// the operator never allowed. Any construct that can introduce a new command
// word is therefore refused outright while an allowlist is in force. That is
// blunt — `ls | wc -l` is refused too — but this list is the sole boundary on
// platforms with no Landlock or Seatbelt, and a boundary that has to parse
// shell grammar correctly to hold is not a boundary.
func (t *ShellTool) checkAllowList(cmd string) error {
	if len(t.allowList) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(cmd)

	if construct := commandSeparator(trimmed); construct != "" {
		return fmt.Errorf("command not allowed: %q chains a second command or redirects I/O, "+
			"which is refused while an allowlist is in force; run one command per call", construct)
	}

	for _, allowed := range t.allowList {
		if trimmed == allowed || strings.HasPrefix(trimmed, allowed+" ") {
			return nil
		}
	}
	return fmt.Errorf("command not in allowlist: %s", trimmed)
}

// commandSeparators are the shell constructs that start a new command word or
// otherwise let the caller direct the command's effects somewhere the allowlist
// never approved. `$(`, backtick and the process-substitution forms are
// included because the shell runs their bodies too.
//
// Redirection belongs here even though it starts no new process. The command is
// handed to `sh -c` unchanged, so with an allowlist of read-only tools in force
// `echo PWNED > /etc/anything`, `echo key >> ~/.ssh/authorized_keys` and
// `cat < /etc/passwd` all passed the first-word check and then wrote or read
// wherever they liked — the allowlist's whole premise is that these commands
// cannot affect state outside what they are handed, and a redirection undoes
// that with one character. Matching `>` and `<` as substrings covers `>>`,
// `2>`, `&>`, `<<`, `<>` and friends, as well as the process-substitution forms
// listed separately for clarity.
//
// `\r` is listed because a bare carriage return terminates a command line for
// some shells and, more practically, is how a `\n` gets smuggled past a check
// that only knows about `\n`. `$'` is listed because $'...' ANSI-C quoting
// expands escapes — $'\x3b' is a semicolon the substring scan would never
// see.
var commandSeparators = []string{
	"$(", "<(", ">(", "$'",
	";", "&", "|", "\n", "\r", "`",
	">", "<",
}

// commandSeparator returns the first such construct found in cmd, or "" if
// there is none. It does not attempt to exempt quoted occurrences: `echo ";"`
// is refused, which costs a caller nothing and keeps the check from depending
// on a quoting parser matching the shell's exactly.
func commandSeparator(cmd string) string {
	for _, sep := range commandSeparators {
		if strings.Contains(cmd, sep) {
			return sep
		}
	}
	return ""
}

// NewShellToolWithMaxOutput creates a new ShellTool with custom max output chars.
func NewShellToolWithMaxOutput(timeout time.Duration, workspace string, restrict bool, maxOutputChars int, allowList ...string) *ShellTool {
	// On a platform with no OS-level containment available (anything that is
	// not Linux/Landlock or macOS/Seatbelt), the deny list is the sole boundary
	// and it is bypassable. When the operator has not supplied an explicit
	// allowlist, fall back to allowlist-only rather than trusting the deny list
	// alone. Platforms that do provide a sandbox (Linux, macOS) keep the
	// unrestricted default; containment there is opt-in via the sandbox.
	if len(allowList) == 0 && !SandboxAvailable() {
		allowList = defaultUnsandboxedAllowlist
	}
	return &ShellTool{
		timeout:        timeout,
		workspace:      workspace,
		restrict:       restrict,
		allowList:      allowList,
		maxOutputChars: maxOutputChars,
		// Explicitly off: the zero value of SandboxMode is "", and while "" is
		// treated as off everywhere it is read, initializing it here keeps a
		// bare-constructed tool off even if a future read forgets to normalize.
		sandbox: SandboxOff,
	}
}

// Name returns the name of the tool.
func (t *ShellTool) Name() string {
	return "shell"
}

// Description returns a description of the tool.
func (t *ShellTool) Description() string {
	desc := `Execute shell commands (builds, tests, git, scripts). Safety restrictions active. `
	desc += `Output truncated to 4000 chars.`
	if len(t.allowList) > 0 {
		desc += ` Only whitelisted commands are allowed.`
	}
	return desc
}

// Parameters returns the parameters for the tool.
func (t *ShellTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "command",
			Type:        ParamString,
			Description: "Command to run",
			Required:    true,
		},
		{
			Name:        "timeout",
			Type:        ParamInteger,
			Description: "Timeout in seconds",
			Required:    false,
			Default:     60,
		},
		{
			Name:        "working_dir",
			Type:        ParamString,
			Description: "Working directory",
			Required:    false,
		},
		{
			Name:        "async",
			Type:        ParamBoolean,
			Description: "Run asynchronously for long operations",
			Required:    false,
			Default:     false,
		},
	}
}

// Execute runs the shell command.
func (t *ShellTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	cmd, _ := args["command"].(string)

	if cmd == "" {
		return ToolResult{Error: errors.New("command is required")}
	}

	// Check allowlist first - if allowlist is set, only allow listed commands
	if err := t.checkAllowList(cmd); err != nil {
		return ToolResult{Error: err}
	}

	// Check for dangerous patterns
	if denied := t.isDenied(cmd); denied != "" {
		return ToolResult{Error: fmt.Errorf("command denied: potentially dangerous pattern detected (%s)", denied)}
	}

	// Get working directory
	workingDir := t.workspace
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		// Resolve working directory
		if filepath.IsAbs(wd) {
			if t.restrict && !isWithinBase(wd, t.workspace) {
				return ToolResult{Error: fmt.Errorf("working directory outside workspace not allowed")}
			}
			workingDir = wd
		} else {
			workingDir = filepath.Clean(filepath.Join(t.workspace, wd))
			if t.restrict && !isWithinBase(workingDir, t.workspace) {
				return ToolResult{Error: fmt.Errorf("working directory outside workspace not allowed")}
			}
		}
	}

	// Get timeout
	timeout := t.timeout
	if to, ok := args["timeout"].(float64); ok && to > 0 {
		timeout = time.Duration(to) * time.Second
	}

	// Create context with timeout
	execCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Execute command
	return t.runCommand(execCtx, cmd, workingDir)
}

// isDenied checks whether a command is too dangerous to run, returning a
// reason when it is. See shell_deny.go for how commands are screened.
func (t *ShellTool) isDenied(cmd string) string {
	return screen(cmd, t.denyList)
}

// buildExecCmd constructs the command to run, either directly or through the
// platform's OS-level containment (Landlock re-exec helper on Linux, the
// sandbox-exec Seatbelt wrapper on macOS).
//
// When containment is on it fails rather than falling back to an unconfined
// run. An operator who switched the sandbox on and silently got no sandbox is
// worse off than one who never switched it on, because they would stop
// thinking about it.
func (t *ShellTool) buildExecCmd(ctx context.Context, cmd, workingDir string) (*exec.Cmd, error) {
	// "" is the zero value and means off (see sandboxPreflight); only a mode
	// that was explicitly set to something other than off takes the sandbox path.
	if t.sandbox == SandboxOff || t.sandbox == "" {
		return exec.CommandContext(ctx, "sh", "-c", cmd), nil
	}

	if err := sandboxPreflight(t.sandbox, SandboxAvailable(), SandboxSupported()); err != nil {
		return nil, err
	}

	// newSandboxCommand is platform-specific: it wires up whatever containment
	// this OS provides. It is only reached once preflight has confirmed a
	// sandbox is available and supported here.
	return newSandboxCommand(ctx, t, cmd, workingDir)
}

// sandboxWorkspace resolves which directory the sandbox confines writes to for
// a given invocation: an explicit working dir if one was passed, else the
// tool's configured workspace.
func (t *ShellTool) sandboxWorkspace(workingDir string) string {
	if workingDir != "" {
		return workingDir
	}
	return t.workspace
}

// runCommand executes the command and returns the result.
func (t *ShellTool) runCommand(ctx context.Context, cmd, workingDir string) ToolResult {
	execCmd, err := t.buildExecCmd(ctx, cmd, workingDir)
	if err != nil {
		return ToolResult{Error: err}
	}

	// Hand the child a reduced environment. A nil Env would inherit this
	// process's, which includes every provider API key — readable with a bare
	// `env`, without touching the filesystem and without tripping any deny-list
	// rule. See shell_env.go.
	//
	// When the sandbox is active, point TMPDIR at the private scratch dir inside
	// the workspace and create it up front. The sandbox denies the shared system
	// temp (macOS /var/folders, /tmp), so a command that writes to $TMPDIR — go
	// build, git, and many others do — would otherwise fail. This mirrors what
	// the Linux re-exec helper does for itself. The scratch dir lives under the
	// workspace, which the sandbox already grants, so no extra grant is needed.
	if t.sandbox != SandboxOff && t.sandbox != "" {
		if scratch := SandboxTempDir(t.sandboxWorkspace(workingDir)); scratch != "" {
			_ = os.MkdirAll(scratch, 0o700)
			execCmd.Env = sanitizedEnv("TMPDIR=" + scratch)
		} else {
			execCmd.Env = sanitizedEnv()
		}
	} else {
		execCmd.Env = sanitizedEnv()
	}

	// Set working directory
	if workingDir != "" {
		// Verify the directory exists
		if _, err := os.Stat(workingDir); err != nil {
			return ToolResult{Error: fmt.Errorf("working directory does not exist: %w", err)}
		}
		execCmd.Dir = workingDir
	}

	// Capture output
	stdout, err := execCmd.StdoutPipe()
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to create stdout pipe: %w", err)}
	}

	stderr, err := execCmd.StderrPipe()
	if err != nil {
		return ToolResult{Error: fmt.Errorf("failed to create stderr pipe: %w", err)}
	}

	// Start execution
	if err := execCmd.Start(); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to start command: %w", err)}
	}

	// Read outputs
	output, err := readOutput(stdout)
	stderrOutput, _ := readOutput(stderr)

	// Wait for completion
	waitErr := execCmd.Wait()

	// Combine outputs
	var result strings.Builder

	// Check for context timeout
	if ctx.Err() == context.DeadlineExceeded {
		return ToolResult{Error: fmt.Errorf("command timed out after %v", t.timeout)}
	}

	if err != nil {
		result.WriteString(fmt.Sprintf("Error starting/reading output: %v\n", err))
	}

	if len(output) > 0 {
		result.WriteString("=== STDOUT ===\n")
		result.WriteString(output)
	}

	if len(stderrOutput) > 0 {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString("=== STDERR ===\n")
		result.WriteString(stderrOutput)
	}

	if waitErr != nil {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString(fmt.Sprintf("Exit error: %v", waitErr))
	}

	if result.Len() == 0 {
		result.WriteString("(command completed with no output)")
	}

	// Truncate output if it exceeds maxOutputChars
	outputStr := result.String()
	if len(outputStr) > t.maxOutputChars {
		truncated := outputStr[:t.maxOutputChars]
		suffix := fmt.Sprintf("\n... (truncated, %d chars total)", len(outputStr))
		outputStr = truncated + suffix
	}

	return ToolResult{Output: outputStr}
}

// readOutput reads all output from a pipe.
func readOutput(pipe interface{ Read([]byte) (int, error) }) (string, error) {
	buf := make([]byte, 1024)
	var result []byte

	for {
		n, err := pipe.Read(buf)
		if n > 0 {
			result = append(result, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	return string(result), nil
}

// longRunningPatterns are commands that typically take a long time.
var longRunningPatterns = []string{
	"python",
	"npm run",
	"yarn",
	"make",
	"docker build",
	"docker compose",
	"rsync",
	"tar",
	"zip",
	"ffmpeg",
	"git clone",
	"wget",
	"curl -O",
	"sleep",
	"watch",
	"tail -f",
	"npm install",
	"pip install",
	"go build",
	"go test",
	"cargo build",
	"mvn",
	"gradle",
}

// IsAsync returns true if the command is likely to be long-running.
func (t *ShellTool) IsAsync(args map[string]any) bool {
	// Check for explicit async flag
	if async, ok := args["async"].(bool); ok {
		return async
	}

	cmd, _ := args["command"].(string)
	if cmd == "" {
		return false
	}

	cmdLower := strings.ToLower(cmd)
	for _, pattern := range longRunningPatterns {
		if strings.Contains(cmdLower, strings.ToLower(pattern)) {
			return true
		}
	}

	return false
}

// ExecuteAsync runs the shell command asynchronously.
func (t *ShellTool) ExecuteAsync(ctx context.Context, args map[string]any, callback AsyncCallback) ToolResult {
	cmd, _ := args["command"].(string)
	if cmd == "" {
		err := errors.New("command is required")
		callback(AsyncResult{Error: err})
		return ToolResult{Error: err}
	}

	// Check allowlist — identical screening to Execute, via the same helper.
	if err := t.checkAllowList(cmd); err != nil {
		callback(AsyncResult{Error: err})
		return ToolResult{Error: err}
	}

	// Check deny list
	if denied := t.isDenied(cmd); denied != "" {
		err := fmt.Errorf("command denied: %s", denied)
		callback(AsyncResult{Error: err})
		return ToolResult{Error: err}
	}

	// Get working directory
	workingDir := t.workspace
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		if filepath.IsAbs(wd) {
			if t.restrict && !isWithinBase(wd, t.workspace) {
				err := fmt.Errorf("working directory outside workspace")
				callback(AsyncResult{Error: err})
				return ToolResult{Error: err}
			}
			workingDir = wd
		} else {
			workingDir = filepath.Clean(filepath.Join(t.workspace, wd))
			if t.restrict && !isWithinBase(workingDir, t.workspace) {
				err := fmt.Errorf("working directory outside workspace")
				callback(AsyncResult{Error: err})
				return ToolResult{Error: err}
			}
		}
	}

	// Get timeout
	timeout := t.timeout
	if to, ok := args["timeout"].(float64); ok && to > 0 {
		timeout = time.Duration(to) * time.Second
	}

	// Run in goroutine
	go func() {
		execCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		result := t.runCommand(execCtx, cmd, workingDir)

		asyncResult := AsyncResult{
			Metadata: map[string]any{
				"command":     cmd,
				"working_dir": workingDir,
			},
		}

		if result.Error != nil {
			asyncResult.Error = result.Error
			asyncResult.Output = fmt.Sprintf("Command failed: %v", result.Error)
		} else {
			asyncResult.Output = result.Output
		}

		callback(asyncResult)
	}()

	return ToolResult{
		Output: fmt.Sprintf("Started command in background: %s", cmd),
	}
}

// ShellToolConfig holds configuration for the shell tool.
type ShellToolConfig struct {
	Timeout   time.Duration
	Workspace string
	Restrict  bool
	// DenyList holds extra substring patterns to reject. They are matched
	// against the normalised command in addition to the built-in structural
	// rules, which cannot be switched off from configuration.
	DenyList       []string
	AllowList      []string
	MaxOutputChars int
}

// NewShellToolFromConfig creates a ShellTool from config.
func NewShellToolFromConfig(cfg ShellToolConfig) *ShellTool {
	workspace := cfg.Workspace
	if workspace == "" {
		workspace = os.Getenv("JOSHBOT_WORKSPACE")
		if workspace == "" {
			workspace = filepath.Join(os.Getenv("HOME"), ".joshbot", "workspace")
		}
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	maxOutputChars := cfg.MaxOutputChars
	if maxOutputChars == 0 {
		maxOutputChars = 4000
	}

	tool := NewShellToolWithMaxOutput(timeout, workspace, cfg.Restrict, maxOutputChars, cfg.AllowList...)

	if len(cfg.DenyList) > 0 {
		tool.denyList = cfg.DenyList
	}

	return tool
}
