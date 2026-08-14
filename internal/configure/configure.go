package configure

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/copilot"
	"github.com/bigknoxy/joshbot/internal/providers"
)

type ProviderOptions struct {
	Name    string
	APIKey  string
	APIBase string
	Model   string
	Timeout time.Duration
	Enabled bool
}

type ProviderListItem struct {
	Name       string
	Configured bool
	IsDefault  bool
	Model      string
}

type Configurator struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Configurator {
	return &Configurator{cfg: cfg}
}

func (c *Configurator) Config() *config.Config {
	return c.cfg
}

func (c *Configurator) ConfigureProvider(opts ProviderOptions) error {
	if c.cfg.Providers == nil {
		c.cfg.Providers = make(map[string]config.ProviderConfig)
	}

	p, exists := c.cfg.Providers[opts.Name]

	if opts.APIKey != "" {
		p.APIKey = opts.APIKey
	} else if !exists {
		return fmt.Errorf("API key is required for first-time configuration of %q", opts.Name)
	}

	if opts.APIBase != "" {
		p.APIBase = opts.APIBase
	} else if !exists || p.APIBase == "" {
		p.APIBase = getDefaultAPIBase(opts.Name)
	}

	if opts.Model != "" {
		p.Model = opts.Model
	}

	p.Enabled = true
	if opts.Timeout > 0 {
		p.Timeout = config.Duration(opts.Timeout)
	}
	c.cfg.Providers[opts.Name] = p

	if c.cfg.ProviderDefaults.Default == "" {
		c.cfg.ProviderDefaults.Default = opts.Name
		if p.Model != "" {
			c.cfg.Agents.Defaults.Model = p.Model
		} else {
			c.cfg.Agents.Defaults.Model = providers.GetDefaultModel(opts.Name)
		}
	} else if c.cfg.ProviderDefaults.Default == opts.Name && p.Model != "" {
		c.cfg.Agents.Defaults.Model = p.Model
	}

	return nil
}

// SetFallbackOrder writes provider_defaults.fallback_order — the order the
// runtime dials providers when the primary fails. Every name must refer to a
// configured, enabled provider: a typo here would silently vanish from the
// chain at the exact moment the primary is down, which is the last place a
// misconfiguration should first surface. An empty list clears the order.
func (c *Configurator) SetFallbackOrder(names []string) error {
	if len(names) == 0 {
		c.cfg.ProviderDefaults.FallbackOrder = nil
		return nil
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		p, ok := c.cfg.Providers[name]
		if !ok || !p.Enabled {
			configured := make([]string, 0, len(c.cfg.Providers))
			for n, cp := range c.cfg.Providers {
				if cp.Enabled {
					configured = append(configured, n)
				}
			}
			sort.Strings(configured)
			return fmt.Errorf("provider %q is not configured and enabled; configured providers: %s", name, strings.Join(configured, ", "))
		}
		if seen[name] {
			return fmt.Errorf("provider %q appears twice in the fallback order", name)
		}
		seen[name] = true
	}
	c.cfg.ProviderDefaults.FallbackOrder = names
	return nil
}

func (c *Configurator) SetDefault(name string) error {
	if c.cfg.Providers == nil {
		return fmt.Errorf("no providers configured")
	}
	p, ok := c.cfg.Providers[name]
	if !ok || !p.Enabled {
		return fmt.Errorf("provider %q is not configured", name)
	}
	c.cfg.ProviderDefaults.Default = name
	if p.Model != "" {
		c.cfg.Agents.Defaults.Model = p.Model
	} else {
		c.cfg.Agents.Defaults.Model = providers.GetDefaultModel(name)
	}
	return nil
}

func (c *Configurator) RemoveProvider(name string) error {
	if c.cfg.Providers == nil {
		return fmt.Errorf("no providers configured")
	}
	if _, ok := c.cfg.Providers[name]; !ok {
		return fmt.Errorf("provider %q is not configured", name)
	}
	delete(c.cfg.Providers, name)
	if c.cfg.ProviderDefaults.Default == name {
		c.cfg.ProviderDefaults.Default = ""
		for n, p := range c.cfg.Providers {
			if p.Enabled {
				c.cfg.ProviderDefaults.Default = n
				break
			}
		}
	}
	return nil
}

func (c *Configurator) ListProviders() []ProviderListItem {
	providerNames := SupportedProviders()
	defaultName := c.cfg.ProviderDefaults.Default
	var items []ProviderListItem

	for _, name := range providerNames {
		p, exists := c.cfg.Providers[name]
		isDefault := name == defaultName
		configured := false

		if exists && p.Enabled {
			if name == "github-copilot" {
				homeDir, _ := copilot.GetHomeDir()
				token, err := copilot.LoadToken(homeDir)
				if err == nil && token != nil && token.AccessToken != "" {
					configured = true
				}
			} else if p.APIKey != "" {
				configured = true
			}
		}

		model := p.Model
		if model == "" && configured {
			model = providers.GetDefaultModel(name)
		}

		items = append(items, ProviderListItem{
			Name:       name,
			Configured: configured,
			IsDefault:  isDefault,
			Model:      model,
		})
	}
	return items
}

func (c *Configurator) ValidateProviderCredentials(name string) error {
	p, ok := c.cfg.Providers[name]
	if !ok {
		return fmt.Errorf("provider %q is not configured", name)
	}
	baseURL := p.APIBase
	if baseURL == "" {
		baseURL = getDefaultAPIBase(name)
	}
	// Azure, custom and litellm have no fixed endpoint, and an unrecognised
	// provider name has none at all. Say so rather than dialling some other
	// provider's endpoint and reporting the result as a validated credential.
	if baseURL == "" {
		return fmt.Errorf("could not verify %q credentials: no API base URL configured (set api_base)", name)
	}
	model := p.Model
	if model == "" {
		model = providers.GetDefaultModel(name)
	}
	// A one-token chat completion, not ListModels: several providers'
	// /models endpoint is unauthenticated (OpenRouter answers 200 to any
	// key), so listing models validated nothing and a typo'd key earned a
	// checkmark and then a raw 401 on the first real message.
	return providers.ProbeCredential(providers.Config{
		APIKey:  p.APIKey,
		APIBase: baseURL,
		Model:   model,
	})
}

// SupportedProviders lists every provider the guided config path can configure.
// It mirrors the providers registered in internal/providers.
func SupportedProviders() []string {
	return []string{
		"openrouter", "openai", "nvidia", "groq", "ollama",
		"anthropic", "poolside", "azure", "custom", "litellm", "github-copilot",
		"deepseek", "gemini",
	}
}

// InteractiveProviders lists the providers the guided (menu) paths can set
// up with just a credential: everything in SupportedProviders() except those
// with no fixed endpoint (azure, custom, litellm), which need --api-base and
// so cannot complete from a menu that only asks for a key. The menus used to
// hardcode six names, so an Anthropic or OpenAI key-holder ran onboard,
// couldn't find their provider, and reasonably concluded it wasn't
// supported.
func InteractiveProviders() []string {
	// deepseek and gemini are excluded for a different reason: the legacy
	// runtime path (registerProviders in cmd/joshbot) has no registration
	// for them, so a menu that offered them would write a config entry the
	// runtime silently ignores — worse than not listing them.
	excluded := map[string]bool{
		"azure": true, "custom": true, "litellm": true,
		"deepseek": true, "gemini": true,
	}
	var out []string
	for _, name := range SupportedProviders() {
		if excluded[name] {
			continue
		}
		if name == "github-copilot" || getDefaultAPIBase(name) != "" {
			out = append(out, name)
		}
	}
	return out
}

// getDefaultAPIBase returns a provider's default API base URL. The values come
// from the provider registry (the single source of truth), so they cannot drift
// from what the runtime actually dials. Azure, custom and litellm have no fixed
// endpoint and return "" — the operator must supply --api-base. Ollama's
// registry entry leaves the base empty (its factory fills it in at request
// time), so it is filled in here explicitly for the guided path.
func getDefaultAPIBase(name string) string {
	// Model-path providers not in the registry need explicit endpoints here.
	if base, ok := modelPathAPIBase[name]; ok {
		return base
	}
	return providers.GetDefaultAPIBaseFor(name)
}

// modelPathAPIBase maps providers that route through OpenRouter/litellm
// with a model prefix to their direct API endpoints.
var modelPathAPIBase = map[string]string{
	"deepseek": "https://api.deepseek.com/v1",
	"gemini":   "https://generativelanguage.googleapis.com/v1beta",
	"ollama":   "http://localhost:11434/v1",
}

func Save(cfg *config.Config) error {
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

func GetProviderDisplayName(name string) string {
	// Prefer the registry's display name (the source of truth), falling back to
	// a title-cased provider key for anything it does not name.
	if display := providers.GetProviderDisplayName(name); display != "" && display != name {
		return display
	}
	return cases.Title(language.Und).String(name)
}

func MaskAPIKey(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}
