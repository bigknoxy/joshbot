// Package cooldown holds the one shared piece of algorithm behind two
// independent failure-tracking implementations: internal/providers' per-
// provider fallback health (providerHealth) and internal/tools' per-web-
// backend health (webBackendHealth). Both need "how long should a
// repeatedly-failing key be deprioritized for", and both used to compute it
// with their own hand-written, near-identical exponential backoff — right
// down to the same overflow-clamp trick — that could silently drift apart on
// the next change to either. Backoff is the one copy; a fix to the formula
// here reaches both callers.
//
// This package deliberately does not also own the state (the failure count,
// the per-key map, the mutex): providers.MultiProvider and internal/tools'
// webBackendHealth each have their own concurrency shape and existing test
// surface built around it, and unifying that too would be a much larger,
// riskier change for no algorithmic benefit. Backoff is a pure function; the
// two callers still track failure counts and cooldown timestamps themselves.
package cooldown

import "time"

// Backoff computes a deprioritization window from a consecutive-failure
// count. explicit, when positive (an upstream-provided duration such as a
// parsed Retry-After header), takes precedence over the computed backoff —
// mirroring providers.markFailure's existing precedence rule. Otherwise, once
// failures reaches threshold, the window grows as base shifted left by
// (failures-threshold), capped at max. Below threshold, and with no explicit
// duration, the result is zero (no cooldown).
//
// The shift is clamped to maxShift before shifting, not only the result:
// with no clamp at all, a large enough failure count shifts base past an
// int64 duration's range and wraps negative, which would silently drop the
// cooldown for exactly the key that is most persistently failing. Choose
// maxShift so base<<maxShift already exceeds max — anything past that loses
// no ceiling, so the clamp costs nothing in practice while removing the
// overflow entirely.
func Backoff(failures, threshold int, explicit, base, max time.Duration, maxShift int) time.Duration {
	cool := explicit
	if cool <= 0 && failures >= threshold {
		shift := failures - threshold
		if shift > maxShift {
			shift = maxShift
		}
		cool = base << shift
	}
	if cool > max {
		cool = max
	}
	if cool < 0 {
		cool = 0
	}
	return cool
}
