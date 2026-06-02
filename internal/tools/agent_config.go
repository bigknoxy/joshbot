package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// SubagentConfig defines a named agent profile with specific model, tools, and system prompt.
type SubagentConfig struct {
	Name         string   `yaml:"name" json:"name"`
	Description  string   `yaml:"description" json:"description"`
	Model        string   `yaml:"model" json:"model"`
	Temperature  float64  `yaml:"temperature" json:"temperature"`
	MaxTokens    int      `yaml:"max_tokens" json:"max_tokens"`
	SystemPrompt string   `yaml:"system_prompt" json:"system_prompt"`
	Tools        []string `yaml:"tools" json:"tools"`
	Skills       []string `yaml:"skills" json:"skills"`
	Tags         []string `yaml:"tags" json:"tags"`
}

// SetDefaults fills in zero-value fields with sensible defaults.
// Temperature defaults to 0.3 and MaxTokens defaults to 500.
func (c *SubagentConfig) SetDefaults() {
	if c.Temperature == 0 {
		c.Temperature = 0.3
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 500
	}
}

// SubagentConfigManager discovers and manages SubagentConfig files
// from a directory of YAML config files.
type SubagentConfigManager struct {
	configDir string
	mu        sync.RWMutex
	configs   map[string]*SubagentConfig
}

// NewSubagentConfigManager creates a new SubagentConfigManager.
// The configDir must exist for Discover to succeed, but Save will create it if needed.
func NewSubagentConfigManager(configDir string) (*SubagentConfigManager, error) {
	return &SubagentConfigManager{
		configDir: configDir,
		configs:   make(map[string]*SubagentConfig),
	}, nil
}

// Discover scans configDir for *.yaml and *.yml files and loads each into
// a SubagentConfig. Returns an error if:
//   - the config directory does not exist or is not a directory
//   - a YAML file has invalid syntax
//   - a config is missing the required 'name' field
//   - two config files have the same name
//
// An empty config directory produces no error and an empty config list.
func (m *SubagentConfigManager) Discover() error {
	info, err := os.Stat(m.configDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("config directory does not exist: %s", m.configDir)
		}
		return fmt.Errorf("failed to stat config directory %s: %w", m.configDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("config path is not a directory: %s", m.configDir)
	}

	entries, err := os.ReadDir(m.configDir)
	if err != nil {
		return fmt.Errorf("failed to read config directory %s: %w", m.configDir, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Reset the config map each discovery cycle.
	m.configs = make(map[string]*SubagentConfig)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(m.configDir, entry.Name())
		cfg, err := m.parseFile(path)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", entry.Name(), err)
		}

		if _, exists := m.configs[cfg.Name]; exists {
			return fmt.Errorf("duplicate subagent config name %q in %s", cfg.Name, entry.Name())
		}

		m.configs[cfg.Name] = cfg
	}

	return nil
}

// parseFile reads and parses a single YAML config file into a SubagentConfig.
// Returns an error if the file cannot be read, the YAML is invalid, or the
// config is missing the required 'name' field.
func (m *SubagentConfigManager) parseFile(path string) (*SubagentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var cfg SubagentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	if cfg.Name == "" {
		return nil, fmt.Errorf("config is missing required 'name' field")
	}

	cfg.SetDefaults()
	return &cfg, nil
}

// Get returns a config by name. The second return value is false if not found.
func (m *SubagentConfigManager) Get(name string) (*SubagentConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cfg, ok := m.configs[name]
	return cfg, ok
}

// List returns all discovered configs. The order is non-deterministic.
func (m *SubagentConfigManager) List() []*SubagentConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	configs := make([]*SubagentConfig, 0, len(m.configs))
	for _, cfg := range m.configs {
		configs = append(configs, cfg)
	}
	return configs
}

// Save writes a SubagentConfig to configDir/{name}.yaml.
// The config directory is created if it does not exist.
// The Name field must be non-empty. Defaults are applied before writing.
func (m *SubagentConfigManager) Save(cfg *SubagentConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("config name is required")
	}

	if err := os.MkdirAll(m.configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	cfg.SetDefaults()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	path := filepath.Join(m.configDir, cfg.Name+".yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", path, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.configs[cfg.Name] = cfg
	return nil
}
