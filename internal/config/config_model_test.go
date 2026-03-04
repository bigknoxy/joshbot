package config

import (
	"strings"
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
		{"nvidia/llama-3.1-nemotron-70b", "nvidia", "openai"},
		{"deepseek/deepseek-chat", "deepseek", "openai"},
		{"gemini/gemini-2.0-flash", "gemini", "openai"},
		{"cerebras/llama-3.3-70b", "cerebras", "openai"},
		{"unknown-model", "unknown", "openai"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			info := DetectProvider(tt.model)
			if info.Name != tt.provider {
				t.Errorf("DetectProvider(%q).Name = %q, want %q", tt.model, info.Name, tt.provider)
			}
			if info.APIFormat != tt.format {
				t.Errorf("DetectProvider(%q).APIFormat = %q, want %q", tt.model, info.APIFormat, tt.format)
			}
		})
	}
}

func TestStripProviderPrefix(t *testing.T) {
	tests := []struct {
		model    string
		stripped string
	}{
		{"anthropic/claude-sonnet-4", "claude-sonnet-4"},
		{"groq/llama-3.3-70b", "llama-3.3-70b"},
		{"nvidia/llama-3.1-nemotron-70b-instruct", "llama-3.1-nemotron-70b-instruct"},
		{"openrouter/anthropic/claude-sonnet-4", "anthropic/claude-sonnet-4"},
		{"no-prefix-model", "no-prefix-model"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := StripProviderPrefix(tt.model)
			if got != tt.stripped {
				t.Errorf("StripProviderPrefix(%q) = %q, want %q", tt.model, got, tt.stripped)
			}
		})
	}
}

func TestConfig_GetModel(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{Name: "smart", Model: "anthropic/claude-sonnet-4"},
				{Name: "fast", Model: "groq/llama-3.3-70b"},
			},
			Agent: AgentModelConfig{Model: "smart"},
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

func TestConfig_UseModelsConfig(t *testing.T) {
	cfgWithModels := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{{Name: "test", Model: "openai/gpt-4"}},
		},
	}
	if !cfgWithModels.UseModelsConfig() {
		t.Error("UseModelsConfig should return true when models are configured")
	}

	cfgWithoutModels := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{},
		},
	}
	if cfgWithoutModels.UseModelsConfig() {
		t.Error("UseModelsConfig should return false when no models configured")
	}
}

func TestConfig_GetActiveModel(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{Name: "smart", Model: "anthropic/claude-sonnet-4"},
				{Name: "fast", Model: "groq/llama-3.3-70b"},
			},
			Agent: AgentModelConfig{Model: "smart"},
		},
	}

	model, err := cfg.GetActiveModel()
	if err != nil {
		t.Fatalf("GetActiveModel error: %v", err)
	}
	if model.Name != "smart" {
		t.Errorf("GetActiveModel name = %q, want smart", model.Name)
	}

	cfgNoActive := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{{Name: "test", Model: "openai/gpt-4"}},
			Agent:  AgentModelConfig{Model: ""},
		},
	}

	_, err = cfgNoActive.GetActiveModel()
	if err == nil {
		t.Error("GetActiveModel should error when no active model set")
	}
}

func TestConfig_GetFallbackModels(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{Name: "smart", Model: "anthropic/claude-sonnet-4"},
				{Name: "fast", Model: "groq/llama-3.3-70b"},
				{Name: "local", Model: "ollama/llama3.2"},
				{Name: "disabled", Model: "openai/gpt-4", Disabled: true},
			},
			Agent: AgentModelConfig{
				Model:    "smart",
				Fallback: []string{"fast", "local", "disabled"},
			},
		},
	}

	fallbacks := cfg.GetFallbackModels()
	if len(fallbacks) != 2 {
		t.Errorf("GetFallbackModels returned %d models, want 2 (disabled should be excluded)", len(fallbacks))
	}
}

func TestConfig_ResolveModelConfig(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{
					Name:      "test",
					Model:     "groq/llama-3.3-70b",
					APIKey:    "test-key",
					MaxTokens: 4096,
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
	if resolved.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want test-key", resolved.APIKey)
	}
	if resolved.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", resolved.MaxTokens)
	}
}

func TestConfig_ResolveModelConfig_CustomBase(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{
					Name:    "custom",
					Model:   "unknown/model",
					APIKey:  "key",
					APIBase: "https://custom.api.com/v1",
				},
			},
		},
	}

	resolved, err := cfg.ResolveModelConfig("custom")
	if err != nil {
		t.Fatalf("ResolveModelConfig error: %v", err)
	}

	if resolved.Provider != "unknown" {
		t.Errorf("Provider = %q, want unknown", resolved.Provider)
	}
	if resolved.APIBase != "https://custom.api.com/v1" {
		t.Errorf("APIBase = %q, want custom URL", resolved.APIBase)
	}
}

func TestConfig_ResolveModelConfig_UnknownWithoutBase(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{
					Name:   "bad",
					Model:  "unknown/model",
					APIKey: "key",
				},
			},
		},
	}

	_, err := cfg.ResolveModelConfig("bad")
	if err == nil {
		t.Error("ResolveModelConfig should error for unknown provider without api_base")
	}
}

func TestConfig_ResolveModelConfig_Disabled(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{
					Name:     "disabled",
					Model:    "openai/gpt-4",
					APIKey:   "key",
					Disabled: true,
				},
			},
		},
	}

	_, err := cfg.ResolveModelConfig("disabled")
	if err == nil {
		t.Error("ResolveModelConfig should error for disabled model")
	}
}

func TestConfig_ResolveModelConfig_EmptyAPIKey(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{
					Name:  "nokey",
					Model: "openai/gpt-4",
				},
			},
		},
	}

	_, err := cfg.ResolveModelConfig("nokey")
	if err == nil {
		t.Error("ResolveModelConfig should error for empty API key")
	}
	if !strings.Contains(err.Error(), "api_key required") {
		t.Errorf("Error should mention api_key required, got: %v", err)
	}
}

func TestConfig_ResolveModelConfig_OllamaNoAPIKey(t *testing.T) {
	cfg := &Config{
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{
				{
					Name:  "local",
					Model: "ollama/llama3.2",
				},
			},
		},
	}

	resolved, err := cfg.ResolveModelConfig("local")
	if err != nil {
		t.Errorf("Ollama should work without API key: %v", err)
	}
	if resolved.Provider != "ollama" {
		t.Errorf("Provider = %q, want ollama", resolved.Provider)
	}
}

func TestValidate_ModelsConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			cfg: &Config{
				ModelsConfig: ModelsConfig{
					Models: []ModelConfig{
						{Name: "test", Model: "openai/gpt-4", APIKey: "key"},
					},
					Agent: AgentModelConfig{Model: "test"},
				},
				Agents: AgentsConfig{
					Defaults: AgentDefaults{
						MaxTokens:           4096,
						Temperature:         0.7,
						MaxToolIterations:   20,
						MemoryWindow:        10,
						CompactionThreshold: 0.7,
					},
				},
				Tools: ToolsConfig{
					Exec: ExecConfig{Timeout: 60},
				},
				Gateway:  GatewayConfig{Port: 8080},
				LogLevel: "info",
			},
			wantErr: false,
		},
		{
			name: "missing models (falls back to legacy)",
			cfg: &Config{
				ModelsConfig: ModelsConfig{
					Models: []ModelConfig{},
					Agent:  AgentModelConfig{Model: "test"},
				},
				Agents: AgentsConfig{
					Defaults: AgentDefaults{Model: ""},
				},
			},
			wantErr: true,
			errMsg:  "model cannot be empty",
		},
		{
			name: "missing active model",
			cfg: &Config{
				ModelsConfig: ModelsConfig{
					Models: []ModelConfig{
						{Name: "test", Model: "openai/gpt-4"},
					},
					Agent: AgentModelConfig{Model: ""},
				},
			},
			wantErr: true,
			errMsg:  "no active model configured",
		},
		{
			name: "active model not found",
			cfg: &Config{
				ModelsConfig: ModelsConfig{
					Models: []ModelConfig{
						{Name: "test", Model: "openai/gpt-4"},
					},
					Agent: AgentModelConfig{Model: "nonexistent"},
				},
			},
			wantErr: true,
			errMsg:  "active model not found",
		},
		{
			name: "fallback model not found",
			cfg: &Config{
				ModelsConfig: ModelsConfig{
					Models: []ModelConfig{
						{Name: "test", Model: "openai/gpt-4"},
					},
					Agent: AgentModelConfig{
						Model:    "test",
						Fallback: []string{"nonexistent"},
					},
				},
			},
			wantErr: true,
			errMsg:  "fallback model not found",
		},
		{
			name: "model without api_base for unknown provider",
			cfg: &Config{
				ModelsConfig: ModelsConfig{
					Models: []ModelConfig{
						{Name: "bad", Model: "unknown/model"},
					},
					Agent: AgentModelConfig{Model: "bad"},
				},
			},
			wantErr: true,
			errMsg:  "api_base required for unknown provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Error("Validate() expected error, got nil")
				} else if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %q, want to contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
