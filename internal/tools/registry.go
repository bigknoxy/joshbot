package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// Registry manages tool registration and execution.
type Registry struct {
	mu     sync.RWMutex
	tools  map[string]Tool
	logger interface{ Info(msg string, args ...any) }
}

// Option is a functional option for configuring the Registry.
type Option func(*Registry)

// WithLogger sets the logger for the registry.
func WithLogger(logger interface{ Info(msg string, args ...any) }) Option {
	return func(r *Registry) {
		r.logger = logger
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
) *Registry {
	registry := NewRegistry()

	// Filesystem tool
	fsTool := NewFilesystemToolFromConfig(FilesystemToolConfig{
		Workspace: workspace,
		Restrict:  restrictToWorkspace,
	})
	_ = registry.Register(fsTool)

	// Shell tool
	shellTool := NewShellToolFromConfig(ShellToolConfig{
		Timeout:   0, // Will default in constructor
		Workspace: workspace,
		Restrict:  restrictToWorkspace,
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
