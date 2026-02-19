// Package tools provides the tool system for joshbot's agent.
// It defines the base tool interface, parameter types, and implementations
// for filesystem, shell, web, and message tools.
package tools

import (
	"encoding/json"
	"fmt"
)

// ParameterType represents the type of a tool parameter.
type ParameterType string

const (
	// ParamString represents a string parameter.
	ParamString ParameterType = "string"
	// ParamInteger represents an integer parameter.
	ParamInteger ParameterType = "integer"
	// ParamNumber represents a number (float) parameter.
	ParamNumber ParameterType = "number"
	// ParamBoolean represents a boolean parameter.
	ParamBoolean ParameterType = "boolean"
	// ParamArray represents an array parameter.
	ParamArray ParameterType = "array"
	// ParamObject represents an object parameter.
	ParamObject ParameterType = "object"
)

// Parameter defines a single parameter for a tool.
type Parameter struct {
	Name        string        `json:"name"`
	Type        ParameterType `json:"type"`
	Description string        `json:"description"`
	Required    bool          `json:"required"`
	Default     any           `json:"default,omitempty"`
	Enum        []string      `json:"enum,omitempty"`
}

// Parameters returns the parameters as a JSON Schema.
func (p Parameter) Parameters() map[string]any {
	result := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"required":   []string{},
	}

	props := result["properties"].(map[string]any)

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

	props[p.Name] = prop

	if p.Required {
		result["required"] = append(result["required"].([]string), p.Name)
	}

	return result
}

// Tool is the interface that all tools must implement.
type Tool interface {
	// Name returns the name of the tool.
	Name() string

	// Description returns a description of what the tool does.
	Description() string

	// Parameters returns the parameter definitions for the tool.
	Parameters() []Parameter

	// Execute runs the tool with the given arguments.
	Execute(ctx interface{}, args map[string]any) ToolResult
}

// ToolResult represents the result of a tool execution.
type ToolResult struct {
	// Output is the result of the tool execution.
	Output string
	// Error is an error message if the tool execution failed.
	Error error
}

// String returns a string representation of the ToolResult.
func (r ToolResult) String() string {
	if r.Error != nil {
		return fmt.Sprintf("Error: %v", r.Error)
	}
	return r.Output
}

// GenerateSchema generates a JSON schema for a tool's parameters.
func GenerateSchema(params []Parameter) string {
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

	_bytes, err := json.Marshal(schema)
	if err != nil {
		return "{}"
	}
	return string(_bytes)
}
