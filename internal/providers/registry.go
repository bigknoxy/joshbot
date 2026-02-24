package providers

import (
	"context"
	"fmt"
	"sync"
)

// ProviderFactory is a function that creates a Provider with the given configuration.
type ProviderFactory func(cfg Config) (Provider, error)

// ProviderInfo contains metadata about a provider.
type ProviderInfo struct {
	Factory      ProviderFactory
	DefaultModel string
	DisplayName  string
	Description  string
}

// registry is the global provider registry.
var (
	registry     = make(map[string]ProviderInfo)
	registryLock sync.RWMutex
)

// RegisterProvider registers a provider factory with the given name.
// This allows adding new providers without modifying the core code.
func RegisterProvider(name string, factory ProviderFactory) {
	registryLock.Lock()
	defer registryLock.Unlock()

	name = normalizeProviderName(name)
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("provider already registered: %s", name))
	}

	registry[name] = ProviderInfo{
		Factory: factory,
	}
}

// RegisterProviderWithInfo registers a provider with full metadata.
func RegisterProviderWithInfo(name string, info ProviderInfo) {
	registryLock.Lock()
	defer registryLock.Unlock()

	name = normalizeProviderName(name)
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("provider already registered: %s", name))
	}

	registry[name] = info
}

// GetProvider returns a new provider instance by name with the given configuration.
// Returns an error if the provider is not registered.
func GetProvider(name string, cfg Config) (Provider, error) {
	registryLock.RLock()
	info, exists := registry[normalizeProviderName(name)]
	registryLock.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown provider: %s (available: %v)", name, AvailableProviders())
	}

	return info.Factory(cfg)
}

// AvailableProviders returns a list of all registered provider names.
func AvailableProviders() []string {
	registryLock.RLock()
	defer registryLock.RUnlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// IsProviderRegistered returns true if a provider with the given name is registered.
func IsProviderRegistered(name string) bool {
	registryLock.RLock()
	defer registryLock.RUnlock()

	_, exists := registry[normalizeProviderName(name)]
	return exists
}

// UnregisterProvider removes a provider from the registry.
// This is mainly useful for testing.
func UnregisterProvider(name string) {
	registryLock.Lock()
	defer registryLock.Unlock()

	delete(registry, normalizeProviderName(name))
}

// normalizeProviderName normalizes a provider name to lowercase for case-insensitive lookup.
func normalizeProviderName(name string) string {
	// Handle common aliases
	switch name {
	case "openai":
		return "openai"
	case "openrouter":
		return "openrouter"
	case "anthropic":
		return "anthropic"
	case "google", "gemini":
		return "gemini"
	case "azure":
		return "azure"
	case "local", "ollama":
		return "ollama"
	case "groq":
		return "groq"
	case "nvidia", "nim":
		return "nvidia"
	default:
		return name
	}
}

// GetDefaultModel returns the default model for a provider.
// Returns empty string if provider not found or no default set.
func GetDefaultModel(name string) string {
	registryLock.RLock()
	defer registryLock.RUnlock()

	if info, exists := registry[normalizeProviderName(name)]; exists {
		return info.DefaultModel
	}
	return ""
}

// GetProviderDisplayName returns a human-readable name for a provider.
func GetProviderDisplayName(name string) string {
	registryLock.RLock()
	defer registryLock.RUnlock()

	if info, exists := registry[normalizeProviderName(name)]; exists {
		if info.DisplayName != "" {
			return info.DisplayName
		}
		return name
	}
	return name
}

// GetProviderDescription returns the description for a provider.
func GetProviderDescription(name string) string {
	registryLock.RLock()
	defer registryLock.RUnlock()

	if info, exists := registry[normalizeProviderName(name)]; exists {
		return info.Description
	}
	return ""
}

// init registers the built-in providers.
func init() {
	// Register OpenRouter
	RegisterProviderWithInfo("openrouter", ProviderInfo{
		Factory: func(cfg Config) (Provider, error) {
			if cfg.APIBase == "" {
				cfg.APIBase = "https://openrouter.ai/api/v1"
			}
			return NewLiteLLMProvider(cfg), nil
		},
		DefaultModel: "arcee-ai/trinity-large-preview:free",
		DisplayName:  "OpenRouter",
		Description:  "Many models, one API key",
	})

	// Register OpenAI
	RegisterProviderWithInfo("openai", ProviderInfo{
		Factory: func(cfg Config) (Provider, error) {
			if cfg.APIBase == "" {
				cfg.APIBase = "https://api.openai.com/v1"
			}
			return NewLiteLLMProvider(cfg), nil
		},
		DefaultModel: "gpt-4o",
		DisplayName:  "OpenAI",
		Description:  "GPT-4 and more",
	})

	// Register NVIDIA
	RegisterProviderWithInfo("nvidia", ProviderInfo{
		Factory: func(cfg Config) (Provider, error) {
			if cfg.APIBase == "" {
				cfg.APIBase = "https://integrate.api.nvidia.com/v1"
			}
			return NewLiteLLMProvider(cfg), nil
		},
		DefaultModel: "meta/llama-3.3-70b-instruct",
		DisplayName:  "NVIDIA NIM",
		Description:  "Free tier available",
	})

	// Register Groq
	RegisterProviderWithInfo("groq", ProviderInfo{
		Factory: func(cfg Config) (Provider, error) {
			if cfg.APIBase == "" {
				cfg.APIBase = "https://api.groq.com/openai/v1"
			}
			return NewLiteLLMProvider(cfg), nil
		},
		DefaultModel: "llama-3.3-70b-versatile",
		DisplayName:  "Groq",
		Description:  "Fast inference",
	})

	// Register Ollama
	RegisterProviderWithInfo("ollama", ProviderInfo{
		Factory: func(cfg Config) (Provider, error) {
			if cfg.APIBase == "" {
				cfg.APIBase = "http://localhost:11434"
			}
			return NewLiteLLMProvider(cfg), nil
		},
		DefaultModel: "llama3.1:8b",
		DisplayName:  "Ollama",
		Description:  "Local, no API key needed",
	})

	// Register Anthropic
	RegisterProviderWithInfo("anthropic", ProviderInfo{
		Factory: func(cfg Config) (Provider, error) {
			if cfg.APIBase == "" {
				cfg.APIBase = "https://api.anthropic.com/v1"
			}
			return NewLiteLLMProvider(cfg), nil
		},
		DefaultModel: "claude-sonnet-4-20250514",
		DisplayName:  "Anthropic",
		Description:  "Claude models",
	})

	// Register Azure (requires API base, no default model)
	RegisterProviderWithInfo("azure", ProviderInfo{
		Factory: func(cfg Config) (Provider, error) {
			if cfg.APIBase == "" {
				return nil, fmt.Errorf("azure provider requires api_base to be set")
			}
			return NewLiteLLMProvider(cfg), nil
		},
		DefaultModel: "",
		DisplayName:  "Azure OpenAI",
		Description:  "Enterprise Azure integration",
	})

	// Keep litellm as a generic fallback
	RegisterProvider("litellm", func(cfg Config) (Provider, error) {
		return NewLiteLLMProvider(cfg), nil
	})
}

// ProviderOption is a functional option for configuring a provider.
type ProviderOption func(*Config) error

// WithAPIKey sets the API key for the provider.
func WithAPIKey(apiKey string) ProviderOption {
	return func(cfg *Config) error {
		cfg.APIKey = apiKey
		return nil
	}
}

// WithAPIBase sets the API base URL for the provider.
func WithAPIBase(apiBase string) ProviderOption {
	return func(cfg *Config) error {
		cfg.APIBase = apiBase
		return nil
	}
}

// WithModel sets the default model for the provider.
func WithModel(model string) ProviderOption {
	return func(cfg *Config) error {
		cfg.Model = model
		return nil
	}
}

// WithTimeout sets the request timeout for the provider.
func WithTimeout(timeoutSeconds int) ProviderOption {
	return func(cfg *Config) error {
		cfg.Timeout = 0 // Will be set to default in provider
		_ = timeoutSeconds
		return nil
	}
}

// WithMaxTokens sets the default max tokens for the provider.
func WithMaxTokens(maxTokens int) ProviderOption {
	return func(cfg *Config) error {
		cfg.MaxTokens = maxTokens
		return nil
	}
}

// WithTemperature sets the default temperature for the provider.
func WithTemperature(temperature float64) ProviderOption {
	return func(cfg *Config) error {
		cfg.Temperature = temperature
		return nil
	}
}

// WithExtraHeaders sets extra headers for the provider.
func WithExtraHeaders(headers map[string]string) ProviderOption {
	return func(cfg *Config) error {
		cfg.ExtraHeaders = headers
		return nil
	}
}

// NewConfig creates a new provider configuration with the given options.
func NewConfig(options ...ProviderOption) (Config, error) {
	cfg := DefaultConfig()

	for _, opt := range options {
		if err := opt(&cfg); err != nil {
			return cfg, err
		}
	}

	return cfg, nil
}

// ToolExecutorFunc is a type for tool execution functions.
type ToolExecutorFunc func(ctx interface{}, req ToolCallRequest) (*ToolCallResponse, error)

// SimpleProviderBuilder is a helper for building providers with tools.
type SimpleProviderBuilder struct {
	cfg         Config
	toolHandler func(ctx context.Context, req ToolCallRequest) (*ToolCallResponse, error)
}

// NewSimpleProviderBuilder creates a new provider builder.
func NewSimpleProviderBuilder() *SimpleProviderBuilder {
	return &SimpleProviderBuilder{
		cfg: DefaultConfig(),
	}
}

// WithConfig sets the base configuration.
func (b *SimpleProviderBuilder) WithConfig(cfg Config) *SimpleProviderBuilder {
	b.cfg = cfg
	return b
}

// WithAPIKey sets the API key.
func (b *SimpleProviderBuilder) WithAPIKey(apiKey string) *SimpleProviderBuilder {
	b.cfg.APIKey = apiKey
	return b
}

// WithAPIBase sets the API base URL.
func (b *SimpleProviderBuilder) WithAPIBase(apiBase string) *SimpleProviderBuilder {
	b.cfg.APIBase = apiBase
	return b
}

// WithModel sets the default model.
func (b *SimpleProviderBuilder) WithModel(model string) *SimpleProviderBuilder {
	b.cfg.Model = model
	return b
}

// WithTools enables tool execution with the given handler.
func (b *SimpleProviderBuilder) WithTools(handler func(ctx context.Context, req ToolCallRequest) (*ToolCallResponse, error)) *SimpleProviderBuilder {
	b.toolHandler = handler
	return b
}

// Build builds the provider.
// If a tool handler was set, returns a LiteLLMProviderWithTools, otherwise returns a LiteLLMProvider.
func (b *SimpleProviderBuilder) Build() (Provider, error) {
	// Build appropriate provider
	if b.toolHandler != nil {
		return NewLiteLLMProviderWithTools(b.cfg, b.toolHandler), nil
	}

	return NewLiteLLMProvider(b.cfg), nil
}
