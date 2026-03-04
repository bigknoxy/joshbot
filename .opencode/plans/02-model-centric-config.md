# Plan: Model-Centric Configuration

**Status:** ✅ Completed  
**Priority:** High  
**Estimated Effort:** 8-12 hours (~400 LOC)  
**Impact:** Medium (better UX, simpler config)  
**Risk:** Medium (breaking change, requires migration)

---

## Goal

Simplify provider configuration by adopting a model-centric approach where models are defined directly with their API keys and endpoints, rather than requiring separate provider configuration.

---

## Background

### Current joshbot Config (Provider-Centric)

```json
{
  "providers": {
    "nvidia": {
      "api_key": "nvapi-xxx",
      "api_base": "https://integrate.api.nvidia.com/v1",
      "enabled": true
    },
    "openrouter": {
      "api_key": "sk-or-xxx",
      "api_base": "https://openrouter.ai/api/v1",
      "enabled": true
    }
  },
  "agents": {
    "defaults": {
      "model": "nvidia/llama-3.1-nemotron-70b-instruct",
      "provider": "nvidia"
    }
  }
}
```

**Issues:**
- Provider and model are separate concerns
- User must know which provider has which model
- Fallback requires manual configuration
- Adding a new model requires understanding provider structure

### Proposed Config (Model-Centric)

```json
{
  "models": [
    {
      "name": "smart",
      "model": "anthropic/claude-sonnet-4",
      "api_key": "sk-ant-xxx"
    },
    {
      "name": "fast",
      "model": "groq/llama-3.3-70b-versatile",
      "api_key": "gsk-xxx",
      "api_base": "https://api.groq.com/openai/v1"
    },
    {
      "name": "local",
      "model": "ollama/llama3.2",
      "api_base": "http://localhost:11434/v1"
    }
  ],
  "agent": {
    "model": "smart",
    "fallback": ["fast", "local"]
  }
}
```

**Benefits:**
- Simpler, more intuitive
- Easy fallback chains
- No need to understand provider routing
- Self-documenting

---

## Implementation Design

### Provider Detection Heuristics

The system auto-detects the provider from the model name prefix:

| Model Prefix | Provider | API Format | Default Base URL |
|--------------|----------|------------|------------------|
| `anthropic/` | Anthropic | Native API | `https://api.anthropic.com` |
| `openai/` | OpenAI | Native API | `https://api.openai.com/v1` |
| `groq/` | Groq | OpenAI-compatible | `https://api.groq.com/openai/v1` |
| `ollama/` | Ollama | OpenAI-compatible | `http://localhost:11434/v1` |
| `openrouter/` | OpenRouter | OpenAI-compatible | `https://openrouter.ai/api/v1` |
| `nvidia/` | NVIDIA | OpenAI-compatible | `https://integrate.api.nvidia.com/v1` |
| `deepseek/` | DeepSeek | OpenAI-compatible | `https://api.deepseek.com/v1` |
| `gemini/` | Google Gemini | Native API | `https://generativelanguage.googleapis.com` |
| No prefix | OpenAI-compatible | Use provided `api_base` | Required |

### Fallback Chain

When a model fails, try the next in the fallback list:
1. Track failures per model with cooldown
2. Skip models that recently failed
3. Return error only when all models exhausted

---

## Step-by-Step Implementation

### Step 1: Define new config types

**File:** `internal/config/config.go`

Add these new types after the existing types (around line 78):

```go
// ModelConfig defines a single model with its API configuration.
// This is the new model-centric approach.
type ModelConfig struct {
    Name       string            `mapstructure:"name" json:"name" yaml:"name"`
    Model      string            `mapstructure:"model" json:"model" yaml:"model"`           // e.g., "anthropic/claude-sonnet-4"
    APIKey     string            `mapstructure:"api_key" json:"api_key,omitempty" yaml:"api_key,omitempty"`
    APIBase    string            `mapstructure:"api_base" json:"api_base,omitempty" yaml:"api_base,omitempty"`
    Extra      map[string]string `mapstructure:"extra" json:"extra,omitempty" yaml:"extra,omitempty"` // Extra headers
    Disabled   bool              `mapstructure:"disabled" json:"disabled,omitempty" yaml:"disabled,omitempty"`
    MaxTokens  int               `mapstructure:"max_tokens" json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
    Temp       *float64          `mapstructure:"temperature" json:"temperature,omitempty" yaml:"temperature,omitempty"`
}

// AgentConfig holds agent configuration (new simplified structure).
type AgentConfig struct {
    Model    string   `mapstructure:"model" json:"model" yaml:"model"`       // Reference to model name
    Fallback []string `mapstructure:"fallback" json:"fallback" yaml:"fallback"` // Fallback model names
}

// ModelsConfig holds all model configurations.
type ModelsConfig struct {
    Models []ModelConfig `mapstructure:"models" json:"models" yaml:"models"`
    Agent  AgentConfig   `mapstructure:"agent" json:"agent" yaml:"agent"`
}
```

### Step 2: Add provider detection logic

Add this function after the new types:

```go
// ProviderInfo contains detected provider information.
type ProviderInfo struct {
    Name      string
    APIFormat string // "openai", "anthropic", "gemini"
    BaseURL   string
}

// providerPrefixes maps model prefixes to provider info.
var providerPrefixes = map[string]ProviderInfo{
    "anthropic/": {Name: "anthropic", APIFormat: "anthropic", BaseURL: "https://api.anthropic.com"},
    "openai/":    {Name: "openai", APIFormat: "openai", BaseURL: "https://api.openai.com/v1"},
    "groq/":      {Name: "groq", APIFormat: "openai", BaseURL: "https://api.groq.com/openai/v1"},
    "ollama/":    {Name: "ollama", APIFormat: "openai", BaseURL: "http://localhost:11434/v1"},
    "openrouter/": {Name: "openrouter", APIFormat: "openai", BaseURL: "https://openrouter.ai/api/v1"},
    "nvidia/":    {Name: "nvidia", APIFormat: "openai", BaseURL: "https://integrate.api.nvidia.com/v1"},
    "deepseek/":  {Name: "deepseek", APIFormat: "openai", BaseURL: "https://api.deepseek.com/v1"},
    "gemini/":    {Name: "gemini", APIFormat: "gemini", BaseURL: "https://generativelanguage.googleapis.com"},
    "cerebras/":  {Name: "cerebras", APIFormat: "openai", BaseURL: "https://api.cerebras.ai/v1"},
}

// DetectProvider extracts provider info from a model string.
func DetectProvider(model string) ProviderInfo {
    for prefix, info := range providerPrefixes {
        if strings.HasPrefix(model, prefix) {
            return info
        }
    }
    // Unknown provider - assume OpenAI-compatible
    return ProviderInfo{Name: "unknown", APIFormat: "openai", BaseURL: ""}
}

// StripProviderPrefix removes the provider prefix from a model name.
func StripProviderPrefix(model string) string {
    for prefix := range providerPrefixes {
        if strings.HasPrefix(model, prefix) {
            return strings.TrimPrefix(model, prefix)
        }
    }
    return model
}
```

### Step 3: Update Config struct

Modify the Config struct to include both old and new formats (non-breaking):

```go
// Config is the root configuration for joshbot.
type Config struct {
    SchemaVersion    int                       `mapstructure:"schema_version" json:"schema_version" yaml:"schema_version"`
    
    // New model-centric config (preferred)
    ModelsConfig     ModelsConfig              `mapstructure:"models_config" json:"models_config,omitempty" yaml:"models_config,omitempty"`
    
    // Legacy provider-centric config (still supported for backward compatibility)
    Providers        map[string]ProviderConfig `mapstructure:"providers" json:"providers,omitempty" yaml:"providers,omitempty"`
    ProviderDefaults ProviderDefaults          `mapstructure:"provider_defaults" json:"provider_defaults,omitempty" yaml:"provider_defaults,omitempty"`
    Agents           AgentsConfig              `mapstructure:"agents" json:"agents,omitempty" yaml:"agents,omitempty"`
    
    // Other config sections
    Channels         ChannelsConfig            `mapstructure:"channels" json:"channels" yaml:"channels"`
    Tools            ToolsConfig               `mapstructure:"tools" json:"tools" yaml:"tools"`
    Gateway          GatewayConfig             `mapstructure:"gateway" json:"gateway" yaml:"gateway"`
    LogLevel         string                    `mapstructure:"log_level" json:"log_level" yaml:"log_level"`
    User             UserConfig                `mapstructure:"user" json:"user,omitempty" yaml:"user,omitempty"`
}
```

### Step 4: Add config helper methods

Add these methods to Config:

```go
// UseModelsConfig returns true if the new model-centric config is being used.
func (c *Config) UseModelsConfig() bool {
    return len(c.ModelsConfig.Models) > 0
}

// GetModel returns a model config by name.
func (c *Config) GetModel(name string) (ModelConfig, bool) {
    for _, m := range c.ModelsConfig.Models {
        if m.Name == name {
            return m, true
        }
    }
    return ModelConfig{}, false
}

// GetActiveModel returns the currently configured model.
func (c *Config) GetActiveModel() (ModelConfig, error) {
    modelName := c.ModelsConfig.Agent.Model
    if modelName == "" {
        return ModelConfig{}, fmt.Errorf("no model configured")
    }
    
    model, ok := c.GetModel(modelName)
    if !ok {
        return ModelConfig{}, fmt.Errorf("model not found: %s", modelName)
    }
    
    return model, nil
}

// GetFallbackModels returns the fallback model chain.
func (c *Config) GetFallbackModels() []ModelConfig {
    var models []ModelConfig
    for _, name := range c.ModelsConfig.Agent.Fallback {
        if m, ok := c.GetModel(name); ok {
            models = append(models, m)
        }
    }
    return models
}

// ResolveModelConfig resolves the full configuration for a model,
// including API base URL from provider detection.
func (c *Config) ResolveModelConfig(name string) (ResolvedModelConfig, error) {
    model, ok := c.GetModel(name)
    if !ok {
        return ResolvedModelConfig{}, fmt.Errorf("model not found: %s", name)
    }
    
    provider := DetectProvider(model.Model)
    
    // Use provided API base, or provider default
    apiBase := model.APIBase
    if apiBase == "" {
        apiBase = provider.BaseURL
    }
    
    // Strip provider prefix for the actual model ID
    modelID := StripProviderPrefix(model.Model)
    
    return ResolvedModelConfig{
        Name:       model.Name,
        ModelID:    modelID,
        Provider:   provider.Name,
        APIFormat:  provider.APIFormat,
        APIBase:    apiBase,
        APIKey:     model.APIKey,
        Extra:      model.Extra,
        MaxTokens:  model.MaxTokens,
        Temp:       model.Temp,
    }, nil
}

// ResolvedModelConfig is a fully resolved model configuration.
type ResolvedModelConfig struct {
    Name      string
    ModelID   string            // The actual model ID (without prefix)
    Provider  string            // Detected provider name
    APIFormat string            // API format to use
    APIBase   string            // Full API base URL
    APIKey    string            // API key
    Extra     map[string]string // Extra headers
    MaxTokens int               // Max tokens override
    Temp      *float64          // Temperature override
}
```

### Step 5: Update Defaults function

Modify the Defaults() function to support both formats:

```go
// Defaults returns a Config with all default values set.
func Defaults() *Config {
    return &Config{
        SchemaVersion: CurrentSchemaVersion,
        
        // New format (empty by default, user must configure)
        ModelsConfig: ModelsConfig{
            Models: []ModelConfig{},
            Agent: AgentConfig{
                Model:    "",
                Fallback: []string{},
            },
        },
        
        // Legacy format (still supported)
        Providers: map[string]ProviderConfig{
            "openrouter": {},
        },
        Agents: AgentsConfig{
            Defaults: AgentDefaults{
                Workspace:           DefaultWorkspace,
                Model:               DefaultModel,
                MaxTokens:           DefaultMaxTokens,
                Temperature:         DefaultTemperature,
                MaxToolIterations:   DefaultMaxToolIterations,
                MemoryWindow:        DefaultMemoryWindow,
                CompactionThreshold: DefaultCompactionThreshold,
            },
        },
        
        // Rest unchanged...
        Channels: ChannelsConfig{
            Telegram: TelegramConfig{
                Enabled:   false,
                Token:     "",
                AllowFrom: []string{},
                Proxy:     "",
            },
        },
        Tools: ToolsConfig{
            Web: WebToolsConfig{
                Search: WebSearchConfig{
                    APIKey: "",
                },
            },
            Exec: ExecConfig{
                Timeout: DefaultExecTimeout,
            },
            RestrictToWorkspace:    true,
            ShellAllowList:         []string{},
            FilesystemAllowedPaths: []string{},
            ToolOutputMaxChars:     DefaultToolOutputMaxChars,
        },
        Gateway: GatewayConfig{
            Host: DefaultGatewayHost,
            Port: DefaultGatewayPort,
        },
        LogLevel: "info",
    }
}
```

### Step 6: Update Validate function

Add validation for the new config format:

```go
// Validate validates the configuration and returns an error if invalid.
func (c *Config) Validate() error {
    // Validate new format if present
    if c.UseModelsConfig() {
        if len(c.ModelsConfig.Models) == 0 {
            return errors.New("no models configured")
        }
        
        if c.ModelsConfig.Agent.Model == "" {
            return errors.New("no active model configured")
        }
        
        // Check active model exists
        if _, ok := c.GetModel(c.ModelsConfig.Agent.Model); !ok {
            return fmt.Errorf("active model not found: %s", c.ModelsConfig.Agent.Model)
        }
        
        // Check fallback models exist
        for _, name := range c.ModelsConfig.Agent.Fallback {
            if _, ok := c.GetModel(name); !ok {
                return fmt.Errorf("fallback model not found: %s", name)
            }
        }
        
        // Validate each model has required fields
        for _, m := range c.ModelsConfig.Models {
            if m.Name == "" {
                return errors.New("model name cannot be empty")
            }
            if m.Model == "" {
                return fmt.Errorf("model %s: model ID cannot be empty", m.Name)
            }
            
            // Check API base is provided for unknown providers
            provider := DetectProvider(m.Model)
            if provider.Name == "unknown" && m.APIBase == "" {
                return fmt.Errorf("model %s: api_base required for unknown provider", m.Name)
            }
        }
        
        return nil
    }
    
    // Validate legacy format
    if c.Agents.Defaults.Model == "" {
        return errors.New("model cannot be empty")
    }
    
    // ... rest of legacy validation ...
    
    return nil
}
```

### Step 7: Add environment variable support

Update applyEnvOverrides to support new format:

```go
// applyEnvOverrides applies environment variable overrides to the config.
func applyEnvOverrides(cfg *Config) {
    // ... existing env vars ...
    
    // New model-centric env vars
    // Format: JOSHBOT_MODELS_CONFIG__MODELS__0__NAME=smart
    //         JOSHBOT_MODELS_CONFIG__MODELS__0__MODEL=anthropic/claude-sonnet-4
    //         JOSHBOT_MODELS_CONFIG__MODELS__0__API_KEY=sk-xxx
    //         JOSHBOT_MODELS_CONFIG__AGENT__MODEL=smart
    
    if v := os.Getenv("JOSHBOT_MODELS_CONFIG__AGENT__MODEL"); v != "" {
        cfg.ModelsConfig.Agent.Model = v
    }
    
    if v := os.Getenv("JOSHBOT_MODELS_CONFIG__AGENT__FALLBACK"); v != "" {
        cfg.ModelsConfig.Agent.Fallback = strings.Split(v, ",")
        for i := range cfg.ModelsConfig.Agent.Fallback {
            cfg.ModelsConfig.Agent.Fallback[i] = strings.TrimSpace(cfg.ModelsConfig.Agent.Fallback[i])
        }
    }
    
    // Parse model configs from env (indexed approach)
    // JOSHBOT_MODELS_CONFIG__MODELS__0__NAME, __MODEL, __API_KEY, __API_BASE
    for i := 0; ; i++ {
        prefix := fmt.Sprintf("JOSHBOT_MODELS_CONFIG__MODELS__%d__", i)
        name := os.Getenv(prefix + "NAME")
        if name == "" {
            break
        }
        
        model := ModelConfig{
            Name:    name,
            Model:   os.Getenv(prefix + "MODEL"),
            APIKey:  os.Getenv(prefix + "API_KEY"),
            APIBase: os.Getenv(prefix + "API_BASE"),
        }
        
        cfg.ModelsConfig.Models = append(cfg.ModelsConfig.Models, model)
    }
}
```

### Step 8: Create migration helper

Add migration from old to new format:

```go
// migrateToModelsConfig converts legacy provider config to new model-centric format.
func migrateToModelsConfig(cfg *Config) error {
    if cfg.UseModelsConfig() {
        return nil // Already using new format
    }
    
    models := []ModelConfig{}
    
    // Convert each provider to a model
    for providerName, pCfg := range cfg.Providers {
        if !pCfg.Enabled {
            continue
        }
        
        // Determine model name from config or default
        modelID := pCfg.Model
        if modelID == "" {
            // Use provider prefix + default model
            switch providerName {
            case "openrouter":
                modelID = "openrouter/anthropic/claude-sonnet-4"
            case "nvidia":
                modelID = "nvidia/llama-3.1-nemotron-70b-instruct"
            case "ollama":
                modelID = "ollama/llama3.2"
            default:
                continue
            }
        }
        
        model := ModelConfig{
            Name:    providerName,
            Model:   modelID,
            APIKey:  pCfg.APIKey,
            APIBase: pCfg.APIBase,
            Extra:   pCfg.ExtraHeaders,
        }
        
        models = append(models, model)
    }
    
    if len(models) == 0 {
        return nil // No providers to migrate
    }
    
    cfg.ModelsConfig = ModelsConfig{
        Models: models,
        Agent: AgentConfig{
            Model:    models[0].Name, // Use first as default
            Fallback: []string{},
        },
    }
    
    logger.Info("Migrated provider config to model-centric format")
    return nil
}
```

### Step 9: Update provider initialization

**File:** `internal/providers/litellm.go`

Add a new constructor that accepts ResolvedModelConfig:

```go
// NewProviderFromModel creates a provider from a resolved model config.
func NewProviderFromModel(cfg config.ResolvedModelConfig) (Provider, error) {
    switch cfg.APIFormat {
    case "anthropic":
        return newAnthropicProvider(cfg)
    case "gemini":
        return newGeminiProvider(cfg)
    case "openai":
        return newOpenAICompatibleProvider(cfg)
    default:
        return nil, fmt.Errorf("unsupported API format: %s", cfg.APIFormat)
    }
}

func newOpenAICompatibleProvider(cfg config.ResolvedModelConfig) (Provider, error) {
    return &LiteLLMProvider{
        baseURL:    cfg.APIBase,
        apiKey:     cfg.APIKey,
        model:      cfg.ModelID,
        maxTokens:  cfg.MaxTokens,
        extra:      cfg.Extra,
        httpClient: &http.Client{Timeout: 120 * time.Second},
    }, nil
}

func newAnthropicProvider(cfg config.ResolvedModelConfig) (Provider, error) {
    // Anthropic uses different API format
    // Implementation depends on whether we add native Anthropic support
    return newOpenAICompatibleProvider(cfg) // Fallback to OpenAI-compatible through proxy
}

func newGeminiProvider(cfg config.ResolvedModelConfig) (Provider, error) {
    // Similar to Anthropic
    return newOpenAICompatibleProvider(cfg)
}
```

### Step 10: Add tests

**File:** `internal/config/config_model_test.go`

```go
package config

import (
    "testing"
)

func TestDetectProvider(t *testing.T) {
    tests := []struct {
        model    string
        provider string
        format   string
    }{
        {"anthropic/claude-sonnet-4", "anthropic", "anthropic"},
        {"openai/gpt-4", "openai", "openai"},
        {"groq/llama-3.3-70b", "groq", "openai"},
        {"ollama/llama3.2", "ollama", "openai"},
        {"openrouter/anthropic/claude", "openrouter", "openai"},
        {"unknown-model", "unknown", "openai"},
    }
    
    for _, tt := range tests {
        info := DetectProvider(tt.model)
        if info.Name != tt.provider {
            t.Errorf("DetectProvider(%q).Name = %q, want %q", tt.model, info.Name, tt.provider)
        }
        if info.APIFormat != tt.format {
            t.Errorf("DetectProvider(%q).APIFormat = %q, want %q", tt.model, info.APIFormat, tt.format)
        }
    }
}

func TestStripProviderPrefix(t *testing.T) {
    tests := []struct {
        model    string
        stripped string
    }{
        {"anthropic/claude-sonnet-4", "claude-sonnet-4"},
        {"groq/llama-3.3-70b", "llama-3.3-70b"},
        {"no-prefix-model", "no-prefix-model"},
    }
    
    for _, tt := range tests {
        got := StripProviderPrefix(tt.model)
        if got != tt.stripped {
            t.Errorf("StripProviderPrefix(%q) = %q, want %q", tt.model, got, tt.stripped)
        }
    }
}

func TestGetModel(t *testing.T) {
    cfg := &Config{
        ModelsConfig: ModelsConfig{
            Models: []ModelConfig{
                {Name: "smart", Model: "anthropic/claude-sonnet-4"},
                {Name: "fast", Model: "groq/llama-3.3-70b"},
            },
            Agent: AgentConfig{Model: "smart"},
        },
    }
    
    model, ok := cfg.GetModel("smart")
    if !ok {
        t.Error("GetModel should find existing model")
    }
    if model.Name != "smart" {
        t.Errorf("GetModel name = %q, want smart", model.Name)
    }
    
    _, ok = cfg.GetModel("nonexistent")
    if ok {
        t.Error("GetModel should not find nonexistent model")
    }
}

func TestResolveModelConfig(t *testing.T) {
    cfg := &Config{
        ModelsConfig: ModelsConfig{
            Models: []ModelConfig{
                {
                    Name:   "test",
                    Model:  "groq/llama-3.3-70b",
                    APIKey: "test-key",
                },
            },
        },
    }
    
    resolved, err := cfg.ResolveModelConfig("test")
    if err != nil {
        t.Fatalf("ResolveModelConfig error: %v", err)
    }
    
    if resolved.Provider != "groq" {
        t.Errorf("Provider = %q, want groq", resolved.Provider)
    }
    if resolved.ModelID != "llama-3.3-70b" {
        t.Errorf("ModelID = %q, want llama-3.3-70b", resolved.ModelID)
    }
    if resolved.APIBase != "https://api.groq.com/openai/v1" {
        t.Errorf("APIBase = %q, want default groq URL", resolved.APIBase)
    }
}

func TestValidate_ModelsConfig(t *testing.T) {
    tests := []struct {
        name    string
        cfg     *Config
        wantErr bool
    }{
        {
            name: "valid config",
            cfg: &Config{
                ModelsConfig: ModelsConfig{
                    Models: []ModelConfig{
                        {Name: "test", Model: "openai/gpt-4", APIKey: "key"},
                    },
                    Agent: AgentConfig{Model: "test"},
                },
            },
            wantErr: false,
        },
        {
            name: "missing model",
            cfg: &Config{
                ModelsConfig: ModelsConfig{
                    Models: []ModelConfig{},
                    Agent:  AgentConfig{Model: "test"},
                },
            },
            wantErr: true,
        },
        {
            name: "missing active model",
            cfg: &Config{
                ModelsConfig: ModelsConfig{
                    Models: []ModelConfig{
                        {Name: "test", Model: "openai/gpt-4"},
                    },
                    Agent: AgentConfig{Model: ""},
                },
            },
            wantErr: true,
        },
        {
            name: "active model not found",
            cfg: &Config{
                ModelsConfig: ModelsConfig{
                    Models: []ModelConfig{
                        {Name: "test", Model: "openai/gpt-4"},
                    },
                    Agent: AgentConfig{Model: "nonexistent"},
                },
            },
            wantErr: true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.cfg.Validate()
            if (err != nil) != tt.wantErr {
                t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

---

## Verification Steps

1. **Build and test:**
   ```bash
   go build ./...
   go test ./internal/config/... -v
   go test ./internal/providers/... -v
   ```

2. **Test config loading:**
   ```bash
   # Create test config
   cat > ~/.joshbot/config_test.json << 'EOF'
   {
     "schema_version": 5,
     "models_config": {
       "models": [
         {
           "name": "test",
           "model": "openai/gpt-4",
           "api_key": "test-key"
         }
       ],
       "agent": {
         "model": "test"
       }
     }
   }
   EOF
   
   # Load and verify
   joshbot status
   ```

3. **Test migration:**
   ```bash
   # Start with old format
   joshbot onboard
   
   # Manually trigger migration (this would be automatic)
   # Verify new format is created
   ```

4. **Test env vars:**
   ```bash
   JOSHBOT_MODELS_CONFIG__AGENT__MODEL=smart \
   JOSHBOT_MODELS_CONFIG__MODELS__0__NAME=smart \
   JOSHBOT_MODELS_CONFIG__MODELS__0__MODEL=openai/gpt-4 \
   JOSHBOT_MODELS_CONFIG__MODELS__0__API_KEY=test \
   joshbot status
   ```

---

## Migration Strategy

### Phase 1: Non-Breaking (This Implementation)
- Add new config types alongside old ones
- Both formats supported
- Auto-migration from old to new on load

### Phase 2: Transition Period
- Log warning when using old format
- Documentation updated to recommend new format
- Onboarding generates new format

### Phase 3: Deprecation (v2.0)
- Remove old provider-centric config
- Require new format

---

## Files Changed

| File | Changes |
|------|---------|
| `internal/config/config.go` | Add ModelConfig, AgentConfig, detection, validation |
| `internal/config/config_model_test.go` | New test file |
| `internal/providers/litellm.go` | Add NewProviderFromModel |
| `cmd/joshbot/main.go` | Update provider initialization logic |
| `docs/CONFIG.md` | Update documentation |

---

## Completion Checklist

- [x] Added ModelConfig, AgentConfig types
- [x] Added provider detection (DetectProvider, StripProviderPrefix)
- [x] Updated Config struct with ModelsConfig
- [x] Added Config helper methods (GetModel, ResolveModelConfig)
- [x] Updated Defaults()
- [x] Updated Validate()
- [x] Added env var support for new format
- [x] Updated provider initialization
- [x] Added unit tests
- [x] Verified build passes
- [x] Verified tests pass
- [x] Tested with API keys (OpenRouter, NVIDIA)
- [x] Code review and fixes applied

---

## Progress Log

| Date | Status | Notes |
|------|--------|-------|
| 2026-03-03 | Not Started | Plan created |
| 2026-03-03 | In Progress | Started implementation |
| 2026-03-03 | Completed | All tasks done, tests pass, verified with API keys |
