package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Credential sources, as reported by CredentialSource. These are descriptions
// for humans and for `joshbot preflight`; nothing branches on them.
const (
	// CredentialFromFile means the literal api_key field in the config file.
	CredentialFromFile = "api_key in the config file"
	// CredentialMissing means no source supplied one.
	CredentialMissing = "not configured"
)

// CredentialFromEnv describes a credential read from a named variable.
func CredentialFromEnv(name string) string { return "$" + name }

// providerEnvKey is the canonical override variable for a provider's key.
func providerEnvKey(provider string) string {
	return "JOSHBOT_PROVIDERS__" + strings.ToUpper(provider) + "__API_KEY"
}

// CredentialSource reports where a provider's API key came from, for display.
// It never returns the credential itself.
func (c *Config) CredentialSource(provider string) string {
	if c.credentialSource != nil {
		if s, ok := c.credentialSource[provider]; ok {
			return s
		}
	}
	if p, ok := c.Providers[provider]; ok && p.APIKey != "" {
		return CredentialFromFile
	}
	return CredentialMissing
}

// withoutEnvCredentials returns a copy of cfg with every credential that came
// from the environment removed from the provider entries.
//
// Load resolves api_key_env — and JOSHBOT_PROVIDERS__<NAME>__API_KEY — into the
// in-memory APIKey field, because everything downstream reads one field.
// Serialising that back would write the secret into the file on the next Save,
// which is the exact outcome api_key_env exists to prevent: any command that
// touches the config (configure, onboard) would silently convert an indirected
// install into one with a plaintext key on disk.
//
// The maps are copied rather than mutated in place: cfg is live, and blanking
// the key the running process is about to dial with is a far worse bug than the
// one being fixed.
func withoutEnvCredentials(cfg *Config) *Config {
	if cfg == nil || len(cfg.credentialSource) == 0 {
		return cfg
	}

	out := *cfg
	out.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
	for name, p := range cfg.Providers {
		// CredentialFromFile and CredentialMissing both mean "nothing came
		// from the environment", so the file's own value is left alone.
		switch cfg.credentialSource[name] {
		case "", CredentialFromFile, CredentialMissing:
		default:
			p.APIKey = ""
		}
		out.Providers[name] = p
	}
	return &out
}

func (c *Config) noteCredentialSource(provider, source string) {
	if c.credentialSource == nil {
		c.credentialSource = make(map[string]string)
	}
	c.credentialSource[provider] = source
}

// resolveProviderCredentials expands api_key_env into the API key field.
//
// It runs *before* applyEnvOverrides, which is what makes the documented
// precedence fall out of the ordering rather than a second rule:
// JOSHBOT_PROVIDERS__<NAME>__API_KEY overwrites whatever this produced.
// Running it afterwards would also make the conflict check below fire on a
// key that the environment had supplied, not the operator's file.
//
// A provider naming a variable that is not set is a startup error. Deferring
// it turns a typo in a variable name into a 401 from the provider mid-turn,
// which reads as a revoked key and sends the operator to the wrong place.
func resolveProviderCredentials(cfg *Config) error {
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	// Deterministic order: with two broken providers the reported one must not
	// depend on map iteration.
	sort.Strings(names)

	for _, name := range names {
		p := cfg.Providers[name]
		if p.APIKeyEnv == "" {
			continue
		}
		if p.APIKey != "" {
			return fmt.Errorf("provider %q sets both api_key and api_key_env; "+
				"remove one — api_key_env names the environment variable holding the credential", name)
		}
		v := os.Getenv(p.APIKeyEnv)
		if v == "" {
			// The canonical override still wins, so a set one means the config
			// is usable and the named variable is redundant rather than wrong.
			if os.Getenv(providerEnvKey(name)) != "" {
				continue
			}
			return fmt.Errorf("provider %q: api_key_env names $%s, which is not set in the environment",
				name, p.APIKeyEnv)
		}
		p.APIKey = v
		cfg.Providers[name] = p
		cfg.noteCredentialSource(name, CredentialFromEnv(p.APIKeyEnv))
	}
	return nil
}
