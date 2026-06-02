package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bigknoxy/joshbot/internal/log"
)

// SubagentConfigTool lets the agent discover, list, get, and save subagent configs.
type SubagentConfigTool struct {
	mgr *SubagentConfigManager
}

// NewSubagentConfigTool creates a new SubagentConfigTool.
func NewSubagentConfigTool(mgr *SubagentConfigManager) *SubagentConfigTool {
	return &SubagentConfigTool{mgr: mgr}
}

func (t *SubagentConfigTool) Name() string {
	return "subagent_config"
}

func (t *SubagentConfigTool) Description() string {
	return "Manage subagent configuration profiles stored in YAML config files. Use this to list available agent profiles, get details of a specific profile, or save a new agent profile."
}

func (t *SubagentConfigTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "operation",
			Type:        ParamString,
			Description: "Operation to perform: 'list' (list all configs), 'get' (get a config by name), 'save' (create/update a config), 'discover' (re-scan config directory).",
			Required:    true,
			Enum:        []string{"list", "get", "save", "discover"},
		},
		{
			Name:        "name",
			Type:        ParamString,
			Description: "Name of the agent profile (required for 'get' and 'save' operations).",
		},
		{
			Name:        "config",
			Type:        ParamObject,
			Description: "Configuration object (required for 'save' operation). Fields: description, model, temperature, max_tokens, system_prompt, tools, skills, tags.",
		},
	}
}

func (t *SubagentConfigTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.mgr == nil {
		return ToolResult{Error: fmt.Errorf("subagent config manager not configured")}
	}

	operation, _ := args["operation"].(string)
	if operation == "" {
		return ToolResult{Error: fmt.Errorf("'operation' is required (list, get, save, discover)")}
	}

	switch operation {
	case "list":
		return t.handleList()
	case "get":
		return t.handleGet(args)
	case "save":
		return t.handleSave(args)
	case "discover":
		return t.handleDiscover()
	default:
		return ToolResult{Error: fmt.Errorf("unknown operation: %s (valid: list, get, save, discover)", operation)}
	}
}

func (t *SubagentConfigTool) handleList() ToolResult {
	configs := t.mgr.List()
	if len(configs) == 0 {
		return ToolResult{Output: "No subagent configs found. Use 'save' to create one."}
	}

	sort.Slice(configs, func(i, j int) bool {
		return configs[i].Name < configs[j].Name
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Subagent Configs (%d)\n\n", len(configs)))
	for _, cfg := range configs {
		sb.WriteString(fmt.Sprintf("- **%s**", cfg.Name))
		if cfg.Description != "" {
			sb.WriteString(fmt.Sprintf(": %s", cfg.Description))
		}
		sb.WriteString("\n")
		if cfg.Model != "" {
			sb.WriteString(fmt.Sprintf("  - Model: `%s`\n", cfg.Model))
		}
		sb.WriteString(fmt.Sprintf("  - Temperature: %.1f, Max tokens: %d\n", cfg.Temperature, cfg.MaxTokens))
		if len(cfg.Tools) > 0 {
			sb.WriteString(fmt.Sprintf("  - Tools: %s\n", strings.Join(cfg.Tools, ", ")))
		}
		if len(cfg.Skills) > 0 {
			sb.WriteString(fmt.Sprintf("  - Skills: %s\n", strings.Join(cfg.Skills, ", ")))
		}
		if len(cfg.Tags) > 0 {
			sb.WriteString(fmt.Sprintf("  - Tags: %s\n", strings.Join(cfg.Tags, ", ")))
		}
	}
	return ToolResult{Output: sb.String()}
}

func (t *SubagentConfigTool) handleGet(args map[string]any) ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return ToolResult{Error: fmt.Errorf("'name' is required for 'get' operation")}
	}

	cfg, ok := t.mgr.Get(name)
	if !ok {
		return ToolResult{Error: fmt.Errorf("subagent config %q not found", name)}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Agent Config: %s\n\n", cfg.Name))
	if cfg.Description != "" {
		sb.WriteString(fmt.Sprintf("**Description:** %s\n\n", cfg.Description))
	}
	if cfg.SystemPrompt != "" {
		sb.WriteString(fmt.Sprintf("**System Prompt:**\n```\n%s\n```\n\n", cfg.SystemPrompt))
	}
	sb.WriteString(fmt.Sprintf("**Model:** `%s`\n", cfg.Model))
	sb.WriteString(fmt.Sprintf("**Temperature:** %.1f\n", cfg.Temperature))
	sb.WriteString(fmt.Sprintf("**Max Tokens:** %d\n", cfg.MaxTokens))
	if len(cfg.Tools) > 0 {
		sb.WriteString(fmt.Sprintf("**Tools:** %s\n", strings.Join(cfg.Tools, ", ")))
	}
	if len(cfg.Skills) > 0 {
		sb.WriteString(fmt.Sprintf("**Skills:** %s\n", strings.Join(cfg.Skills, ", ")))
	}
	if len(cfg.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("**Tags:** %s\n", strings.Join(cfg.Tags, ", ")))
	}

	return ToolResult{Output: sb.String()}
}

func (t *SubagentConfigTool) handleSave(args map[string]any) ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return ToolResult{Error: fmt.Errorf("'name' is required for 'save' operation")}
	}

	cfg := &SubagentConfig{Name: name}

	if configRaw, ok := args["config"]; ok {
		if configMap, ok := configRaw.(map[string]any); ok {
			if desc, ok := configMap["description"].(string); ok {
				cfg.Description = desc
			}
			if model, ok := configMap["model"].(string); ok {
				cfg.Model = model
			}
			if temp, ok := configMap["temperature"].(float64); ok {
				cfg.Temperature = temp
			}
			if maxTok, ok := configMap["max_tokens"].(float64); ok {
				cfg.MaxTokens = int(maxTok)
			}
			if sp, ok := configMap["system_prompt"].(string); ok {
				cfg.SystemPrompt = sp
			}
			if toolsRaw, ok := configMap["tools"].([]any); ok {
				for _, t := range toolsRaw {
					if s, ok := t.(string); ok {
						cfg.Tools = append(cfg.Tools, s)
					}
				}
			}
			if skillsRaw, ok := configMap["skills"].([]any); ok {
				for _, s := range skillsRaw {
					if str, ok := s.(string); ok {
						cfg.Skills = append(cfg.Skills, str)
					}
				}
			}
			if tagsRaw, ok := configMap["tags"].([]any); ok {
				for _, t := range tagsRaw {
					if s, ok := t.(string); ok {
						cfg.Tags = append(cfg.Tags, s)
					}
				}
			}
		}
	}

	if err := t.mgr.Save(cfg); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to save config %q: %w", name, err)}
	}

	log.Info("Saved subagent config", "name", name)
	return ToolResult{Output: fmt.Sprintf("Saved subagent config %q. Use 'get' with name=%q to view it.", name, name)}
}

func (t *SubagentConfigTool) handleDiscover() ToolResult {
	err := t.mgr.Discover()
	if err != nil {
		return ToolResult{Error: fmt.Errorf("discovery failed: %w", err)}
	}
	count := len(t.mgr.List())
	return ToolResult{Output: fmt.Sprintf("Discovered %d subagent config(s). Use 'list' to see them.", count)}
}
