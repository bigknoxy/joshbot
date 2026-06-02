package configure

import (
	"fmt"
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
		p.Timeout = opts.Timeout
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
	providerNames := []string{"nvidia", "openrouter", "groq", "ollama", "github-copilot", "poolside"}
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
	_, err := providers.ListModels(providers.Config{
		APIKey:  p.APIKey,
		APIBase: baseURL,
	})
	return err
}

func getDefaultAPIBase(name string) string {
	bases := map[string]string{
		"nvidia":     "https://integrate.api.nvidia.com/v1",
		"openrouter": "https://openrouter.ai/api/v1",
		"groq":       "https://api.groq.com/openai/v1",
		"ollama":     "http://localhost:11434",
		"poolside":   "https://api.poolside.ai/v1",
	}
	if base, ok := bases[name]; ok {
		return base
	}
	return ""
}

func Save(cfg *config.Config) error {
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

func GetProviderDisplayName(name string) string {
	names := map[string]string{
		"nvidia":         "NVIDIA NIM",
		"openrouter":     "OpenRouter",
		"groq":           "Groq",
		"ollama":         "Ollama",
		"github-copilot": "GitHub Copilot",
		"poolside":       "Poolside",
	}
	if display, ok := names[name]; ok {
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
