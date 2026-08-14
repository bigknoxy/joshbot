package providers

import (
	"context"
	"math/rand"
	"time"
)

// Retry and cooldown tuning. A transient blip should be retried on the same
// provider before the chain moves on — falling over switches the model the
// user is talking to, which is a bigger quality change than a short wait.
const (
	// retryBaseDelay seeds the exponential backoff between same-provider
	// retries when the upstream gave no Retry-After.
	retryBaseDelay = 500 * time.Millisecond
	// maxInTurnWait caps how long a single turn will wait before a retry. A
	// Retry-After longer than this is not worth stalling the conversation
	// for: the provider goes into cooldown for that span and the chain moves
	// on immediately.
	maxInTurnWait = 20 * time.Second
	// cooldownThreshold is how many consecutive exhausted failures a provider
	// accrues before it is deprioritized in the chain.
	cooldownThreshold = 2
	// cooldownBase and cooldownMax bound the deprioritization window when the
	// upstream gave no Retry-After to seed it.
	cooldownBase = 15 * time.Second
	cooldownMax  = 5 * time.Minute
)

// providerHealth tracks consecutive failures for one provider. It is
// process-local and deliberately forgetful: any success resets it, and a
// cooldown only deprioritizes the provider (it is still tried when everything
// healthier has failed), so a wrong guess costs latency, never availability.
type providerHealth struct {
	failures  int
	coolUntil time.Time
	lastErr   string
}

// ProviderHealthInfo is a read-only snapshot for status displays.
type ProviderHealthInfo struct {
	Name      string
	Failures  int
	CoolUntil time.Time
	LastErr   string
}

// retryDelay picks the wait before retrying the same provider. An upstream
// Retry-After wins when present; otherwise exponential backoff with jitter so
// concurrent turns do not retry in lockstep. A second return of false means
// the wait would exceed maxInTurnWait and the caller should fall over now.
func retryDelay(err error, attempt int) (time.Duration, bool) {
	if ra := RetryAfterFromError(err); ra > 0 {
		if ra > maxInTurnWait {
			return 0, false
		}
		return ra, true
	}
	d := retryBaseDelay << attempt
	// Up to 25% jitter.
	d += time.Duration(rand.Int63n(int64(d)/4 + 1))
	if d > maxInTurnWait {
		d = maxInTurnWait
	}
	return d, true
}

// sleepCtx waits for d or until the context is done, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// markFailure records an exhausted (post-retry) fallback-class failure and
// computes the provider's cooldown window: the upstream Retry-After when
// given, otherwise exponential in the consecutive-failure count once the
// threshold is crossed.
func (mp *MultiProvider) markFailure(name string, err error) {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	h := mp.health[name]
	if h == nil {
		h = &providerHealth{}
		mp.health[name] = h
	}
	h.failures++
	h.lastErr = ClassifyError(err)

	cool := time.Duration(0)
	if ra := RetryAfterFromError(err); ra > 0 {
		cool = ra
	} else if h.failures >= cooldownThreshold {
		// The shift is clamped before shifting, not only the result: an
		// unbounded exponent overflows the int64 duration negative at ~40
		// consecutive failures, and a negative cool would silently drop the
		// cooldown for exactly the provider that is most persistently down.
		// 15s << 5 already exceeds cooldownMax, so 5 loses nothing.
		shift := h.failures - cooldownThreshold
		if shift > 5 {
			shift = 5
		}
		cool = cooldownBase << shift
	}
	if cool > cooldownMax {
		cool = cooldownMax
	}
	if cool > 0 {
		h.coolUntil = mp.now().Add(cool)
	}
}

// markSuccess clears a provider's failure history.
func (mp *MultiProvider) markSuccess(name string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if h := mp.health[name]; h != nil && (h.failures > 0 || !h.coolUntil.IsZero()) {
		mp.health[name] = &providerHealth{}
	}
}

// inCooldown reports whether a provider is currently deprioritized.
func (mp *MultiProvider) inCooldown(name string) bool {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	h := mp.health[name]
	return h != nil && mp.now().Before(h.coolUntil)
}

// DefaultMaxRetries is the same-provider retry budget the joshbot binary
// applies to every registered provider unless providers.<name>.max_retries
// overrides it. It lives at the wiring layer, not in Register, so that a
// bare MultiProvider (as tests and library callers build) retries nothing
// unless asked to.
const DefaultMaxRetries = 2

// maxRetriesFor returns the per-provider retry budget (zero = no retries).
func (mp *MultiProvider) maxRetriesFor(name string) int {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	if entry, ok := mp.entries[name]; ok {
		return entry.MaxRetries
	}
	return 0
}

// SetMaxRetries overrides the same-provider retry budget for one provider.
// Zero means fail over immediately, matching providers.<name>.max_retries.
func (mp *MultiProvider) SetMaxRetries(name string, n int) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if entry, ok := mp.entries[name]; ok {
		entry.MaxRetries = n
	}
}

// HealthSnapshot returns the current failure/cooldown state of every
// registered provider that has one, for status displays. Healthy providers
// are omitted.
func (mp *MultiProvider) HealthSnapshot() []ProviderHealthInfo {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	infos := make([]ProviderHealthInfo, 0, len(mp.health))
	for name, h := range mp.health {
		if h.failures == 0 && h.coolUntil.IsZero() {
			continue
		}
		infos = append(infos, ProviderHealthInfo{
			Name:      name,
			Failures:  h.failures,
			CoolUntil: h.coolUntil,
			LastErr:   h.lastErr,
		})
	}
	return infos
}
