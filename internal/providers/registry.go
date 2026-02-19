package providers

import (
	"context"
	"fmt"
	"sync"
)

// ProviderFactory is a function that creates a Provider with the given configuration.
type ProviderFactory func(cfg Config) (Provider, error)

// registry is the global provider registry.
var (
	registry     = make(map[string]ProviderFactory)
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

	registry[name] = factory
}

// GetProvider returns a new provider instance by name with the given configuration.
// Returns an error if the provider is not registered.
func GetProvider(name string, cfg Config) (Provider, error) {
	registryLock.RLock()
	factory, exists := registry[normalizeProviderName(name)]
	registryLock.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown provider: %s (available: %v)", name, AvailableProviders())
	}

	return factory(cfg)
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
	default:
		return name
	}
}

// init registers the built-in providers.
func init() {
	// Register the LiteLLM provider as the default
	RegisterProvider("litellm", func(cfg Config) (Provider, error) {
		return NewLiteLLMProvider(cfg), nil
	})

	// Register common provider aliases that use LiteLLM
	RegisterProvider("openrouter", func(cfg Config) (Provider, error) {
		// Set default API base for OpenRouter if not specified
		if cfg.APIBase == "" {
			cfg.APIBase = "https://openrouter.ai/api/v1"
		}
		return NewLiteLLMProvider(cfg), nil
	})

	RegisterProvider("openai", func(cfg Config) (Provider, error) {
		// Set default API base for OpenAI if not specified
		if cfg.APIBase == "" {
			cfg.APIBase = "https://api.openai.com/v1"
		}
		return NewLiteLLMProvider(cfg), nil
	})

	RegisterProvider("anthropic", func(cfg Config) (Provider, error) {
		// Set default API base for Anthropic if not specified
		if cfg.APIBase == "" {
			cfg.APIBase = "https://api.anthropic.com/v1"
		}
		return NewLiteLLMProvider(cfg), nil
	})

	RegisterProvider("azure", func(cfg Config) (Provider, error) {
		// Azure OpenAI requires specific configuration
		if cfg.APIBase == "" {
			return nil, fmt.Errorf("azure provider requires api_base to be set")
		}
		return NewLiteLLMProvider(cfg), nil
	})

	RegisterProvider("ollama", func(cfg Config) (Provider, error) {
		// Default to local Ollama instance
		if cfg.APIBase == "" {
			cfg.APIBase = "http://localhost:11434"
		}
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
