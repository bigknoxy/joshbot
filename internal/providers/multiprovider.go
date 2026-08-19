package providers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ProviderEntry represents a single provider in the fallback chain.
type ProviderEntry struct {
	Name     string   // Provider name (e.g., "openrouter", "groq")
	Provider Provider // The actual provider instance
	Model    string   // Default model for this provider
	Priority int      // Fallback order (0 = primary, higher = later fallback)
	Enabled  bool     // Whether this provider is enabled for fallback
	// MaxRetries is the same-provider retry budget for fallback-class
	// failures. Zero (the default here) means fail over immediately; the
	// joshbot binary raises it to DefaultMaxRetries at registration time.
	MaxRetries int
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
	// health tracks consecutive failures per provider so a known-dead
	// provider is deprioritized instead of re-dialled (and timed out) on
	// every turn. Process-local by design.
	health map[string]*providerHealth
	// now and sleep are injection points for tests; production uses
	// time.Now and sleepCtx.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
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
		health:          make(map[string]*providerHealth),
		now:             time.Now,
		sleep:           sleepCtx,
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

// Clear removes all registered providers.
func (mp *MultiProvider) Clear() {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.entries = make(map[string]*ProviderEntry)
	mp.orderedEntries = nil
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

// screenForImages applies the image rules that can only be checked once the
// fallback chain is known, before any request is dialled.
//
// Two things happen here. The total-payload limit is enforced across the whole
// request, and candidates whose model cannot accept an image are dropped from
// the chain rather than dialled — a provider rejects an image_url part as an
// opaque 400 that reads as a joshbot bug, and falling back to a text-only model
// would silently answer as though no image had been sent. If that empties the
// chain, the error names every model tried and how to change it.
func (mp *MultiProvider) screenForImages(req ChatRequest, providers []*ProviderEntry, modelName, addressed string) ([]*ProviderEntry, error) {
	if !RequestHasImages(req) {
		return providers, nil
	}
	var images []Image
	for _, m := range req.Messages {
		images = append(images, m.Images...)
	}
	if err := ValidateImages(images); err != nil {
		return nil, err
	}

	capable := make([]*ProviderEntry, 0, len(providers))
	tried := make([]string, 0, len(providers))
	for _, entry := range providers {
		model := mp.resolveModel(entry, modelName, entry.Name == addressed)
		if SupportsVision(model) {
			capable = append(capable, entry)
			continue
		}
		tried = append(tried, model)
	}
	if len(capable) == 0 {
		return nil, &ErrVisionUnsupported{Models: tried}
	}
	return capable, nil
}

// screenForDocuments applies the document rules that can only be checked once
// the fallback chain is known, before any request is dialled.
//
// It is deliberately a separate function from screenForImages rather than a
// branch inside it: reading an image and reading a PDF are different model
// capabilities (llava reads images and no documents), so the two chains are
// filtered against two different lists and either can empty the chain on its
// own with its own error.
func (mp *MultiProvider) screenForDocuments(req ChatRequest, providers []*ProviderEntry, modelName, addressed string) ([]*ProviderEntry, error) {
	if !RequestHasDocuments(req) {
		return providers, nil
	}
	var docs []Document
	for _, m := range req.Messages {
		docs = append(docs, m.Documents...)
	}
	if err := ValidateDocuments(docs); err != nil {
		return nil, err
	}

	capable := make([]*ProviderEntry, 0, len(providers))
	tried := make([]string, 0, len(providers))
	for _, entry := range providers {
		model := mp.resolveModel(entry, modelName, entry.Name == addressed)
		if SupportsDocuments(model) {
			capable = append(capable, entry)
			continue
		}
		tried = append(tried, model)
	}
	if len(capable) == 0 {
		return nil, &ErrDocumentsUnsupported{Models: tried}
	}
	return capable, nil
}

// Chat sends a chat request with automatic fallback.
func (mp *MultiProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	providerName, modelName := mp.parseModel(req.Model)
	providerName = mp.effectiveAddressed(providerName)
	providers := mp.getFallbackChain(providerName)

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	providers, err := mp.screenForImages(req, providers, modelName, providerName)
	if err != nil {
		return nil, err
	}

	providers, err = mp.screenForDocuments(req, providers, modelName, providerName)
	if err != nil {
		return nil, err
	}

	var lastErr error
	attempted := make([]string, 0, len(providers))
	failures := make([]string, 0, len(providers))
	// addressedErrClass records why the addressed provider failed this turn,
	// for the fallback notice. Empty means it was never dialled before a
	// fallback answered (deprioritized by cooldown).
	addressedErrClass := ""

	for _, entry := range providers {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		tryReq := req
		tryReq.Model = mp.resolveModel(entry, modelName, entry.Name == providerName)

		mp.logger.Debug("Trying provider",
			"provider", entry.Name,
			"model", tryReq.Model,
			"attempt", len(attempted)+1,
		)

		var resp *ChatResponse
		err := mp.tryWithRetry(ctx, entry.Name, func() error {
			r, callErr := entry.Provider.Chat(ctx, tryReq)
			if callErr == nil {
				resp = r
			}
			return callErr
		})
		if err == nil {
			mp.markSuccess(entry.Name)
			notifyFallback(ctx, providerName, entry.Name, tryReq.Model, addressedErrClass)
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if entry.Name == providerName {
			addressedErrClass = ClassifyError(err)
		}

		attempted = append(attempted, entry.Name)
		lastErr = err
		failures = append(failures, fmt.Sprintf("%s: %v", entry.Name, err))

		mp.logger.Warn("Provider failed",
			"provider", entry.Name,
			"model", tryReq.Model,
			"error", err,
		)

		// A non-fallback error from the provider the request was addressed to
		// is the answer: nothing later in the chain can improve on it. From a
		// fallback it is not — returning it discards the primary's failure and
		// reports a provider the user never asked for, so keep going and let
		// the aggregate error below name the whole chain.
		if !IsFallbackError(err, entry.Name) {
			mp.logger.Debug("Non-fallback error",
				"provider", entry.Name,
				"error_type", fmt.Sprintf("%T", err),
			)
			if entry.Name == providerName {
				return nil, err
			}
			continue
		}

		mp.markFailure(entry.Name, err)
		mp.logger.Info("Falling back to next provider",
			"failed_provider", entry.Name,
			"reason", ClassifyError(err),
		)
	}

	return nil, fmt.Errorf("all providers failed (tried: %s; %s): %w",
		strings.Join(attempted, " → "), strings.Join(failures, "; "), lastErr)
}

// ChatStream sends a streaming chat request with fallback.
func (mp *MultiProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	providerName, modelName := mp.parseModel(req.Model)
	providerName = mp.effectiveAddressed(providerName)
	providers := mp.getFallbackChain(providerName)

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	providers, err := mp.screenForImages(req, providers, modelName, providerName)
	if err != nil {
		return nil, err
	}

	providers, err = mp.screenForDocuments(req, providers, modelName, providerName)
	if err != nil {
		return nil, err
	}

	var lastErr error
	failures := make([]string, 0, len(providers))
	// See the note in Chat: empty means the addressed provider was never
	// dialled before a fallback answered.
	addressedErrClass := ""

	for _, entry := range providers {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		tryReq := req
		tryReq.Model = mp.resolveModel(entry, modelName, entry.Name == providerName)

		var ch <-chan StreamChunk
		err := mp.tryWithRetry(ctx, entry.Name, func() error {
			c, callErr := entry.Provider.ChatStream(ctx, tryReq)
			if callErr == nil {
				ch = c
			}
			return callErr
		})
		if err == nil {
			mp.markSuccess(entry.Name)
			notifyFallback(ctx, providerName, entry.Name, tryReq.Model, addressedErrClass)
			return ch, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if entry.Name == providerName {
			addressedErrClass = ClassifyError(err)
		}

		lastErr = err
		failures = append(failures, fmt.Sprintf("%s: %v", entry.Name, err))

		// See the note in Chat: a fallback's non-fallback error must not stand
		// in for the primary's.
		if !IsFallbackError(err, entry.Name) {
			if entry.Name == providerName {
				return nil, err
			}
			continue
		}

		mp.markFailure(entry.Name, err)
		mp.logger.Info("Stream fallback",
			"failed_provider", entry.Name,
			"error", err,
		)
	}

	return nil, fmt.Errorf("all providers failed for stream (%s): %w",
		strings.Join(failures, "; "), lastErr)
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

// parseModel resolves a model specification to a provider name and model name.
// It handles "provider:model" format and direct provider name lookups.
//
// The read lock is held across the whole body, not reacquired per lookup. Both
// defaultProvider reads used to sit outside it, so a config reload calling
// SetDefault raced any in-flight turn — including the learning consolidator's
// own Chat, which runs on a background goroutine for the lifetime of the
// process. Taking and dropping the lock twice also let a reload land between
// the entries lookup and the default read, resolving against a chain that no
// longer existed. Nothing called here locks, so a single span is safe.
func (mp *MultiProvider) parseModel(modelSpec string) (providerName, modelName string) {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	if modelSpec == "" {
		return mp.defaultProvider, ""
	}

	// Check for "provider:model" format
	if idx := strings.Index(modelSpec, ":"); idx > 0 {
		potentialProvider := modelSpec[:idx]
		potentialModel := modelSpec[idx+1:]

		if _, exists := mp.entries[potentialProvider]; exists {
			return potentialProvider, potentialModel
		}
	}

	// Check if modelSpec is itself a registered provider name (e.g., "smart")
	if _, exists := mp.entries[modelSpec]; exists {
		return modelSpec, ""
	}

	return mp.defaultProvider, modelSpec
}

// resolveModel determines the model to use for a provider.
//
// A model ID belongs to the provider that publishes it, so the requested model
// is honoured only by the provider the request was addressed to. A fallback is
// asked for its own model instead: sending openrouter's "z-ai/glm-5.2" on to
// poolside earns a 404 ("please check the model you provided") that reads as a
// joshbot misconfiguration and hides the rate limit that caused the fallback.
// The requested model is still the last resort for a provider that has no model
// of its own configured — better a wrong guess than an empty model field.
func (mp *MultiProvider) resolveModel(entry *ProviderEntry, requestedModel string, addressed bool) string {
	if requestedModel != "" && addressed {
		return requestedModel
	}
	if entry.Model != "" {
		return entry.Model
	}
	if m := entry.Provider.Config().Model; m != "" {
		return m
	}
	return requestedModel
}

// effectiveAddressed resolves which provider a request is considered
// addressed to. An unregistered or disabled name — a stale
// provider_defaults.default, or the constructor's "openrouter" default in a
// config that never registered openrouter — must not become the addressed
// provider: the fallback notice would blame a provider that does not exist,
// and the addressed-only rules (honouring the requested model, returning a
// non-fallback error immediately) would apply to nobody. The
// highest-priority enabled entry stands in.
func (mp *MultiProvider) effectiveAddressed(name string) string {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	if e, ok := mp.entries[name]; ok && e.Enabled {
		return name
	}
	for _, e := range mp.orderedEntries {
		if e.Enabled {
			return e.Name
		}
	}
	return name
}

// tryWithRetry runs call up to 1 + the provider's retry budget times,
// retrying only fallback-class errors and pacing each retry by the upstream
// Retry-After when given, otherwise jittered exponential backoff. Returns the
// last error; a cancelled context ends the retries immediately.
func (mp *MultiProvider) tryWithRetry(ctx context.Context, name string, call func() error) error {
	maxRetries := mp.maxRetriesFor(name)

	var err error
	for attempt := 0; ; attempt++ {
		err = call()
		if err == nil {
			return nil
		}
		if attempt >= maxRetries || !IsFallbackError(err, name) {
			return err
		}
		delay, ok := retryDelay(err, attempt)
		if !ok {
			// The upstream asked for a longer wait than a turn should
			// stall for: hand the error back so the caller cools this
			// provider down and moves along the chain now.
			return err
		}
		mp.logger.Info("Retrying provider after transient error",
			"provider", name,
			"attempt", attempt+1,
			"max_retries", maxRetries,
			"delay", delay.String(),
			"reason", ClassifyError(err),
		)
		if sleepErr := mp.sleep(ctx, delay); sleepErr != nil {
			return err
		}
	}
}

// getFallbackChain returns providers in fallback order, excluding disabled
// providers. Providers in cooldown are moved to the end of the chain rather
// than dropped: a healthy fallback answers first, but a chain that is all
// cooling still gets dialled — a cooldown costs latency, never availability.
func (mp *MultiProvider) getFallbackChain(startProvider string) []*ProviderEntry {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	ordered := make([]*ProviderEntry, 0, len(mp.orderedEntries))
	seen := make(map[string]bool)

	// Start with specified provider (only if enabled)
	if entry, exists := mp.entries[startProvider]; exists && entry.Enabled {
		ordered = append(ordered, entry)
		seen[startProvider] = true
	}

	// Add remaining enabled providers by priority
	for _, entry := range mp.orderedEntries {
		if !seen[entry.Name] && entry.Enabled {
			ordered = append(ordered, entry)
			seen[entry.Name] = true
		}
	}

	now := mp.now()
	result := make([]*ProviderEntry, 0, len(ordered))
	cooling := make([]*ProviderEntry, 0)
	for _, entry := range ordered {
		if h := mp.health[entry.Name]; h != nil && now.Before(h.coolUntil) {
			cooling = append(cooling, entry)
			continue
		}
		result = append(result, entry)
	}
	for _, entry := range cooling {
		mp.logger.Debug("Provider in cooldown, deprioritized",
			"provider", entry.Name,
			"cool_until", mp.health[entry.Name].coolUntil.Format(time.RFC3339),
		)
	}
	return append(result, cooling...)
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
