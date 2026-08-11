package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Profile is a named endpoint/model setup an operator can switch between with
// `--profile`, instead of editing the config file to move between (say) a
// hosted provider and a local Ollama.
//
// A profile deliberately cannot hold a credential. APIKey exists only so that a
// config carrying one is rejected at load with an error naming the field: a
// profile is the thing most likely to be pasted into a chat or committed to a
// dotfiles repo, so silently accepting a secret here would defeat the point of
// api_key_env entirely. See validateProfiles.
type Profile struct {
	// Provider names the provider the model belongs to. It is used as the
	// model's routing prefix when Model carries none, so "openrouter" plus
	// "z-ai/glm-4.6" resolves the same way "openrouter/z-ai/glm-4.6" does.
	Provider string `mapstructure:"provider" json:"provider" yaml:"provider"`
	// Model is the model name, with or without a provider prefix.
	Model string `mapstructure:"model" json:"model" yaml:"model"`
	// APIBase overrides the provider's default endpoint. It is required for a
	// provider joshbot does not know a URL for.
	APIBase string `mapstructure:"api_base" json:"api_base,omitempty" yaml:"api_base,omitempty"`
	// APIKeyEnv names the environment variable holding the credential. Empty
	// means the endpoint needs none, which is the ordinary case for a local
	// Ollama.
	APIKeyEnv string `mapstructure:"api_key_env" json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
	// APIKey is never used. It is present so that a raw credential written here
	// is a load error rather than a secret nobody notices.
	APIKey string `mapstructure:"api_key" json:"api_key,omitempty" yaml:"api_key,omitempty"`
	// Description is free text shown by `joshbot profiles list`.
	Description string `mapstructure:"description" json:"description,omitempty" yaml:"description,omitempty"`
	// Disabled keeps a profile in the file but refuses to select it.
	Disabled bool `mapstructure:"disabled" json:"disabled,omitempty" yaml:"disabled,omitempty"`
}

// ProfileNames returns the configured profile names in a stable order, for
// error messages and for `joshbot profiles list`.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SelectProfile resolves which profile a run should use.
//
// Precedence, highest first: the --profile flag, then default_profile, then
// nothing — an install with profiles configured but neither selector present
// behaves exactly as it did before profiles existed. That last case is the
// whole backward-compatibility guarantee, so it is a returned "" rather than a
// fallback buried in ApplyProfile.
func (c *Config) SelectProfile(flag string) string {
	if flag != "" {
		return flag
	}
	return c.DefaultProfile
}

// validateProfiles rejects a profiles block that cannot be used, at load time.
//
// Every check here is one that would otherwise surface as a provider error
// mid-turn: a 401 for a credential that was never resolved reads as a revoked
// key and sends the operator to a provider dashboard rather than to their
// config file.
func validateProfiles(cfg *Config) error {
	for _, name := range cfg.ProfileNames() {
		p := cfg.Profiles[name]
		if p.APIKey != "" {
			return fmt.Errorf("profile %q sets api_key; profiles never hold a credential — "+
				"use api_key_env to name the environment variable holding it", name)
		}
		if strings.TrimSpace(p.Provider) == "" {
			return fmt.Errorf("profile %q has no provider", name)
		}
		if strings.TrimSpace(p.Model) == "" {
			return fmt.Errorf("profile %q has no model", name)
		}
	}
	if cfg.DefaultProfile != "" {
		if _, ok := cfg.Profiles[cfg.DefaultProfile]; !ok {
			return fmt.Errorf("default_profile is %q, which is not a configured profile; configured: %s",
				cfg.DefaultProfile, strings.Join(cfg.ProfileNames(), ", "))
		}
	}
	return nil
}

// ProfileModelID returns the model string a profile resolves to, with the
// provider prefix applied when the model carries none. Exported for the
// listing command, which shows an operator what they would actually dial.
//
// A bare slash is not evidence of a prefix: real model IDs contain them
// routinely ("z-ai/glm-4.6"), so treating one as a provider prefix would leave
// that model routed to a provider named "z-ai" and failing to resolve. The
// prefix is recognised only if it is one joshbot actually routes on, or the
// profile's own provider name.
func (p Profile) ProfileModelID() string {
	model := strings.TrimSpace(p.Model)
	provider := strings.TrimSpace(p.Provider)
	if _, known := providerPrefixes[strings.SplitAfter(model, "/")[0]]; known {
		return model
	}
	if provider != "" && strings.HasPrefix(model, provider+"/") {
		return model
	}
	return provider + "/" + model
}

// ApplyProfile rewrites the config so the run uses the named profile, and
// nothing else.
//
// The profile becomes the one and only model: it replaces the models block
// rather than being added to it, because a profile whose fallback was some
// other entry in the file would silently dial an endpoint the operator did not
// select — the exact surprise switching profiles is meant to avoid. Everything
// downstream keeps reading the model-centric config it already read, so no
// provider wiring changes.
func (c *Config) ApplyProfile(name string) error {
	if name == "" {
		return nil
	}
	p, ok := c.Profiles[name]
	if !ok {
		if len(c.Profiles) == 0 {
			return fmt.Errorf("no profile named %q: no profiles are configured", name)
		}
		return fmt.Errorf("no profile named %q; configured profiles: %s",
			name, strings.Join(c.ProfileNames(), ", "))
	}
	if p.Disabled {
		return fmt.Errorf("profile %q is disabled; remove its \"disabled\" flag to use it", name)
	}

	// Resolve the credential before anything else touches the config: a failure
	// here must leave the run unmodified rather than half-switched.
	apiKey := ""
	if p.APIKeyEnv != "" {
		apiKey = os.Getenv(p.APIKeyEnv)
		if apiKey == "" {
			return fmt.Errorf("profile %q: api_key_env names $%s, which is not set in the environment",
				name, p.APIKeyEnv)
		}
	}

	model := p.ProfileModelID()
	c.ModelsConfig = ModelsConfig{
		Models: []ModelConfig{{
			Name:      name,
			Model:     model,
			APIKey:    apiKey,
			APIBase:   strings.TrimSpace(p.APIBase),
			MaxTokens: c.Agents.Defaults.MaxTokens,
		}},
		Agent: AgentModelConfig{Model: name},
	}
	// Kept in step because status output, logging and the legacy fallbacks all
	// read it; leaving it pointing at the pre-profile model would misreport
	// every run made under a profile.
	c.Agents.Defaults.Model = model
	c.ProviderDefaults.Default = strings.TrimSpace(p.Provider)
	c.activeProfile = name

	// Resolution failures (an unknown provider with no api_base, a missing
	// credential) are startup errors, not first-request errors.
	if _, err := c.ResolveModelConfig(name); err != nil {
		return fmt.Errorf("profile %q: %w", name, err)
	}
	return nil
}

// ActiveProfile reports the profile applied to this config, or "" if none was.
func (c *Config) ActiveProfile() string { return c.activeProfile }
