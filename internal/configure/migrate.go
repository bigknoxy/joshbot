package configure

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// MigrateToModelsConfig converts a legacy provider-centric config into the
// model-centric format and clears the legacy block, returning a human-readable
// report of what was written.
//
// The migration is all-or-nothing and refuses to be lossy: a provider it
// cannot represent faithfully (github-copilot, whose runtime provider is not
// an OpenAI-compatible endpoint; a per-provider timeout or max_retries, which
// ModelConfig has no field for; a model whose provider detection would not
// round-trip) aborts the whole run with an error naming the obstacle, because
// a migration that silently drops a setting is worse than no migration. The
// caller decides whether to save.
func (c *Configurator) MigrateToModelsConfig() ([]string, error) {
	cfg := c.cfg
	if cfg.UseModelsConfig() {
		return nil, fmt.Errorf("config already uses models_config; nothing to migrate")
	}
	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("no legacy providers configured; nothing to migrate")
	}

	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	var models []config.ModelConfig
	var report []string
	for _, name := range names {
		p := cfg.Providers[name]
		if !p.Enabled {
			report = append(report, fmt.Sprintf("skipped %s: not enabled", name))
			continue
		}
		if name == "github-copilot" {
			return nil, fmt.Errorf("cannot migrate github-copilot: its runtime provider is not an OpenAI-compatible endpoint the model-centric format can express; remove it (joshbot configure --remove github-copilot) or keep the legacy format")
		}
		if p.Timeout != 0 {
			return nil, fmt.Errorf("cannot migrate %s: providers.%s.timeout has no model-centric equivalent; clear it first or keep the legacy format", name, name)
		}
		if p.MaxRetries != nil {
			return nil, fmt.Errorf("cannot migrate %s: providers.%s.max_retries has no model-centric equivalent; clear it first or keep the legacy format", name, name)
		}

		model := p.Model
		if model == "" {
			model = providers.GetDefaultModel(name)
		}
		if model == "" {
			return nil, fmt.Errorf("cannot migrate %s: no model configured and no known default", name)
		}
		// The model-centric format routes by prefix, so the provider name
		// becomes the prefix unless the model already carries it (poolside
		// IDs carry theirs on the wire and DetectProvider recognises them).
		prefixed := model
		if config.DetectProvider(model).Name != name {
			prefixed = name + "/" + model
		}

		models = append(models, config.ModelConfig{
			Name:      name,
			Model:     prefixed,
			APIKey:    p.APIKey,
			APIKeys:   p.APIKeys,
			APIBase:   p.APIBase,
			Extra:     p.ExtraHeaders,
			ExtraBody: p.ExtraBody,
		})
		report = append(report, fmt.Sprintf("migrated %s -> model %q (wire model %q)", name, name, model))
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no enabled legacy providers to migrate")
	}

	// Agent routing: the legacy default becomes agent.model, the rest of the
	// fallback order (or every other migrated entry) becomes agent.fallback.
	primary := cfg.ProviderDefaults.Default
	if !hasModel(models, primary) {
		primary = models[0].Name
	}
	var fallback []string
	if len(cfg.ProviderDefaults.FallbackOrder) > 0 {
		for _, n := range cfg.ProviderDefaults.FallbackOrder {
			if n != primary && hasModel(models, n) {
				fallback = append(fallback, n)
			}
		}
	} else {
		for _, m := range models {
			if m.Name != primary {
				fallback = append(fallback, m.Name)
			}
		}
	}

	trial := *cfg
	trial.ModelsConfig = config.ModelsConfig{
		Models: models,
		Agent:  config.AgentModelConfig{Model: primary, Fallback: fallback},
	}
	// Round-trip check: every migrated entry must resolve, and resolve to the
	// provider it came from — a detection mismatch means requests would be
	// attributed (vision screening, notices, fallback rules) to the wrong
	// provider, so it aborts rather than migrating approximately. "custom"
	// is exempt from the name check: it is not a real upstream, and its
	// explicit api_base is what the resolution dials.
	for _, m := range models {
		resolved, err := trial.ResolveModelConfig(m.Name)
		if err != nil {
			return nil, fmt.Errorf("migration would not resolve %q: %w", m.Name, err)
		}
		if m.Name != "custom" && resolved.Provider != m.Name {
			return nil, fmt.Errorf("cannot migrate %s: model %q resolves to provider %q, not %s — migrate this entry by hand", m.Name, m.Model, resolved.Provider, m.Name)
		}
	}

	cfg.ModelsConfig = trial.ModelsConfig
	cfg.Providers = nil
	cfg.ProviderDefaults = config.ProviderDefaults{}
	report = append(report,
		fmt.Sprintf("agent.model = %q, agent.fallback = [%s]", primary, strings.Join(fallback, ", ")),
		"legacy providers block removed")
	return report, nil
}

func hasModel(models []config.ModelConfig, name string) bool {
	for _, m := range models {
		if m.Name == name {
			return true
		}
	}
	return false
}
