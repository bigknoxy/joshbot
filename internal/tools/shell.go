package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// NewShellToolWithMaxOutput creates a new ShellTool with custom max output chars.
func NewShellToolWithMaxOutput(timeout time.Duration, workspace string, restrict bool, maxOutputChars int, allowList ...string) *ShellTool {
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
	if len(t.allowList) > 0 {
		allowed := false
		cmdTrimmed := strings.TrimSpace(cmd)
		for _, allowedCmd := range t.allowList {
			if cmdTrimmed == allowedCmd || strings.HasPrefix(cmdTrimmed, allowedCmd+" ") {
				allowed = true
				break
			}
		}
		if !allowed {
			return ToolResult{Error: fmt.Errorf("command not in allowlist: %s", cmdTrimmed)}
		}
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
// sandbox helper.
//
// When containment is on it fails rather than falling back to an unconfined
// run. An operator who switched the sandbox on and silently got no sandbox is
// worse off than one who never switched it on, because they would stop
// thinking about it.
func (t *ShellTool) buildExecCmd(ctx context.Context, cmd, workingDir string) (*exec.Cmd, error) {
	// "" is the zero value and means off (see sandboxPreflight); only a mode
	// that was explicitly set to something other than off takes the helper path.
	if t.sandbox == SandboxOff || t.sandbox == "" {
		return exec.CommandContext(ctx, "sh", "-c", cmd), nil
	}

	if err := sandboxPreflight(t.sandbox, SandboxAvailable(), SandboxSupported()); err != nil {
		return nil, err
	}

	helper := t.helperPath
	if helper == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("shell sandbox needs to re-exec this binary but its path could not be determined: %w", err)
		}
		helper = self
	}

	ws := workingDir
	if ws == "" {
		ws = t.workspace
	}

	return exec.CommandContext(ctx, helper, SandboxHelperArg,
		ws, strconv.FormatBool(t.allowNetwork), cmd), nil
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
	execCmd.Env = sanitizedEnv()

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

	// Check allowlist
	if len(t.allowList) > 0 {
		allowed := false
		cmdTrimmed := strings.TrimSpace(cmd)
		for _, allowedCmd := range t.allowList {
			if cmdTrimmed == allowedCmd || strings.HasPrefix(cmdTrimmed, allowedCmd+" ") {
				allowed = true
				break
			}
		}
		if !allowed {
			err := fmt.Errorf("command not in allowlist: %s", cmdTrimmed)
			callback(AsyncResult{Error: err})
			return ToolResult{Error: err}
		}
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
