// Package tools provides MCP tool integration for joshbot's agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/mcp"
)

// MCPToolWrapper wraps MCP tools as joshbot tools.
type MCPToolWrapper struct {
	manager *mcp.Manager
}

// NewMCPToolWrapper creates a new MCP tool wrapper.
func NewMCPToolWrapper(manager *mcp.Manager) *MCPToolWrapper {
	return &MCPToolWrapper{
		manager: manager,
	}
}

// RegisterTools registers all MCP tools with the given registry.
func (w *MCPToolWrapper) RegisterTools(registry *Registry) {
	if w.manager == nil {
		return
	}

	tools := w.manager.GetAllTools()
	for _, tool := range tools {
		qualifiedName := tool.ServerName + "_" + tool.Name
		w.registerSingleTool(registry, qualifiedName, tool)
	}
}

// registerSingleTool registers a single MCP tool.
func (w *MCPToolWrapper) registerSingleTool(registry *Registry, qualifiedName string, tool mcp.MCPTool) {
	registry.Register(&MCPToolAdapter{
		qualifiedName: qualifiedName,
		tool:          tool,
		manager:       w.manager,
	})
}

// MCPToolAdapter adapts an MCP tool to joshbot's Tool interface.
type MCPToolAdapter struct {
	qualifiedName string
	tool          mcp.MCPTool
	manager       *mcp.Manager
}

// Name returns the tool name.
func (t *MCPToolAdapter) Name() string {
	return t.qualifiedName
}

// Description returns the tool description.
func (t *MCPToolAdapter) Description() string {
	return fmt.Sprintf("[MCP: %s] %s", t.tool.ServerName, t.tool.Description)
}

// Parameters returns the tool's input schema as joshbot Parameters.
func (t *MCPToolAdapter) Parameters() []Parameter {
	if len(t.tool.InputSchema) == 0 {
		return []Parameter{}
	}

	// Parse the JSON schema and convert to Parameters
	var schema struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(t.tool.InputSchema, &schema); err != nil {
		return []Parameter{}
	}

	params := make([]Parameter, 0, len(schema.Properties))
	for name, prop := range schema.Properties {
		propMap, ok := prop.(map[string]any)
		if !ok {
			continue
		}

		param := Parameter{
			Name:        name,
			Description: fmt.Sprintf("%v", propMap["description"]),
		}

		// Check if required
		for _, req := range schema.Required {
			if req == name {
				param.Required = true
				break
			}
		}

		// Map JSON schema type to Parameter type
		switch propMap["type"] {
		case "string":
			param.Type = ParamString
		case "number", "integer":
			param.Type = ParamInteger
		case "boolean":
			param.Type = ParamBoolean
		case "array":
			param.Type = ParamString
		case "object":
			param.Type = ParamString
		default:
			param.Type = ParamString
		}

		params = append(params, param)
	}

	return params
}

// Execute calls the MCP tool and returns the result.
func (t *MCPToolAdapter) Execute(ctxArg interface{}, args map[string]any) ToolResult {
	ctx, ok := ctxArg.(context.Context)
	if !ok {
		ctx = context.Background()
	}

	result, err := t.manager.CallTool(ctx, t.qualifiedName, args)
	if err != nil {
		return ToolResult{
			Output: fmt.Sprintf("MCP tool error: %s", err.Error()),
			Error:  err,
		}
	}

	// Format the result
	output := ""
	for _, content := range result.Content {
		switch content.Type {
		case "text":
			output += content.Text
		case "image":
			output += fmt.Sprintf("[Image data]")
		default:
			output += content.Text
		}
	}

	if result.IsError {
		return ToolResult{
			Output: fmt.Sprintf("MCP tool returned error: %s", output),
			Error:  fmt.Errorf("MCP tool error: %s", output),
		}
	}

	return ToolResult{Output: output}
}

// MCPStatusTool reports the status of MCP server connections.
type MCPStatusTool struct {
	manager *mcp.Manager
}

// Name returns the tool name.
func (t *MCPStatusTool) Name() string {
	return "mcp_status"
}

// Description returns the tool description.
func (t *MCPStatusTool) Description() string {
	return "Check the status of connected MCP servers and available tools"
}

// Parameters returns the tool's input schema.
func (t *MCPStatusTool) Parameters() []Parameter {
	return []Parameter{}
}

// Execute returns MCP server status.
func (t *MCPStatusTool) Execute(ctxArg interface{}, args map[string]any) ToolResult {
	if t.manager == nil {
		return ToolResult{Output: "No MCP manager configured"}
	}

	servers := t.manager.ServerNames()
	if len(servers) == 0 {
		return ToolResult{Output: "No MCP servers configured. Add servers to config.json under tools.mcp.servers"}
	}

	output := "MCP Servers:\n"
	for _, name := range servers {
		output += fmt.Sprintf("- %s (connected)\n", name)
	}

	tools := t.manager.GetAllTools()
	output += fmt.Sprintf("\nTotal tools available: %d\n", len(tools))
	for _, tool := range tools {
		output += fmt.Sprintf("- %s_%s: %s\n", tool.ServerName, tool.Name, tool.Description)
	}

	return ToolResult{Output: output}
}

// SetupMCPTools creates and registers MCP tools based on config.
func SetupMCPTools(registry *Registry, servers []config.MCPServerConfig) error {
	if len(servers) == 0 {
		return nil
	}

	manager := mcp.NewManager()
	ctx := context.Background()

	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		if err := manager.AddServer(ctx, srv.Name, srv.URL, nil); err != nil {
			// Log warning but continue with other servers
			continue
		}
	}

	wrapper := NewMCPToolWrapper(manager)
	wrapper.RegisterTools(registry)

	// Always register status tool if any servers were added
	if len(manager.ServerNames()) > 0 {
		registry.Register(&MCPStatusTool{manager: manager})
	}

	return nil
}
