package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/cron"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/skills"
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
	// A tool may supply a complete JSON Schema directly (e.g. MCP tools, whose
	// server-provided inputSchema would lose nested structure if flattened into
	// []Parameter). Prefer it when present and non-empty.
	var raw json.RawMessage
	if sp, ok := tool.(rawSchemaProvider); ok {
		if s := sp.RawSchema(); len(s) > 0 {
			raw = s
		}
	}
	if raw == nil {
		raw = json.RawMessage(GenerateSchema(tool.Parameters()))
	}

	return providers.Tool{
		Type: "function",
		Function: providers.FunctionDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  &raw,
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
// This returns an empty registry; use RegistryWithDefaults for pre-configured tools.
func DefaultRegistry() *Registry {
	return NewRegistry()
}

// registrySettings holds optional configuration for RegistryWithDefaults.
type registrySettings struct {
	sandbox            SandboxMode
	allowNetwork       bool
	approval           ApprovalMode
	cronService        *cron.Service
	cronDefaultChannel string
}

// RegistryOption adjusts optional registry behaviour. Options are used rather
// than more positional parameters, which this constructor already has enough of.
type RegistryOption func(*registrySettings)

// WithShellSandbox turns on OS-level containment for shell commands.
// allowNetwork permits outbound TCP from those commands.
func WithShellSandbox(mode SandboxMode, allowNetwork bool) RegistryOption {
	return func(s *registrySettings) {
		s.sandbox = mode
		s.allowNetwork = allowNetwork
	}
}

// WithShellApproval turns on the human-approval gate for shell commands. The
// approver itself rides the request context (WithApprover), so a turn nobody
// is watching — cron, heartbeat — is denied rather than blocked.
func WithShellApproval(mode ApprovalMode) RegistryOption {
	return func(s *registrySettings) {
		s.approval = mode
	}
}

// WithCronService registers the cron tool against a running scheduler.
// defaultChannel is where a reminder is delivered when the caller names none.
func WithCronService(svc *cron.Service, defaultChannel string) RegistryOption {
	return func(s *registrySettings) {
		s.cronService = svc
		s.cronDefaultChannel = defaultChannel
	}
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
	skillLoader *skills.Loader,
	opts ...RegistryOption,
) *Registry {
	settings := registrySettings{sandbox: SandboxOff, approval: ApprovalOff}
	for _, opt := range opts {
		opt(&settings)
	}

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
	shellTool.SetSandbox(settings.sandbox, settings.allowNetwork)
	shellTool.SetApproval(settings.approval)
	_ = registry.Register(shellTool)

	// Web tool
	webTool := NewWebToolFromConfig(WebToolConfig{
		Timeout: 0, // Will default in constructor
	})
	_ = registry.Register(webTool)

	// Register web operation aliases for LLMs that call them directly
	webAliases := []string{"web_search", "web_fetch", "web_code", "web_company", "web_research"}

	for _, aliasName := range webAliases {
		if err := registry.Register(&webAlias{web: webTool, name: aliasName}); err != nil {
			log.Warn("failed to register web alias", "name", aliasName, "error", err)
		}
	}

	// Message tool (optional)
	if messageSender != nil {
		msgTool := NewMessageTool(messageSender)
		_ = registry.Register(msgTool)

		channelTool := NewChannelMessageTool(messageSender)
		_ = registry.Register(channelTool)

		// Outbound media. Contained exactly like the filesystem tool: the
		// bytes leave the process, so "the agent may only send what it could
		// legitimately read" has to be enforced by the same walk, not by a
		// second, weaker path check.
		sendFileTool := NewSendFileTool(messageSender, FilesystemToolConfig{
			Workspace:    workspace,
			Restrict:     restrictToWorkspace,
			AllowedPaths: filesystemAllowedPaths,
		})
		_ = registry.Register(sendFileTool)
	}

	// Skill registry tool (optional)
	if skillLoader != nil {
		skillTool := NewSkillRegistryTool(skillLoader)
		_ = registry.Register(skillTool)
	}

	// Cron tool (optional): only offered when a scheduler is running, so the
	// agent is never told it can schedule something that nothing will deliver.
	if settings.cronService != nil {
		if err := registry.Register(NewCronTool(settings.cronService, settings.cronDefaultChannel)); err != nil {
			log.Error("failed to register cron tool", "error", err)
		}
	}

	return registry
}
