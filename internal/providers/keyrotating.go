package providers

import (
	"context"
	"fmt"
	"sync"
)

// KeyedProvider extends Provider with the ability to update the API key at runtime.
type KeyedProvider interface {
	Provider
	SetAPIKey(key string)
}

// KeyRotatingProvider wraps a KeyedProvider with automatic key rotation.
// On rate-limit (429), quota (402), or auth (401) errors, it rotates to the
// next key from the pool and retries once. Exhausted keys go into cooldown.
type KeyRotatingProvider struct {
	inner      KeyedProvider
	pool       *APIKeyPool
	mu         sync.Mutex
	currentKey string
}

// NewKeyRotatingProvider wraps a KeyedProvider with key rotation.
func NewKeyRotatingProvider(inner KeyedProvider, pool *APIKeyPool) *KeyRotatingProvider {
	return &KeyRotatingProvider{
		inner: inner,
		pool:  pool,
	}
}

func (p *KeyRotatingProvider) Name() string { return p.inner.Name() }

func (p *KeyRotatingProvider) Config() Config { return p.inner.Config() }

func (p *KeyRotatingProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	key := p.acquireKey()
	if key == "" {
		return "", fmt.Errorf("all API keys in cooldown")
	}
	return p.inner.Transcribe(ctx, audioData, prompt)
}

func (p *KeyRotatingProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	key := p.acquireKey()
	if key == "" {
		return nil, fmt.Errorf("all API keys in cooldown")
	}

	resp, err := p.inner.Chat(ctx, req)
	if err == nil {
		p.pool.ReportSuccess(key)
		return resp, nil
	}

	if err := p.rotateOnFailure(err); err != nil {
		return nil, err
	}

	return p.inner.Chat(ctx, req)
}

func (p *KeyRotatingProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	key := p.acquireKey()
	if key == "" {
		return nil, fmt.Errorf("all API keys in cooldown")
	}

	ch, err := p.inner.ChatStream(ctx, req)
	if err == nil {
		p.pool.ReportSuccess(key)
		return ch, nil
	}

	if err := p.rotateOnFailure(err); err != nil {
		return nil, err
	}

	return p.inner.ChatStream(ctx, req)
}

// acquireKey returns a usable key, setting it on the inner provider under lock.
// Returns empty string if no keys available.
func (p *KeyRotatingProvider) acquireKey() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.currentKey != "" {
		p.inner.SetAPIKey(p.currentKey)
		return p.currentKey
	}

	key := p.pool.Next()
	if key == "" {
		return ""
	}
	p.currentKey = key
	p.inner.SetAPIKey(key)
	return key
}

// rotateOnFailure checks if the error warrants key rotation. If so, it reports
// the failure, selects the next key, and returns nil. Returns the original
// error if rotation is not warranted or no keys are available.
func (p *KeyRotatingProvider) rotateOnFailure(err error) error {
	if !shouldRotateKey(err) {
		return err
	}

	p.mu.Lock()
	failedKey := p.currentKey
	p.mu.Unlock()

	p.pool.ReportFailure(failedKey)

	nextKey := p.pool.Next()
	if nextKey == "" {
		return fmt.Errorf("all API keys exhausted after failure: %w", err)
	}

	p.mu.Lock()
	p.currentKey = nextKey
	p.inner.SetAPIKey(nextKey)
	p.mu.Unlock()

	return nil
}

// shouldRotateKey returns true if the error indicates a key-specific failure
// (rate-limit, quota, or auth) that warrants rotation.
func shouldRotateKey(err error) bool {
	switch extractStatusCode(err.Error()) {
	case 429, 402, 401:
		return true
	}
	return false
}

// Pool returns the underlying APIKeyPool for status inspection.
func (p *KeyRotatingProvider) Pool() *APIKeyPool {
	return p.pool
}
