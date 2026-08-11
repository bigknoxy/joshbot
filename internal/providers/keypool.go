package providers

import (
	"sync"
	"time"
)

// KeyState tracks the state of a single API key.
type KeyState struct {
	Key        string
	CooldownAt time.Time
	FailCount  int
}

// APIKeyPool manages a pool of API keys with rotation and cooldown.
type APIKeyPool struct {
	mu                    sync.Mutex
	keys                  []*KeyState
	cooldownDur           time.Duration
	cooldownAfterFailures int
	nextIdx               int
}

// NewAPIKeyPool creates a key pool. cooldown is how long a key stays in cooldown
// after exceeding the failure limit. cooldownAfterFailures is the number of
// failures before a key enters cooldown (default 3 if ≤ 0).
func NewAPIKeyPool(keys []string, cooldown time.Duration, cooldownAfterFailures int) *APIKeyPool {
	states := make([]*KeyState, len(keys))
	for i, k := range keys {
		states[i] = &KeyState{Key: k}
	}
	if cooldown <= 0 {
		cooldown = 24 * time.Hour
	}
	if cooldownAfterFailures <= 0 {
		cooldownAfterFailures = 3
	}
	return &APIKeyPool{
		keys:                  states,
		cooldownDur:           cooldown,
		cooldownAfterFailures: cooldownAfterFailures,
	}
}

// Next returns the next available (non-cooldown) key, or empty string if none available.
func (p *APIKeyPool) Next() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.keys) == 0 {
		return ""
	}

	start := p.nextIdx
	for i := 0; i < len(p.keys); i++ {
		idx := (start + i) % len(p.keys)
		ks := p.keys[idx]
		if time.Now().Before(ks.CooldownAt) {
			continue
		}
		p.nextIdx = (idx + 1) % len(p.keys)
		return ks.Key
	}
	return ""
}

// Len returns the total number of keys in the pool.
func (p *APIKeyPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keys)
}

// Available returns the count of keys not in cooldown.
func (p *APIKeyPool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	count := 0
	now := time.Now()
	for _, ks := range p.keys {
		if now.After(ks.CooldownAt) {
			count++
		}
	}
	return count
}

// ReportFailure marks a key as having failed. Puts key in cooldown after
// cooldownAfterFailures failures.
func (p *APIKeyPool) ReportFailure(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, ks := range p.keys {
		if ks.Key != key {
			continue
		}
		ks.FailCount++
		if ks.FailCount >= p.cooldownAfterFailures {
			ks.CooldownAt = time.Now().Add(p.cooldownDur)
		}
		return
	}
}

// ReportSuccess resets the failure count for a key.
func (p *APIKeyPool) ReportSuccess(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, ks := range p.keys {
		if ks.Key == key {
			ks.FailCount = 0
			return
		}
	}
}

// ReportCooldown immediately places a key into cooldown.
func (p *APIKeyPool) ReportCooldown(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, ks := range p.keys {
		if ks.Key == key {
			ks.CooldownAt = time.Now().Add(p.cooldownDur)
			ks.FailCount = 0
			return
		}
	}
}
