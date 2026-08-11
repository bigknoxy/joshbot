package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
)

type ReloadProvidersFunc func() error

type ConfigureTool struct {
	cfg             *config.Config
	reloadProviders ReloadProvidersFunc
}

func NewConfigureTool(cfg *config.Config, reload ReloadProvidersFunc) *ConfigureTool {
	return &ConfigureTool{cfg: cfg, reloadProviders: reload}
}

func (t *ConfigureTool) Name() string {
	return "joshbot_config"
}

func (t *ConfigureTool) Description() string {
	return "Read or modify joshbot config: list models, switch model, adjust settings (temperature, max_tokens)."
}

func (t *ConfigureTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "operation",
			Type:        ParamString,
			Description: "Operation: list_models, status, switch_model, set, get",
			Required:    true,
			Enum:        []string{"list_models", "status", "switch_model", "set", "get"},
		},
		{
			Name:        "model",
			Type:        ParamString,
			Description: "Model name (required for switch_model)",
		},
		{
			Name:        "setting",
			Type:        ParamString,
			Description: "Setting name (get/set): temperature, max_tokens, model",
		},
		{
			Name:        "value",
			Type:        ParamString,
			Description: "Value for the setting (required for set)",
		},
	}
}

func (t *ConfigureTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.cfg == nil {
		return ToolResult{Error: fmt.Errorf("config not available")}
	}

	operation, _ := args["operation"].(string)
	if operation == "" {
		return ToolResult{Error: fmt.Errorf("'operation' is required")}
	}

	switch operation {
	case "list_models":
		return t.handleListModels()
	case "status":
		return t.handleStatus()
	case "switch_model":
		return t.handleSwitchModel(args)
	case "get":
		return t.handleGet(args)
	case "set":
		return t.handleSet(args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown operation: %s (valid: list_models, status, switch_model, get, set)", operation)}
	}
}

func (t *ConfigureTool) handleListModels() ToolResult {
	if !t.cfg.UseModelsConfig() {
		return ToolResult{Output: "Using legacy provider config. Run 'joshbot onboard' or configure manually to set up model-centric config."}
	}

	models := t.cfg.ModelsConfig.Models
	if len(models) == 0 {
		return ToolResult{Output: "No models configured."}
	}

	activeName := ""
	if t.cfg.UseModelsConfig() {
		activeName = t.cfg.ModelsConfig.Agent.Model
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].Name < models[j].Name
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Available Models (%d)\n\n", len(models)))
	for _, m := range models {
		active := ""
		if m.Name == activeName {
			active = " ← active"
		}
		status := ""
		if m.Disabled {
			status = " [disabled]"
		}
		sb.WriteString(fmt.Sprintf("- **%s**%s%s: `%s`\n", m.Name, active, status, m.Model))
	}
	sb.WriteString("\nUse `switch_model` with the model name to change the active model.")
	return ToolResult{Output: sb.String()}
}

func (t *ConfigureTool) handleStatus() ToolResult {
	var sb strings.Builder
	sb.WriteString("## Current Configuration\n\n")

	if t.cfg.UseModelsConfig() {
		activeName := t.cfg.ModelsConfig.Agent.Model
		sb.WriteString(fmt.Sprintf("**Config format:** model-centric\n"))
		sb.WriteString(fmt.Sprintf("**Active model:** %s\n", activeName))

		if m, ok := t.cfg.GetModel(activeName); ok {
			sb.WriteString(fmt.Sprintf("**Model ID:** `%s`\n", m.Model))
			sb.WriteString(fmt.Sprintf("**Max tokens:** %d\n", m.MaxTokens))
			sb.WriteString(fmt.Sprintf("**API base:** %s\n", m.APIBase))
			if len(m.APIKey) > 8 {
				sb.WriteString(fmt.Sprintf("**API key:** %s...%s\n", m.APIKey[:4], m.APIKey[len(m.APIKey)-4:]))
			} else {
				sb.WriteString("**API key:** configured\n")
			}
		}

		if len(t.cfg.ModelsConfig.Agent.Fallback) > 0 {
			sb.WriteString(fmt.Sprintf("**Fallback:** %s\n", strings.Join(t.cfg.ModelsConfig.Agent.Fallback, ", ")))
		}

		sb.WriteString(fmt.Sprintf("\n**Temperature:** %.1f\n", t.cfg.Agents.Defaults.Temperature))
		sb.WriteString(fmt.Sprintf("**Max tool iterations:** %d\n", t.cfg.Agents.Defaults.MaxToolIterations))
		sb.WriteString(fmt.Sprintf("**Workspace:** %s\n", t.cfg.Agents.Defaults.Workspace))
	} else {
		sb.WriteString("**Config format:** legacy provider-based\n")
		sb.WriteString(fmt.Sprintf("**Default provider:** %s\n", t.cfg.ProviderDefaults.Default))
		sb.WriteString(fmt.Sprintf("**Model:** %s\n", t.cfg.Agents.Defaults.Model))
	}

	return ToolResult{Output: sb.String()}
}

func (t *ConfigureTool) handleSwitchModel(args map[string]any) ToolResult {
	if !t.cfg.UseModelsConfig() {
		return ToolResult{Error: fmt.Errorf("model switching requires model-centric config. Run 'joshbot onboard' first")}
	}

	modelName, _ := args["model"].(string)
	if modelName == "" {
		return ToolResult{Error: fmt.Errorf("'model' is required for 'switch_model'")}
	}

	if _, ok := t.cfg.GetModel(modelName); !ok {
		return ToolResult{Error: fmt.Errorf("model %q not found. Use 'list_models' to see available models", modelName)}
	}

	prev := t.cfg.ModelsConfig.Agent.Model
	t.cfg.ModelsConfig.Agent.Model = modelName

	if err := config.Save(t.cfg); err != nil {
		// Restore on failure
		t.cfg.ModelsConfig.Agent.Model = prev
		return ToolResult{Error: fmt.Errorf("failed to save config: %w", err)}
	}

	if t.reloadProviders != nil {
		if err := t.reloadProviders(); err != nil {
			log.Warn("Config saved but provider reload failed", "error", err)
			return ToolResult{Output: fmt.Sprintf("Switched from %q to %q. Config saved but provider reload failed: %s. Changes will apply on restart.", prev, modelName, err)}
		}
	}

	log.Info("Switched active model", "from", prev, "to", modelName)
	return ToolResult{Output: fmt.Sprintf("Switched active model from **%s** to **%s**. The change is active now.", prev, modelName)}
}

func (t *ConfigureTool) handleGet(args map[string]any) ToolResult {
	setting, _ := args["setting"].(string)
	if setting == "" {
		return ToolResult{Error: fmt.Errorf("'setting' is required for 'get'")}
	}

	switch setting {
	case "model":
		if t.cfg.UseModelsConfig() {
			return ToolResult{Output: fmt.Sprintf("Active model: **%s**", t.cfg.ModelsConfig.Agent.Model)}
		}
		return ToolResult{Output: fmt.Sprintf("Default model: `%s`", t.cfg.Agents.Defaults.Model)}
	case "temperature":
		return ToolResult{Output: fmt.Sprintf("Temperature: **%.1f**", t.cfg.Agents.Defaults.Temperature)}
	case "max_tokens":
		return ToolResult{Output: fmt.Sprintf("Max tokens: **%d**", t.cfg.Agents.Defaults.MaxTokens)}
	case "workspace":
		return ToolResult{Output: fmt.Sprintf("Workspace: `%s`", t.cfg.Agents.Defaults.Workspace)}
	default:
		return ToolResult{Error: fmt.Errorf("unknown setting: %s (valid: model, temperature, max_tokens, workspace)", setting)}
	}
}

func (t *ConfigureTool) handleSet(args map[string]any) ToolResult {
	setting, _ := args["setting"].(string)
	if setting == "" {
		return ToolResult{Error: fmt.Errorf("'setting' is required for 'set'")}
	}

	value, _ := args["value"].(string)
	if value == "" && setting != "" {
		return ToolResult{Error: fmt.Errorf("'value' is required for 'set'")}
	}

	switch setting {
	case "temperature":
		return t.setFloat(&t.cfg.Agents.Defaults.Temperature, value, "temperature")
	case "max_tokens":
		return t.setInt(&t.cfg.Agents.Defaults.MaxTokens, value, "max tokens")
	case "model":
		return t.handleSwitchModel(args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown setting: %s (valid: model, temperature, max_tokens)", setting)}
	}
}

func (t *ConfigureTool) setFloat(target *float64, value, name string) ToolResult {
	var v float64
	if _, err := fmt.Sscanf(value, "%f", &v); err != nil {
		return ToolResult{Error: fmt.Errorf("invalid %s value: %q (use decimal like 0.7)", name, value)}
	}
	prev := *target
	*target = v
	if err := config.Save(t.cfg); err != nil {
		*target = prev
		return ToolResult{Error: fmt.Errorf("failed to save config: %w", err)}
	}
	log.Info("Updated setting", "name", name, "from", prev, "to", v)
	return ToolResult{Output: fmt.Sprintf("Set %s from **%.2f** to **%.2f**. Saved to config.", name, prev, v)}
}

func (t *ConfigureTool) setInt(target *int, value, name string) ToolResult {
	var v int
	if _, err := fmt.Sscanf(value, "%d", &v); err != nil {
		return ToolResult{Error: fmt.Errorf("invalid %s value: %q (use integer like 4096)", name, value)}
	}
	prev := *target
	*target = v
	if err := config.Save(t.cfg); err != nil {
		*target = prev
		return ToolResult{Error: fmt.Errorf("failed to save config: %w", err)}
	}
	log.Info("Updated setting", "name", name, "from", prev, "to", v)
	return ToolResult{Output: fmt.Sprintf("Set %s from **%d** to **%d**. Saved to config.", name, prev, v)}
}
