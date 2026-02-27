package providers

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ProviderEntry represents a single provider in the fallback chain.
type ProviderEntry struct {
	Name     string   // Provider name (e.g., "openrouter", "groq")
	Provider Provider // The actual provider instance
	Model    string   // Default model for this provider
	Priority int      // Fallback order (0 = primary, higher = later fallback)
	Enabled  bool     // Whether this provider is enabled for fallback
}

// MultiProviderConfig holds configuration for the multi-provider.
type MultiProviderConfig struct {
	DefaultProvider string
	Logger          Logger
}

// MultiProvider implements Provider with automatic fallback on transient errors.
type MultiProvider struct {
	mu              sync.RWMutex
	entries         map[string]*ProviderEntry
	orderedEntries  []*ProviderEntry
	defaultProvider string
	logger          Logger
}

// NewMultiProvider creates a new MultiProvider.
func NewMultiProvider(cfg MultiProviderConfig) *MultiProvider {
	if cfg.Logger == nil {
		cfg.Logger = &DefaultLogger{}
	}

	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "openrouter"
	}

	return &MultiProvider{
		entries:         make(map[string]*ProviderEntry),
		orderedEntries:  make([]*ProviderEntry, 0),
		defaultProvider: cfg.DefaultProvider,
		logger:          cfg.Logger,
	}
}

// Register adds a provider to the fallback chain.
func (mp *MultiProvider) Register(name string, provider Provider, model string, priority int, enabled ...bool) {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	isEnabled := true
	if len(enabled) > 0 {
		isEnabled = enabled[0]
	}

	entry := &ProviderEntry{
		Name:     name,
		Provider: provider,
		Model:    model,
		Priority: priority,
		Enabled:  isEnabled,
	}

	mp.entries[name] = entry
	mp.rebuildOrderedList()
}

// Unregister removes a provider from the chain.
func (mp *MultiProvider) Unregister(name string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	delete(mp.entries, name)
	mp.rebuildOrderedList()
}

// SetDefault sets the default provider.
func (mp *MultiProvider) SetDefault(name string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.defaultProvider = name
}

// rebuildOrderedList rebuilds the ordered entries slice sorted by priority.
func (mp *MultiProvider) rebuildOrderedList() {
	entries := make([]*ProviderEntry, 0, len(mp.entries))
	for _, entry := range mp.entries {
		entries = append(entries, entry)
	}

	// Sort by priority (bubble sort is fine for small lists)
	for i := 0; i < len(entries)-1; i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Priority > entries[j].Priority {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	mp.orderedEntries = entries
}

// Name returns the name of this provider.
func (mp *MultiProvider) Name() string {
	return "multiprovider"
}

// Config returns the configuration of the default provider.
func (mp *MultiProvider) Config() Config {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	if entry, exists := mp.entries[mp.defaultProvider]; exists {
		return entry.Provider.Config()
	}

	return DefaultConfig()
}

// Chat sends a chat request with automatic fallback.
func (mp *MultiProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	providerName, modelName := mp.parseModel(req.Model)
	providers := mp.getFallbackChain(providerName)

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	var lastErr error
	attempted := make([]string, 0, len(providers))

	for _, entry := range providers {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		tryReq := req
		tryReq.Model = mp.resolveModel(entry, modelName)

		mp.logger.Debug("Trying provider",
			"provider", entry.Name,
			"model", tryReq.Model,
			"attempt", len(attempted)+1,
		)

		resp, err := entry.Provider.Chat(ctx, tryReq)
		if err == nil {
			return resp, nil
		}

		attempted = append(attempted, entry.Name)
		lastErr = err

		mp.logger.Warn("Provider failed",
			"provider", entry.Name,
			"model", tryReq.Model,
			"error", err,
		)

		// Check if we should fallback
		if !IsFallbackError(err, entry.Name) {
			mp.logger.Debug("Non-fallback error, stopping",
				"provider", entry.Name,
				"error_type", fmt.Sprintf("%T", err),
			)
			return nil, err
		}

		mp.logger.Info("Falling back to next provider",
			"failed_provider", entry.Name,
			"reason", ClassifyError(err),
		)
	}

	return nil, fmt.Errorf("all providers failed (tried: %s): %w",
		strings.Join(attempted, " → "), lastErr)
}

// ChatStream sends a streaming chat request with fallback.
func (mp *MultiProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	providerName, modelName := mp.parseModel(req.Model)
	providers := mp.getFallbackChain(providerName)

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	var lastErr error

	for _, entry := range providers {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		tryReq := req
		tryReq.Model = mp.resolveModel(entry, modelName)

		ch, err := entry.Provider.ChatStream(ctx, tryReq)
		if err == nil {
			return ch, nil
		}

		lastErr = err

		if !IsFallbackError(err, entry.Name) {
			return nil, err
		}

		mp.logger.Info("Stream fallback",
			"failed_provider", entry.Name,
			"error", err,
		)
	}

	return nil, fmt.Errorf("all providers failed for stream: %w", lastErr)
}

// Transcribe delegates to the primary provider.
func (mp *MultiProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	mp.mu.RLock()
	entry, exists := mp.entries[mp.defaultProvider]
	mp.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("no default provider configured")
	}

	return entry.Provider.Transcribe(ctx, audioData, prompt)
}

// parseModel parses "provider:model" format.
func (mp *MultiProvider) parseModel(modelSpec string) (providerName, modelName string) {
	if modelSpec == "" {
		return mp.defaultProvider, ""
	}

	if idx := strings.Index(modelSpec, ":"); idx > 0 {
		potentialProvider := modelSpec[:idx]
		potentialModel := modelSpec[idx+1:]

		mp.mu.RLock()
		_, exists := mp.entries[potentialProvider]
		mp.mu.RUnlock()

		if exists {
			return potentialProvider, potentialModel
		}
	}

	return mp.defaultProvider, modelSpec
}

// resolveModel determines the model to use for a provider.
func (mp *MultiProvider) resolveModel(entry *ProviderEntry, requestedModel string) string {
	if requestedModel != "" {
		return requestedModel
	}
	if entry.Model != "" {
		return entry.Model
	}
	return entry.Provider.Config().Model
}

// getFallbackChain returns providers in fallback order, excluding disabled providers.
func (mp *MultiProvider) getFallbackChain(startProvider string) []*ProviderEntry {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	result := make([]*ProviderEntry, 0, len(mp.orderedEntries))
	seen := make(map[string]bool)

	// Start with specified provider (only if enabled)
	if entry, exists := mp.entries[startProvider]; exists && entry.Enabled {
		result = append(result, entry)
		seen[startProvider] = true
	}

	// Add remaining enabled providers by priority
	for _, entry := range mp.orderedEntries {
		if !seen[entry.Name] && entry.Enabled {
			result = append(result, entry)
			seen[entry.Name] = true
		}
	}

	return result
}

// GetProviderNames returns all registered provider names.
func (mp *MultiProvider) GetProviderNames() []string {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	names := make([]string, 0, len(mp.entries))
	for name := range mp.entries {
		names = append(names, name)
	}
	return names
}

// HasProvider returns true if a provider is registered and enabled.
func (mp *MultiProvider) HasProvider(name string) bool {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	entry, exists := mp.entries[name]
	return exists && entry.Enabled
}

// SetEnabled enables or disables a provider in the fallback chain.
func (mp *MultiProvider) SetEnabled(name string, enabled bool) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if entry, exists := mp.entries[name]; exists {
		entry.Enabled = enabled
		mp.rebuildOrderedList()
	}
}
