# Plan: Async Tools

**Status:** ✅ Completed  
**Priority:** Medium  
**Estimated Effort:** 6-8 hours (~300 LOC)  
**Impact:** Medium (prevents timeouts, better UX for long-running ops)  
**Risk:** Low (additive change, existing behavior unchanged)

---

## Goal

Add async tool support to joshbot, allowing tools to execute in the background and notify the user when complete. This prevents timeout issues for long-running operations like data processing, backups, and monitoring.

---

## Background

### Current Behavior (Synchronous)

```
User: "Run the data processing pipeline"
  ↓
Tool: exec "python process_data.py"  (takes 10 minutes)
  ↓
User waits... and waits... and waits...
  ↓
TIMEOUT after 60 seconds
```

### Desired Behavior (Asynchronous)

```
User: "Run the data processing pipeline"
  ↓
Tool: exec "python process_data.py" (async)
  ↓
Immediate response: "Started processing data. I'll notify you when it's done."
  ↓
... 10 minutes later ...
  ↓
Callback: "Processing complete! Generated 1,234 records."
```

---

## Use Cases

1. **Long-running scripts** - Data processing, backups, builds
2. **Monitoring tasks** - Watch log files for errors over time
3. **Batch operations** - Process many files in parallel
4. **External service calls** - API requests that may be slow

---

## Implementation Design

### Core Concept

Tools can optionally implement `AsyncTool` interface. When detected, the agent:
1. Spawns a goroutine for tool execution
2. Returns immediate acknowledgment to user
3. Sends callback message when tool completes

### Key Components

1. **AsyncTool Interface** - Marker interface for async tools
2. **AsyncCallback** - Function signature for completion notifications
3. **AsyncResult** - Struct for tracking pending operations
4. **Message Bus Integration** - Route callbacks to correct channel

---

## Step-by-Step Implementation

### Step 1: Define async interfaces

**File:** `internal/tools/tool.go`

Add these new interfaces and types after the existing `Tool` interface:

```go
// AsyncCallback is called when an async tool completes.
type AsyncCallback func(result AsyncResult)

// AsyncResult contains the result of an async tool execution.
type AsyncResult struct {
    ToolName string         // Name of the tool that completed
    Args     map[string]any // Arguments passed to the tool
    Output   string         // Tool output
    Error    error          // Error if tool failed
    Metadata map[string]any // Additional metadata
}

// AsyncTool is an optional interface that tools can implement
// to indicate they support asynchronous execution.
type AsyncTool interface {
    Tool
    
    // IsAsync returns true if this execution should be async.
    // Tools can decide dynamically based on arguments.
    IsAsync(args map[string]any) bool
    
    // ExecuteAsync runs the tool in the background and calls the callback when done.
    // Should return immediately with a message to show the user.
    ExecuteAsync(ctx context.Context, args map[string]any, callback AsyncCallback) ToolResult
}

// PendingAsync tracks a pending async operation.
type PendingAsync struct {
    ID        string         // Unique identifier
    ToolName  string         // Tool being executed
    Args      map[string]any // Tool arguments
    StartedAt time.Time      // When it started
    Channel   string         // Channel to send callback to
    ChatID    string         // Chat ID for callback
}
```

### Step 2: Update Registry to support async tools

**File:** `internal/tools/registry.go`

Add async execution support:

```go
import (
    "context"
    "sync"
    "time"
    
    "github.com/google/uuid"
)

// Registry manages tool registration and execution.
type Registry struct {
    mu              sync.RWMutex
    tools           map[string]Tool
    logger          interface{ Info(msg string, args ...any) }
    pendingAsync    map[string]*PendingAsync // Track pending async operations
    pendingMu       sync.RWMutex
    asyncCallbackCh chan AsyncResult         // Channel for async results
}

// Option is a functional option for configuring the Registry.
type Option func(*Registry)

// WithLogger sets the logger for the registry.
func WithLogger(logger interface{ Info(msg string, args ...any) }) Option {
    return func(r *Registry) {
        r.logger = logger
    }
}

// WithAsyncSupport enables async tool execution.
func WithAsyncSupport(callbackCh chan AsyncResult) Option {
    return func(r *Registry) {
        r.asyncCallbackCh = callbackCh
        r.pendingAsync = make(map[string]*PendingAsync)
    }
}

// NewRegistry creates a new tool registry.
func NewRegistry(opts ...Option) *Registry {
    r := &Registry{
        tools: make(map[string]Tool),
    }
    
    for _, opt := range opts {
        opt(r)
    }
    
    return r
}

// ExecuteWithContext runs a tool with channel/chat context for callbacks.
func (r *Registry) ExecuteWithContext(
    ctx context.Context,
    name string,
    args map[string]any,
    channel, chatID string,
    asyncCallback func(AsyncResult),
) (ToolResult, bool) {
    tool, ok := r.Get(name)
    if !ok {
        return ToolResult{Error: fmt.Errorf("tool not found: %s", name)}, false
    }
    
    // Check if tool supports async
    if asyncTool, ok := tool.(AsyncTool); ok && asyncTool.IsAsync(args) {
        // Execute asynchronously
        return r.executeAsync(ctx, asyncTool, args, channel, chatID, asyncCallback)
    }
    
    // Execute synchronously
    result := tool.Execute(ctx, args)
    return result, false
}

// executeAsync runs an async tool in a goroutine.
func (r *Registry) executeAsync(
    ctx context.Context,
    tool AsyncTool,
    args map[string]any,
    channel, chatID string,
    callback func(AsyncResult),
) (ToolResult, bool) {
    // Generate unique ID for this operation
    opID := uuid.New().String()[:8]
    
    // Track pending operation
    pending := &PendingAsync{
        ID:        opID,
        ToolName:  tool.Name(),
        Args:      args,
        StartedAt: time.Now(),
        Channel:   channel,
        ChatID:    chatID,
    }
    
    r.pendingMu.Lock()
    r.pendingAsync[opID] = pending
    r.pendingMu.Unlock()
    
    // Execute in goroutine
    go func() {
        // Clean up when done
        defer func() {
            r.pendingMu.Lock()
            delete(r.pendingAsync, opID)
            r.pendingMu.Unlock()
        }()
        
        // Create callback wrapper
        cb := func(result AsyncResult) {
            result.ToolName = tool.Name()
            result.Args = args
            
            // Send to callback channel if configured
            if r.asyncCallbackCh != nil {
                select {
                case r.asyncCallbackCh <- result:
                default:
                    if r.logger != nil {
                        r.logger.Info("Async callback channel full, dropping result")
                    }
                }
            }
            
            // Call user callback if provided
            if callback != nil {
                callback(result)
            }
        }
        
        // Execute the async tool
        tool.ExecuteAsync(ctx, args, cb)
    }()
    
    // Return immediate result
    return ToolResult{
        Output: fmt.Sprintf("Started %s in background (ID: %s). I'll notify you when it's done.", tool.Name(), opID),
    }, true
}

// GetPendingAsync returns all pending async operations.
func (r *Registry) GetPendingAsync() []*PendingAsync {
    r.pendingMu.RLock()
    defer r.pendingMu.RUnlock()
    
    result := make([]*PendingAsync, 0, len(r.pendingAsync))
    for _, p := range r.pendingAsync {
        result = append(result, p)
    }
    return result
}

// CancelAsync cancels a pending async operation by ID.
func (r *Registry) CancelAsync(id string) error {
    r.pendingMu.Lock()
    defer r.pendingMu.Unlock()
    
    if _, ok := r.pendingAsync[id]; !ok {
        return fmt.Errorf("pending operation not found: %s", id)
    }
    
    // For now, just remove from tracking
    // In future, we could add context cancellation
    delete(r.pendingAsync, id)
    return nil
}
```

### Step 3: Implement async shell tool

**File:** `internal/tools/shell.go`

Update ShellTool to implement AsyncTool:

```go
import (
    "context"
    "errors"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "sync"
    "time"
)

// asyncThreshold is the duration after which commands are considered long-running.
const asyncThreshold = 30 * time.Second

// ShellTool provides shell execution capabilities.
type ShellTool struct {
    timeout        time.Duration
    workspace      string
    restrict       bool
    denyList       []string
    allowList      []string
    maxOutputChars int
    runningCmds    map[string]*exec.Cmd // Track running commands
    cmdMu          sync.Mutex
}

// IsAsync returns true if the command is likely to be long-running.
func (t *ShellTool) IsAsync(args map[string]any) bool {
    cmd, _ := args["command"].(string)
    if cmd == "" {
        return false
    }
    
    // Heuristics for long-running commands
    longRunningPatterns := []string{
        "python",          // Python scripts
        "npm run",         // npm scripts
        "make",            // Build commands
        "docker build",    // Docker builds
        "rsync",           // File sync
        "tar",             // Archiving
        "zip",             // Archiving
        "ffmpeg",          // Video processing
        "git clone",       // Git operations
        "wget",            // Downloads
        "curl -O",         // Downloads
        "sleep",           // Explicit waits
        "watch",           // Monitoring
        "tail -f",         // Log following
    }
    
    cmdLower := strings.ToLower(cmd)
    for _, pattern := range longRunningPatterns {
        if strings.Contains(cmdLower, strings.ToLower(pattern)) {
            return true
        }
    }
    
    // Check for explicit async flag
    if async, ok := args["async"].(bool); ok {
        return async
    }
    
    return false
}

// ExecuteAsync runs the shell command asynchronously.
func (t *ShellTool) ExecuteAsync(ctx context.Context, args map[string]any, callback AsyncCallback) ToolResult {
    cmd, _ := args["command"].(string)
    if cmd == "" {
        callback(AsyncResult{Error: errors.New("command is required")})
        return ToolResult{Error: errors.New("command is required")}
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
            callback(AsyncResult{Error: fmt.Errorf("command not in allowlist: %s", cmdTrimmed)})
            return ToolResult{Error: fmt.Errorf("command not in allowlist: %s", cmdTrimmed)}
        }
    }
    
    // Check deny list
    if denied := t.isDenied(cmd); denied != "" {
        callback(AsyncResult{Error: fmt.Errorf("command denied: %s", denied)})
        return ToolResult{Error: fmt.Errorf("command denied: %s", denied)}
    }
    
    // Get working directory
    workingDir := t.workspace
    if wd, ok := args["working_dir"].(string); ok && wd != "" {
        if filepath.IsAbs(wd) {
            if t.restrict && !isWithinBase(wd, t.workspace) {
                callback(AsyncResult{Error: fmt.Errorf("working directory outside workspace")})
                return ToolResult{Error: fmt.Errorf("working directory outside workspace")}
            }
            workingDir = wd
        } else {
            workingDir = filepath.Clean(filepath.Join(t.workspace, wd))
            if t.restrict && !isWithinBase(workingDir, t.workspace) {
                callback(AsyncResult{Error: fmt.Errorf("working directory outside workspace")})
                return ToolResult{Error: fmt.Errorf("working directory outside workspace")}
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
        // Create context with timeout
        execCtx, cancel := context.WithTimeout(context.Background(), timeout)
        defer cancel()
        
        // Execute command
        result := t.runCommand(execCtx, cmd, workingDir)
        
        // Prepare async result
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
        
        // Call callback
        callback(asyncResult)
    }()
    
    return ToolResult{
        Output: fmt.Sprintf("Started command in background: %s", cmd),
    }
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
        {
            Name:        "async",
            Type:        ParamBoolean,
            Description: "Run command asynchronously (for long-running commands)",
            Required:    false,
            Default:     false,
        },
    }
}
```

### Step 4: Update agent to handle async callbacks

**File:** `internal/agent/agent.go`

Add async callback handling:

```go
import (
    // ... existing imports ...
    "github.com/bigknoxy/joshbot/internal/tools"
)

// Agent represents the core AI agent.
type Agent struct {
    // ... existing fields ...
    
    // asyncCallbacks handles async tool callbacks
    asyncCallbacks chan tools.AsyncResult
}

// New creates a new Agent.
func New(cfg *config.Config, opts ...Option) (*Agent, error) {
    // ... existing initialization ...
    
    // Create async callback channel
    asyncCallbacks := make(chan tools.AsyncResult, 100)
    
    // Create tool registry with async support
    registry := tools.NewRegistry(
        tools.WithAsyncSupport(asyncCallbacks),
    )
    
    agent := &Agent{
        // ... existing fields ...
        asyncCallbacks: asyncCallbacks,
    }
    
    // Start callback processor
    go agent.processAsyncCallbacks()
    
    return agent, nil
}

// processAsyncCallbacks handles async tool completion notifications.
func (a *Agent) processAsyncCallbacks() {
    for result := range a.asyncCallbacks {
        // Format message for user
        var msg string
        if result.Error != nil {
            msg = fmt.Sprintf("❌ Background task failed (%s): %v", result.ToolName, result.Error)
        } else {
            // Truncate output if too long
            output := result.Output
            if len(output) > 2000 {
                output = output[:2000] + "... (truncated)"
            }
            msg = fmt.Sprintf("✅ Background task completed (%s):\n%s", result.ToolName, output)
        }
        
        // Create outbound message
        // We need to track which channel/chat to send to
        // This requires storing channel/chatID when starting async operation
        // For now, send to default channel
        
        // Publish to bus
        a.bus.Publish(bus.OutboundMessage{
            Content: msg,
            // Channel and ChatID would be set from pending operation
        })
    }
}

// ExecuteTool executes a tool with async support.
func (a *Agent) ExecuteTool(ctx context.Context, name string, args map[string]any, channel, chatID string) (string, bool, error) {
    result, isAsync := a.tools.ExecuteWithContext(ctx, name, args, channel, chatID, func(asyncResult tools.AsyncResult) {
        // This callback is called when async tool completes
        // Send notification to user
        var msg string
        if asyncResult.Error != nil {
            msg = fmt.Sprintf("❌ Background task failed (%s): %v", asyncResult.ToolName, asyncResult.Error)
        } else {
            output := asyncResult.Output
            if len(output) > 2000 {
                output = output[:2000] + "... (truncated)"
            }
            msg = fmt.Sprintf("✅ Background task completed (%s):\n%s", asyncResult.ToolName, output)
        }
        
        a.bus.Publish(bus.OutboundMessage{
            Channel: channel,
            ChatID:  chatID,
            Content: msg,
        })
    })
    
    if result.Error != nil {
        return "", isAsync, result.Error
    }
    
    return result.Output, isAsync, nil
}
```

### Step 5: Update ReAct loop for async tools

**File:** `internal/agent/agent.go`

Modify the reactLoop to handle async results:

```go
func (a *Agent) reactLoop(ctx context.Context, session *session.Session, msg InboundMessage, maxIter int) (string, error) {
    // ... existing loop logic ...
    
    for iteration := 1; iteration <= maxIter; iteration++ {
        // ... LLM call and tool call parsing ...
        
        // Execute tools
        for _, tc := range response.ToolCalls {
            output, isAsync, err := a.ExecuteTool(ctx, tc.Name, tc.Arguments, msg.Channel, msg.ChatID)
            
            if err != nil {
                // Handle error
                toolResults = append(toolResults, providers.Message{
                    Role:       providers.RoleTool,
                    ToolCallID: tc.ID,
                    Content:    fmt.Sprintf("Error: %v", err),
                })
                continue
            }
            
            if isAsync {
                // Async tool - add placeholder to messages
                toolResults = append(toolResults, providers.Message{
                    Role:       providers.RoleTool,
                    ToolCallID: tc.ID,
                    Content:    fmt.Sprintf("Task started in background. You will be notified when it completes."),
                })
            } else {
                // Sync tool - add result to messages
                toolResults = append(toolResults, providers.Message{
                    Role:       providers.RoleTool,
                    ToolCallID: tc.ID,
                    Content:    output,
                })
            }
        }
        
        // ... continue loop ...
    }
}
```

### Step 6: Add tests

**File:** `internal/tools/async_test.go`

```go
package tools

import (
    "context"
    "sync"
    "testing"
    "time"
)

// MockAsyncTool is a test implementation of AsyncTool
type MockAsyncTool struct {
    name       string
    isAsync    bool
    executeErr error
    output     string
    duration   time.Duration
}

func (m *MockAsyncTool) Name() string { return m.name }
func (m *MockAsyncTool) Description() string { return "mock async tool" }
func (m *MockAsyncTool) Parameters() []Parameter { return nil }

func (m *MockAsyncTool) Execute(ctx interface{}, args map[string]any) ToolResult {
    return ToolResult{Output: m.output}
}

func (m *MockAsyncTool) IsAsync(args map[string]any) bool {
    return m.isAsync
}

func (m *MockAsyncTool) ExecuteAsync(ctx context.Context, args map[string]any, callback AsyncCallback) ToolResult {
    go func() {
        time.Sleep(m.duration)
        
        result := AsyncResult{Output: m.output}
        if m.executeErr != nil {
            result.Error = m.executeErr
        }
        callback(result)
    }()
    return ToolResult{Output: "started"}
}

func TestRegistry_ExecuteAsync(t *testing.T) {
    callbackCh := make(chan AsyncResult, 1)
    registry := NewRegistry(WithAsyncSupport(callbackCh))
    
    mockTool := &MockAsyncTool{
        name:     "test_async",
        isAsync:  true,
        output:   "test output",
        duration: 100 * time.Millisecond,
    }
    
    if err := registry.Register(mockTool); err != nil {
        t.Fatalf("Failed to register tool: %v", err)
    }
    
    // Execute async
    result, isAsync := registry.ExecuteWithContext(context.Background(), "test_async", nil, "test", "test", nil)
    
    if !isAsync {
        t.Error("Expected async execution")
    }
    
    if result.Error != nil {
        t.Errorf("Unexpected error: %v", result.Error)
    }
    
    // Wait for callback
    select {
    case asyncResult := <-callbackCh:
        if asyncResult.Output != "test output" {
            t.Errorf("Output = %q, want %q", asyncResult.Output, "test output")
        }
    case <-time.After(1 * time.Second):
        t.Error("Timeout waiting for async callback")
    }
}

func TestRegistry_ExecuteSync(t *testing.T) {
    registry := NewRegistry()
    
    // Register a sync tool
    syncTool := &MockAsyncTool{
        name:    "test_sync",
        isAsync: false,
        output:  "sync output",
    }
    
    if err := registry.Register(syncTool); err != nil {
        t.Fatalf("Failed to register tool: %v", err)
    }
    
    // Execute sync
    result, isAsync := registry.ExecuteWithContext(context.Background(), "test_sync", nil, "test", "test", nil)
    
    if isAsync {
        t.Error("Expected sync execution")
    }
    
    if result.Output != "sync output" {
        t.Errorf("Output = %q, want %q", result.Output, "sync output")
    }
}

func TestShellTool_IsAsync(t *testing.T) {
    shell := NewShellTool(60*time.Second, "", false)
    
    tests := []struct {
        command  string
        expected bool
    }{
        {"echo hello", false},
        {"python script.py", true},
        {"npm run build", true},
        {"make all", true},
        {"docker build .", true},
        {"ls -la", false},
        {"tail -f /var/log/syslog", true},
    }
    
    for _, tt := range tests {
        args := map[string]any{"command": tt.command}
        got := shell.IsAsync(args)
        if got != tt.expected {
            t.Errorf("IsAsync(%q) = %v, want %v", tt.command, got, tt.expected)
        }
    }
}

func TestPendingAsyncTracking(t *testing.T) {
    callbackCh := make(chan AsyncResult, 10)
    registry := NewRegistry(WithAsyncSupport(callbackCh))
    
    mockTool := &MockAsyncTool{
        name:     "long_task",
        isAsync:  true,
        output:   "done",
        duration: 200 * time.Millisecond,
    }
    
    if err := registry.Register(mockTool); err != nil {
        t.Fatalf("Failed to register: %v", err)
    }
    
    // Start async operation
    _, _ = registry.ExecuteWithContext(context.Background(), "long_task", nil, "ch", "chat", nil)
    
    // Should have pending operation
    time.Sleep(10 * time.Millisecond) // Let goroutine start
    pending := registry.GetPendingAsync()
    if len(pending) != 1 {
        t.Errorf("Pending count = %d, want 1", len(pending))
    }
    
    // Wait for completion
    <-callbackCh
    
    // Should be removed after completion
    time.Sleep(10 * time.Millisecond)
    pending = registry.GetPendingAsync()
    if len(pending) != 0 {
        t.Errorf("Pending count = %d, want 0", len(pending))
    }
}
```

---

## Verification Steps

1. **Build and test:**
   ```bash
   go build ./...
   go test ./internal/tools/... -v
   go test ./internal/agent/... -v
   ```

2. **Manual test with long-running command:**
   ```bash
   joshbot agent -m "Run 'sleep 5 && echo done' in the background and tell me when it's done"
   ```
   
   Expected:
   - Immediate response: "Started command in background..."
   - After 5 seconds: "✅ Background task completed (shell): done"

3. **Test with script:**
   ```bash
   # Create a test script
   cat > /tmp/test_async.sh << 'EOF'
   #!/bin/bash
   echo "Starting..."
   sleep 10
   echo "Processing..."
   sleep 10
   echo "Done!"
   EOF
   chmod +x /tmp/test_async.sh
   
   joshbot agent -m "Run /tmp/test_async.sh in the background"
   ```

---

## Potential Issues

1. **Callback routing**: Need to ensure callbacks go to the correct channel/chat. Store this info when starting async operation.

2. **Process management**: Long-running shell processes should be cancellable. Consider adding context cancellation.

3. **Output buffering**: Very long outputs should be truncated or saved to file.

4. **Error handling**: Network failures during callback should be retried.

5. **Memory leaks**: Ensure pending operations are cleaned up even if tool crashes.

---

## Future Enhancements

1. **Progress updates**: Tools could send intermediate progress notifications
2. **Cancellation**: Allow users to cancel running async operations
3. **Status command**: `joshbot status --async` to list running operations
4. **Persistence**: Store async operations in session for recovery after restart

---

## Files Changed

| File | Changes |
|------|---------|
| `internal/tools/tool.go` | Add AsyncTool interface, AsyncResult, PendingAsync |
| `internal/tools/registry.go` | Add async execution support, callback handling |
| `internal/tools/shell.go` | Implement AsyncTool interface |
| `internal/tools/async_test.go` | Add unit tests for async execution |
| `internal/agent/agent.go` | Add callback processor, update ExecuteTool |

---

## Completion Checklist

- [x] Added AsyncTool interface
- [x] Added AsyncResult and PendingAsync types
- [x] Updated Registry with async support
- [x] Implemented IsAsync() for ShellTool
- [x] Implemented ExecuteAsync() for ShellTool
- [x] Added async parameter to shell tool
- [x] Updated Agent to handle callbacks
- [x] Added callback processor goroutine
- [x] Updated ReAct loop for async handling
- [x] Added unit tests
- [x] Verified build passes
- [x] Verified tests pass
- [x] Tested manually with long-running commands

---

## Progress Log

| Date | Status | Notes |
|------|--------|-------|
| 2026-03-03 | Not Started | Plan created |
| 2026-03-05 | Completed | Implementation done, tests pass, CLI callback handler added |
