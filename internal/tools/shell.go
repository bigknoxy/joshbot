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
	denyList       []string
	allowList      []string // If non-empty, only these commands are allowed
	maxOutputChars int      // Maximum characters to truncate output to
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
		denyList:       defaultDenyList(),
		allowList:      allowList,
		maxOutputChars: maxOutputChars,
	}
}

// defaultDenyList returns the default deny list for dangerous commands.
func defaultDenyList() []string {
	return []string{
		"rm -rf /",
		"rm -rf /*",
		"mkfs",
		"dd if=/dev/zero",
		":(){:|:&};:", // Fork bomb
		">/dev/sda",
		"chmod -R 777 /",
		"wget .* | sh",
		"curl .* | sh",
	}
}

// Name returns the name of the tool.
func (t *ShellTool) Name() string {
	return "shell"
}

// Description returns a description of the tool.
func (t *ShellTool) Description() string {
	desc := `Execute shell commands. Use this to run terminal commands, scripts, `
	desc += `and interact with the system. Commands are subject to safety restrictions. `
	desc += `Output is truncated to 4000 characters for large outputs.`
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
			Description: "The shell command to execute",
			Required:    true,
		},
		{
			Name:        "timeout",
			Type:        ParamInteger,
			Description: "Timeout in seconds (default: 60)",
			Required:    false,
			Default:     60,
		},
		{
			Name:        "working_dir",
			Type:        ParamString,
			Description: "Working directory for the command",
			Required:    false,
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

// isDenied checks if a command matches any deny list pattern.
func (t *ShellTool) isDenied(cmd string) string {
	cmdLower := strings.ToLower(cmd)

	// Check exact matches
	for _, pattern := range t.denyList {
		if strings.Contains(cmdLower, strings.ToLower(pattern)) {
			return pattern
		}
	}

	// Additional checks
	// Check for multiple rm -rf
	if strings.Count(cmdLower, "rm -rf") > 1 {
		return "multiple rm -rf"
	}

	// Check for piping to shell
	if (strings.Contains(cmdLower, "| sh") || strings.Contains(cmdLower, "| bash")) &&
		!strings.HasPrefix(cmdLower, "#") {
		return "pipe to shell"
	}

	// Check for background processes
	if strings.Contains(cmdLower, "&") && !strings.HasPrefix(cmdLower, "#") {
		// Allow some common background patterns
		allowed := []string{"&>", "&>>", "2>&1", "1>&2"}
		isAllowed := false
		for _, a := range allowed {
			if strings.Contains(cmdLower, a) {
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			return "background execution"
		}
	}

	return ""
}

// runCommand executes the command and returns the result.
func (t *ShellTool) runCommand(ctx context.Context, cmd, workingDir string) ToolResult {
	// Use shell -c to run the command
	execCmd := exec.CommandContext(ctx, "sh", "-c", cmd)

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

// ShellToolConfig holds configuration for the shell tool.
type ShellToolConfig struct {
	Timeout        time.Duration
	Workspace      string
	Restrict       bool
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
