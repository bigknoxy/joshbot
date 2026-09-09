package tools

import (
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/cooldown"
)

// webBackendHealth tracks consecutive failures per (operation, backend) pair
// for the web tool's fallback chains. It is modeled directly on
// internal/providers/health.go's providerHealth: process-local, deliberately
// forgetful (any success clears the whole failure history), and a cooldown
// only deprioritizes a backend — it moves to the back of the try order, it is
// never dropped, so a wrong guess costs latency, never the capability
// entirely. The backoff formula itself (including the overflow-safe shift
// clamp) is shared with providerHealth via internal/cooldown rather than
// hand-copied here a second time — only the state-tracking shape (this
// struct, its map, its mutex) is duplicated, since the two have different
// concurrency needs and keying (per provider name here vs. per
// operation+backend pair there).
//
// It is keyed per operation ("web_search", "web_code", "web_company",
// "web_research") as well as per backend name ("exa-cli", "exa-mcp",
// "duckduckgo") because a backend failing for one operation says nothing
// about its health for another — exa-cli's `code` subcommand can be down
// while `search` still works.
type webBackendHealth struct {
	mu    sync.Mutex
	state map[string]map[string]*webBackendState
	// now is overridable in tests so cooldown expiry can be asserted without
	// a real sleep, mirroring MultiProvider.now.
	now func() time.Time
}

// webBackendState is one backend's failure/cooldown bookkeeping for one
// operation.
type webBackendState struct {
	failures  int
	coolUntil time.Time
}

// Cooldown tuning. Deliberately smaller, and quicker to trigger, than
// internal/providers/health.go's: a web-tool fallback chain already tries
// every backend within the same turn, so unlike a provider (which is retried
// in place before the chain even considers moving on) a single failed leg is
// enough signal to try the next backend first on the *next* call — the
// current call has already moved on to its own next tier regardless. And the
// whole window is seconds, not minutes: a cooldown that outlived a
// conversation would be worse than never deprioritizing at all.
const (
	webCooldownThreshold = 1
	webCooldownBase      = 10 * time.Second
	webCooldownMax       = 2 * time.Minute
	// webCooldownMaxShift clamps cooldown.Backoff's exponent: 10s << 3
	// already exceeds webCooldownMax, so 3 loses no ceiling.
	webCooldownMaxShift = 3
)

// newWebBackendHealth constructs an empty, ready-to-use health map.
func newWebBackendHealth() *webBackendHealth {
	return &webBackendHealth{
		state: make(map[string]map[string]*webBackendState),
		now:   time.Now,
	}
}

func (h *webBackendHealth) stateLocked(op, backend string) *webBackendState {
	byBackend, ok := h.state[op]
	if !ok {
		byBackend = make(map[string]*webBackendState)
		h.state[op] = byBackend
	}
	s, ok := byBackend[backend]
	if !ok {
		s = &webBackendState{}
		byBackend[backend] = s
	}
	return s
}

// markFailure records a failed attempt against a backend for one operation
// and computes its cooldown window once the failure count crosses the
// threshold.
func (h *webBackendHealth) markFailure(op, backend string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.stateLocked(op, backend)
	s.failures++
	// The backoff formula (including the overflow-safe shift clamp) lives in
	// internal/cooldown, shared with internal/providers' provider health, so
	// the two do not drift into two subtly different copies of the same
	// algorithm.
	cool := cooldown.Backoff(s.failures, webCooldownThreshold, 0, webCooldownBase, webCooldownMax, webCooldownMaxShift)
	if cool > 0 {
		s.coolUntil = h.now().Add(cool)
	}
}

// markSuccess clears a backend's failure history for one operation.
func (h *webBackendHealth) markSuccess(op, backend string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	byBackend, ok := h.state[op]
	if !ok {
		return
	}
	if s, ok := byBackend[backend]; ok && (s.failures > 0 || !s.coolUntil.IsZero()) {
		byBackend[backend] = &webBackendState{}
	}
}

// inCooldown reports whether a backend is currently deprioritized for one
// operation.
func (h *webBackendHealth) inCooldown(op, backend string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	byBackend, ok := h.state[op]
	if !ok {
		return false
	}
	s, ok := byBackend[backend]
	if !ok {
		return false
	}
	return h.now().Before(s.coolUntil)
}

// orderedBackends returns names in try order for one operation: every
// healthy backend first (in its original relative order), then every
// cooled-down backend (also in its original relative order). It never
// mutates names — callers pass shared package-level slices — and it is a
// stable two-pass partition rather than a sort, since the only ordering rule
// is the boolean "currently cooled down or not".
func (h *webBackendHealth) orderedBackends(op string, names []string) []string {
	h.mu.Lock()
	byBackend := h.state[op]
	cooled := make(map[string]bool, len(names))
	now := h.now()
	for _, n := range names {
		if byBackend == nil {
			continue
		}
		if s, ok := byBackend[n]; ok && now.Before(s.coolUntil) {
			cooled[n] = true
		}
	}
	h.mu.Unlock()

	ordered := make([]string, 0, len(names))
	var deferred []string
	for _, n := range names {
		if cooled[n] {
			deferred = append(deferred, n)
		} else {
			ordered = append(ordered, n)
		}
	}
	return append(ordered, deferred...)
}
