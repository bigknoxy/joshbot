package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/charmbracelet/log"
	"github.com/google/uuid"
)

// Registry manages tool registration and execution.
type Registry struct {
	mu              sync.RWMutex
	tools           map[string]Tool
	logger          interface{ Info(msg string, args ...any) }
	pendingAsync    map[string]*PendingAsync
	pendingMu       sync.RWMutex
	asyncCallbackCh chan AsyncResult
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

// Register adds a tool to the registry.
func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("cannot register nil tool")
	}

	name := tool.Name()
	if name == "" {
		return fmt.Errorf("tool name cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %s already registered", name)
	}

	r.tools[name] = tool

	if r.logger != nil {
		r.logger.Info("Registered tool", "name", name)
	}

	return nil
}

// Unregister removes a tool from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.tools, name)
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tool names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}

	return names
}

// Count returns the number of registered tools.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.tools)
}

// Execute runs a tool by name with the given arguments.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	tool, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("tool not found: %s", name)
	}

	result := tool.Execute(ctx, args)

	if result.Error != nil {
		return "", fmt.Errorf("tool execution failed: %w", result.Error)
	}

	return result.Output, nil
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
	opID := uuid.New().String()[:8]

	pending := &PendingAsync{
		ID:        opID,
		ToolName:  tool.Name(),
		Args:      args,
		StartedAt: time.Now(),
		Channel:   channel,
		ChatID:    chatID,
	}

	r.pendingMu.Lock()
	if r.pendingAsync == nil {
		r.pendingAsync = make(map[string]*PendingAsync)
	}
	r.pendingAsync[opID] = pending
	r.pendingMu.Unlock()

	go func() {
		defer func() {
			r.pendingMu.Lock()
			delete(r.pendingAsync, opID)
			r.pendingMu.Unlock()
		}()

		cb := func(result AsyncResult) {
			result.ToolName = tool.Name()
			result.Args = args
			result.Channel = channel
			result.ChatID = chatID

			if r.asyncCallbackCh != nil {
				select {
				case r.asyncCallbackCh <- result:
				default:
					if r.logger != nil {
						r.logger.Info("Async callback channel full, dropping result", "tool", tool.Name())
					}
				}
			}

			if callback != nil {
				callback(result)
			}
		}

		tool.ExecuteAsync(ctx, args, cb)
	}()

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

	delete(r.pendingAsync, id)
	return nil
}

// SetAsyncCallback sets the callback channel for async tool results.
func (r *Registry) SetAsyncCallback(ch chan AsyncResult) {
	r.asyncCallbackCh = ch
	if r.pendingAsync == nil {
		r.pendingAsync = make(map[string]*PendingAsync)
	}
}

// GetAsyncCallbackChannel returns the async callback channel.
func (r *Registry) GetAsyncCallbackChannel() chan AsyncResult {
	return r.asyncCallbackCh
}

// GetSchemas returns the tool schemas for LLM function calling.
func (r *Registry) GetSchemas() []providers.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	schemas := make([]providers.Tool, 0, len(r.tools))

	for _, tool := range r.tools {
		schemas = append(schemas, toolToProviderTool(tool))
	}

	return schemas
}

// toolToProviderTool converts a Tool to a providers.Tool.
func toolToProviderTool(tool Tool) providers.Tool {
	params := tool.Parameters()

	// Generate JSON schema from parameters
	properties := make(map[string]any)
	required := make([]string, 0)

	for _, p := range params {
		prop := map[string]any{
			"type":        string(p.Type),
			"description": p.Description,
		}

		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}

		if p.Default != nil {
			prop["default"] = p.Default
		}

		properties[p.Name] = prop

		if p.Required {
			required = append(required, p.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	schemaBytes, _ := json.Marshal(schema)

	return providers.Tool{
		Type: "function",
		Function: providers.FunctionDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  (*json.RawMessage)(&schemaBytes),
		},
	}
}

// GetToolDocs returns documentation for all registered tools.
func (r *Registry) GetToolDocs() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var docs strings.Builder

	docs.WriteString("# Available Tools\n\n")

	for name, tool := range r.tools {
		docs.WriteString(fmt.Sprintf("## %s\n\n", name))
		docs.WriteString(tool.Description())
		docs.WriteString("\n\n")

		params := tool.Parameters()
		if len(params) > 0 {
			docs.WriteString("### Parameters\n\n")
			docs.WriteString("| Name | Type | Required | Description |\n")
			docs.WriteString("|------|------|----------|-------------|\n")

			for _, p := range params {
				required := "No"
				if p.Required {
					required = "Yes"
				}
				docs.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
					p.Name, p.Type, required, p.Description))
			}
			docs.WriteString("\n")
		}
	}

	return docs.String()
}

// DefaultRegistry creates a registry with default tools.
func DefaultRegistry() *Registry {
	registry := NewRegistry()

	// Note: These would need proper configuration in production
	// For now, register empty tools that can be configured later

	return registry
}

// RegistryWithDefaults creates a registry with standard tools configured.
func RegistryWithDefaults(
	workspace string,
	restrictToWorkspace bool,
	execTimeout int,
	webTimeout int,
	messageSender MessageSender,
	shellAllowList []string,
	filesystemAllowedPaths []string,
) *Registry {
	registry := NewRegistry()

	// Filesystem tool
	fsTool := NewFilesystemToolFromConfig(FilesystemToolConfig{
		Workspace:    workspace,
		Restrict:     restrictToWorkspace,
		AllowedPaths: filesystemAllowedPaths,
	})
	if err := registry.Register(fsTool); err != nil {
		log.Error("failed to register filesystem tool", "error", err)
	}

	// Register filesystem operation aliases for LLMs that call them directly
	aliases := []struct {
		name string
		op   string
	}{
		{"read_file", "read_file"},
		{"write_file", "write_file"},
		{"edit_file", "edit_file"},
		{"list_dir", "list_dir"},
		{"glob", "glob"},
		{"grep", "grep"},
	}

	for _, alias := range aliases {
		if err := registry.Register(&filesystemAlias{fs: fsTool, name: alias.name, op: alias.op}); err != nil {
			log.Warn("failed to register filesystem alias", "name", alias.name, "error", err)
		}
	}

	// Shell tool
	shellTool := NewShellToolFromConfig(ShellToolConfig{
		Timeout:   0, // Will default in constructor
		Workspace: workspace,
		Restrict:  restrictToWorkspace,
		AllowList: shellAllowList,
	})
	_ = registry.Register(shellTool)

	// Web tool
	webTool := NewWebToolFromConfig(WebToolConfig{
		Timeout: 0, // Will default in constructor
	})
	_ = registry.Register(webTool)

	// Message tool (optional)
	if messageSender != nil {
		msgTool := NewMessageTool(messageSender)
		_ = registry.Register(msgTool)

		channelTool := NewChannelMessageTool(messageSender)
		_ = registry.Register(channelTool)
	}

	return registry
}
