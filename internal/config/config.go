package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Logger is a simple logger interface for the config package.
type Logger interface {
	Warn(msg string, args ...interface{})
	Info(msg string, args ...interface{})
}

// defaultLogger is the default logger used when none is provided.
type defaultLogger struct{}

func (d *defaultLogger) Warn(msg string, args ...interface{}) {
	log.Printf("WARN: "+msg, args...)
}

func (d *defaultLogger) Info(msg string, args ...interface{}) {
	log.Printf("INFO: "+msg, args...)
}

// logger is the package-level logger.
var logger Logger = &defaultLogger{}

// SetLogger sets the logger for the config package.
func SetLogger(l Logger) {
	logger = l
}

const (
	// DefaultModel is the default LLM model.
	DefaultModel = "z-ai/glm-4.5-air:free"
	// DefaultExecTimeout is the default shell execution timeout in seconds.
	DefaultExecTimeout = 60
	// DefaultGatewayHost is the default gateway host.
	DefaultGatewayHost = "0.0.0.0"
	// DefaultGatewayPort is the default gateway port.
	DefaultGatewayPort = 18790
	// DefaultMaxTokens is the default max tokens for LLM responses.
	DefaultMaxTokens = 8192
	// DefaultTemperature is the default temperature for LLM responses.
	DefaultTemperature = 0.7
	// DefaultMaxToolIterations is the default max tool iterations in ReAct loop.
	DefaultMaxToolIterations = 20
	// DefaultMemoryWindow is the default memory window size.
	DefaultMemoryWindow = 50
	// CurrentSchemaVersion is the current config schema version.
	CurrentSchemaVersion = 1
)

// DefaultHome is the default joshbot home directory.
var DefaultHome = filepath.Join(os.Getenv("HOME"), ".joshbot")

// DefaultWorkspace is the default workspace directory.
var DefaultWorkspace = filepath.Join(DefaultHome, "workspace")

// ProviderConfig holds configuration for a single LLM provider.
type ProviderConfig struct {
	APIKey       string            `mapstructure:"api_key" json:"api_key" yaml:"api_key"`
	APIBase      string            `mapstructure:"api_base" json:"api_base" yaml:"api_base"`
	ExtraHeaders map[string]string `mapstructure:"extra_headers" json:"extra_headers" yaml:"extra_headers"`
}

// AgentDefaults holds default agent configuration.
type AgentDefaults struct {
	Workspace         string  `mapstructure:"workspace" json:"workspace" yaml:"workspace"`
	Model             string  `mapstructure:"model" json:"model" yaml:"model"`
	MaxTokens         int     `mapstructure:"max_tokens" json:"max_tokens" yaml:"max_tokens"`
	Temperature       float64 `mapstructure:"temperature" json:"temperature" yaml:"temperature"`
	MaxToolIterations int     `mapstructure:"max_tool_iterations" json:"max_tool_iterations" yaml:"max_tool_iterations"`
	MemoryWindow      int     `mapstructure:"memory_window" json:"memory_window" yaml:"memory_window"`
}

// AgentsConfig holds agent configuration.
type AgentsConfig struct {
	Defaults AgentDefaults `mapstructure:"defaults" json:"defaults" yaml:"defaults"`
}

// TelegramConfig holds Telegram channel configuration.
type TelegramConfig struct {
	Enabled   bool     `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Token     string   `mapstructure:"token" json:"token" yaml:"token"`
	AllowFrom []string `mapstructure:"allow_from" json:"allow_from" yaml:"allow_from"`
	Proxy     string   `mapstructure:"proxy" json:"proxy" yaml:"proxy"`
}

// ChannelsConfig holds channels configuration.
type ChannelsConfig struct {
	Telegram TelegramConfig `mapstructure:"telegram" json:"telegram" yaml:"telegram"`
}

// WebSearchConfig holds web search tool configuration.
type WebSearchConfig struct {
	APIKey string `mapstructure:"api_key" json:"api_key" yaml:"api_key"`
}

// WebToolsConfig holds web tools configuration.
type WebToolsConfig struct {
	Search WebSearchConfig `mapstructure:"search" json:"search" yaml:"search"`
}

// ExecConfig holds shell execution configuration.
type ExecConfig struct {
	Timeout int `mapstructure:"timeout" json:"timeout" yaml:"timeout"`
}

// ToolsConfig holds tools configuration.
type ToolsConfig struct {
	Web                 WebToolsConfig `mapstructure:"web" json:"web" yaml:"web"`
	Exec                ExecConfig     `mapstructure:"exec" json:"exec" yaml:"exec"`
	RestrictToWorkspace bool           `mapstructure:"restrict_to_workspace" json:"restrict_to_workspace" yaml:"restrict_to_workspace"`
}

// GatewayConfig holds gateway server configuration.
type GatewayConfig struct {
	Host string `mapstructure:"host" json:"host" yaml:"host"`
	Port int    `mapstructure:"port" json:"port" yaml:"port"`
}

// Config is the root configuration for joshbot.
type Config struct {
	SchemaVersion int                       `mapstructure:"schema_version" json:"schema_version" yaml:"schema_version"`
	Providers     map[string]ProviderConfig `mapstructure:"providers" json:"providers" yaml:"providers"`
	Agents        AgentsConfig              `mapstructure:"agents" json:"agents" yaml:"agents"`
	Channels      ChannelsConfig            `mapstructure:"channels" json:"channels" yaml:"channels"`
	Tools         ToolsConfig               `mapstructure:"tools" json:"tools" yaml:"tools"`
	Gateway       GatewayConfig             `mapstructure:"gateway" json:"gateway" yaml:"gateway"`
	LogLevel      string                    `mapstructure:"log_level" json:"log_level" yaml:"log_level"`
}

// parseConfigFromFile parses JSON config data into the Config struct.
func parseConfigFromFile(data []byte, cfg *Config) error {
	return json.Unmarshal(data, cfg)
}

// serializeConfig serializes the Config struct to JSON.
func serializeConfig(cfg *Config) ([]byte, error) {
	return json.MarshalIndent(cfg, "", "  ")
}

// applyEnvOverrides applies environment variable overrides to the config.
func applyEnvOverrides(cfg *Config) {
	// Helper to get env var with prefix
	getEnv := func(key string) string {
		return os.Getenv("JOSHBOT_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_")))
	}

	// Schema version
	if v := getEnv("SCHEMA_VERSION"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.SchemaVersion)
	}

	// Model
	if v := getEnv("AGENTS__DEFAULTS__MODEL"); v != "" {
		cfg.Agents.Defaults.Model = v
	}

	// Workspace
	if v := getEnv("AGENTS__DEFAULTS__WORKSPACE"); v != "" {
		cfg.Agents.Defaults.Workspace = v
	}

	// Max tokens
	if v := getEnv("AGENTS__DEFAULTS__MAX_TOKENS"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Agents.Defaults.MaxTokens)
	}

	// Temperature
	if v := getEnv("AGENTS__DEFAULTS__TEMPERATURE"); v != "" {
		fmt.Sscanf(v, "%f", &cfg.Agents.Defaults.Temperature)
	}

	// Max tool iterations
	if v := getEnv("AGENTS__DEFAULTS__MAX_TOOL_ITERATIONS"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Agents.Defaults.MaxToolIterations)
	}

	// Memory window
	if v := getEnv("AGENTS__DEFAULTS__MEMORY_WINDOW"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Agents.Defaults.MemoryWindow)
	}

	// Telegram enabled
	if v := getEnv("CHANNELS__TELEGRAM__ENABLED"); v != "" {
		cfg.Channels.Telegram.Enabled = v == "true" || v == "1"
	}

	// Telegram token
	if v := getEnv("CHANNELS__TELEGRAM__TOKEN"); v != "" {
		cfg.Channels.Telegram.Token = v
	}

	// Telegram proxy
	if v := getEnv("CHANNELS__TELEGRAM__PROXY"); v != "" {
		cfg.Channels.Telegram.Proxy = v
	}

	// Web search API key
	if v := getEnv("TOOLS__WEB__SEARCH__API_KEY"); v != "" {
		cfg.Tools.Web.Search.APIKey = v
	}

	// Exec timeout
	if v := getEnv("TOOLS__EXEC__TIMEOUT"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Tools.Exec.Timeout)
	}

	// Restrict to workspace
	if v := getEnv("TOOLS__RESTRICT_TO_WORKSPACE"); v != "" {
		cfg.Tools.RestrictToWorkspace = v == "true" || v == "1"
	}

	// Gateway host
	if v := getEnv("GATEWAY__HOST"); v != "" {
		cfg.Gateway.Host = v
	}

	// Gateway port
	if v := getEnv("GATEWAY__PORT"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Gateway.Port)
	}

	// Log level
	if v := getEnv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}

	// Provider API keys
	if v := getEnv("PROVIDERS__OPENROUTER__API_KEY"); v != "" {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]ProviderConfig)
		}
		if p, ok := cfg.Providers["openrouter"]; ok {
			p.APIKey = v
			cfg.Providers["openrouter"] = p
		} else {
			cfg.Providers["openrouter"] = ProviderConfig{APIKey: v}
		}
	}

	if v := getEnv("PROVIDERS__OPENROUTER__API_BASE"); v != "" {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]ProviderConfig)
		}
		if p, ok := cfg.Providers["openrouter"]; ok {
			p.APIBase = v
			cfg.Providers["openrouter"] = p
		} else {
			cfg.Providers["openrouter"] = ProviderConfig{APIBase: v}
		}
	}
}

// Defaults returns a Config with all default values set.
func Defaults() *Config {
	return &Config{
		SchemaVersion: CurrentSchemaVersion,
		Providers: map[string]ProviderConfig{
			"openrouter": {},
		},
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Workspace:         DefaultWorkspace,
				Model:             DefaultModel,
				MaxTokens:         DefaultMaxTokens,
				Temperature:       DefaultTemperature,
				MaxToolIterations: DefaultMaxToolIterations,
				MemoryWindow:      DefaultMemoryWindow,
			},
		},
		Channels: ChannelsConfig{
			Telegram: TelegramConfig{
				Enabled:   false,
				Token:     "",
				AllowFrom: []string{},
				Proxy:     "",
			},
		},
		Tools: ToolsConfig{
			Web: WebToolsConfig{
				Search: WebSearchConfig{
					APIKey: "",
				},
			},
			Exec: ExecConfig{
				Timeout: DefaultExecTimeout,
			},
			RestrictToWorkspace: false,
		},
		Gateway: GatewayConfig{
			Host: DefaultGatewayHost,
			Port: DefaultGatewayPort,
		},
		LogLevel: "info",
	}
}

// HomeDir returns the joshbot home directory (~/.joshbot).
func (c *Config) HomeDir() string {
	return DefaultHome
}

// WorkspaceDir returns the workspace directory.
func (c *Config) WorkspaceDir() string {
	return c.Agents.Defaults.Workspace
}

// SessionsDir returns the sessions directory.
func (c *Config) SessionsDir() string {
	return filepath.Join(DefaultHome, "sessions")
}

// MediaDir returns the media directory.
func (c *Config) MediaDir() string {
	return filepath.Join(DefaultHome, "media")
}

// CronDir returns the cron directory.
func (c *Config) CronDir() string {
	return filepath.Join(DefaultHome, "cron")
}

// EnsureDirs creates all required directories for joshbot.
func (c *Config) EnsureDirs() error {
	dirs := []string{
		c.HomeDir(),
		c.WorkspaceDir(),
		c.SessionsDir(),
		c.MediaDir(),
		c.CronDir(),
		filepath.Join(c.WorkspaceDir(), "memory"),
		filepath.Join(c.WorkspaceDir(), "skills"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// Validate validates the configuration and returns an error if invalid.
func (c *Config) Validate() error {
	// Validate model is not empty
	if c.Agents.Defaults.Model == "" {
		return errors.New("model cannot be empty")
	}

	// Validate max_tokens is positive
	if c.Agents.Defaults.MaxTokens <= 0 {
		return errors.New("max_tokens must be positive")
	}

	// Validate temperature is in valid range
	if c.Agents.Defaults.Temperature < 0 || c.Agents.Defaults.Temperature > 2 {
		return errors.New("temperature must be between 0 and 2")
	}

	// Validate max_tool_iterations is positive
	if c.Agents.Defaults.MaxToolIterations <= 0 {
		return errors.New("max_tool_iterations must be positive")
	}

	// Validate memory_window is positive
	if c.Agents.Defaults.MemoryWindow <= 0 {
		return errors.New("memory_window must be positive")
	}

	// Validate exec timeout is positive
	if c.Tools.Exec.Timeout <= 0 {
		return errors.New("exec timeout must be positive")
	}

	// Validate gateway port is valid
	if c.Gateway.Port <= 0 || c.Gateway.Port > 65535 {
		return errors.New("gateway port must be between 1 and 65535")
	}

	// Validate log level
	validLogLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}
	if !validLogLevels[c.LogLevel] {
		return errors.New("log_level must be one of: debug, info, warn, error")
	}

	return nil
}

// Load loads configuration from file and environment variables.
// Priority: env vars > config file > defaults
func Load() (*Config, error) {
	// Check for config file
	configPath := filepath.Join(DefaultHome, "config.json")
	if _, err := os.Stat(configPath); err == nil {
		// Try to load from file
		data, err := os.ReadFile(configPath)
		if err != nil {
			logger.Warn("Failed to read config file, using defaults", "error", err)
			cfg := Defaults()
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
			return cfg, nil
		}

		cfg := Defaults()
		// Override with file values using simple JSON parsing
		if err := parseConfigFromFile(data, cfg); err != nil {
			logger.Warn("Failed to parse config file, using defaults", "error", err)
			cfg = Defaults()
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
			return cfg, nil
		}

		// Apply environment variable overrides
		applyEnvOverrides(cfg)

		// Sanitize string fields that may have whitespace from user input
		for name, p := range cfg.Providers {
			p.APIKey = strings.TrimSpace(p.APIKey)
			p.APIBase = strings.TrimSpace(p.APIBase)
			cfg.Providers[name] = p
		}
		cfg.Channels.Telegram.Token = strings.TrimSpace(cfg.Channels.Telegram.Token)
		cfg.Agents.Defaults.Model = strings.TrimSpace(cfg.Agents.Defaults.Model)

		// Apply migrations if needed
		if cfg.SchemaVersion < CurrentSchemaVersion {
			if err := migrateConfig(cfg); err != nil {
				logger.Warn("Config migration failed, using defaults", "error", err)
				cfg = Defaults()
			}
		}

		// Validate configuration
		if err := cfg.Validate(); err != nil {
			logger.Warn("Config validation failed, using defaults", "error", err)
			cfg = Defaults()
		}

		return cfg, nil
	}

	// No config file, use defaults
	cfg := Defaults()

	// Apply environment variable overrides even without config file
	applyEnvOverrides(cfg)

	// Sanitize string fields that may have whitespace from user input
	for name, p := range cfg.Providers {
		p.APIKey = strings.TrimSpace(p.APIKey)
		p.APIBase = strings.TrimSpace(p.APIBase)
		cfg.Providers[name] = p
	}
	cfg.Channels.Telegram.Token = strings.TrimSpace(cfg.Channels.Telegram.Token)
	cfg.Agents.Defaults.Model = strings.TrimSpace(cfg.Agents.Defaults.Model)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save saves the configuration to the config file.
func Save(cfg *Config) error {
	// Ensure config directory exists
	if err := os.MkdirAll(DefaultHome, 0o755); err != nil {
		return err
	}

	// Write config to JSON file
	data, err := serializeConfig(cfg)
	if err != nil {
		return err
	}

	configPath := filepath.Join(DefaultHome, "config.json")
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return err
	}

	logger.Info("Config saved", "path", configPath)
	return nil
}

// migrateConfig migrates config from older schema versions to current.
func migrateConfig(cfg *Config) error {
	// Migration from v0 to v1
	if cfg.SchemaVersion < 1 {
		// Update defunct model if present
		if cfg.Agents.Defaults.Model == "google/gemma-2-9b-it:free" {
			cfg.Agents.Defaults.Model = "arcee-ai/tranny-large-preview:free"
			logger.Info("Migrated model from google/gemma-2-9b-it:free to arcee-ai/tranny-large-preview:free")
		}
		cfg.SchemaVersion = 1

		// Backup old config
		configPath := filepath.Join(DefaultHome, "config.json")
		if _, err := os.Stat(configPath); err == nil {
			backupPath := configPath + ".bak"
			if data, err := os.ReadFile(configPath); err == nil {
				_ = os.WriteFile(backupPath, data, 0o644)
				logger.Info("Backed up config", "to", backupPath)
			}
		}
	}

	return nil
}

// String returns a string representation of the config (for debugging).
func (c *Config) String() string {
	return fmt.Sprintf("Config{SchemaVersion: %d, Model: %s, LogLevel: %s, Gateway: %s:%d}",
		c.SchemaVersion,
		c.Agents.Defaults.Model,
		c.LogLevel,
		c.Gateway.Host,
		c.Gateway.Port,
	)
}
