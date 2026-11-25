package main

import (
	"strings"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/configure"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// providerModelFor returns the model a provider should answer with.
//
// A provider's own configured model wins. Otherwise the model comes from the
// global agents.defaults.model — but only when that model id *belongs* to
// this provider: it is the provider's registered default, or it carries the
// provider's name. Every other provider falls back to its own registry
// default.
//
// That distinction is not a nicety. A fresh install whose only credential is
// an NVIDIA key in the environment registers nvidia and no openrouter, while
// agents.defaults.model still holds the global default "openrouter/free".
// NVIDIA answered 404 "page not found", and the error told the user to check
// a model name that was never wrong. The same shape bit a *fallback*: a
// provider in the chain inherited the primary's model id and answered 404 for
// a model it does not serve.
func providerModelFor(cfg *config.Config, name string, p config.ProviderConfig) string {
	if p.Model != "" {
		return p.Model
	}
	canonical := providers.CanonicalProviderName(name)
	fallback := providers.GetDefaultModel(canonical)
	if cfg == nil {
		return fallback
	}
	global := cfg.Agents.Defaults.Model
	if global == "" {
		return fallback
	}
	if modelBelongsTo(global, canonical) {
		return global
	}
	// A model id no provider claims is the operator's own — it belongs to
	// whichever provider they made primary, and there is nothing else this
	// provider could answer with.
	if !anyProviderClaims(global) {
		return global
	}
	return fallback
}

// modelBelongsTo reports whether a model id belongs to a provider: it is
// that provider's registered default, or it carries the provider's name.
func modelBelongsTo(modelID, providerName string) bool {
	canonical := providers.CanonicalProviderName(providerName)
	return modelID == providers.GetDefaultModel(canonical) ||
		strings.HasPrefix(modelID, canonical+"/")
}

// anyProviderClaims reports whether any known provider's registered default
// or name-prefixed model is this model id.
func anyProviderClaims(modelID string) bool {
	for _, name := range configure.SupportedProviders() {
		if modelBelongsTo(modelID, name) {
			return true
		}
	}
	return false
}
