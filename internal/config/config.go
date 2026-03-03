package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	DefaultModel = "arcee-ai/trinity-large-preview:free"
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
	// DefaultCompactionThreshold is the default threshold for proactive context compaction.
	DefaultCompactionThreshold = 0.7
	// DefaultToolOutputMaxChars is the default max characters for tool output truncation.
	DefaultToolOutputMaxChars = 4000
	// CurrentSchemaVersion is the current config schema version.
	CurrentSchemaVersion = 4
)

// DefaultHome is the default joshbot home directory.
var DefaultHome = filepath.Join(os.Getenv("HOME"), ".joshbot")

// DefaultWorkspace is the default workspace directory.
var DefaultWorkspace = filepath.Join(DefaultHome, "workspace")

// ProviderConfig holds configuration for a single LLM provider.
type ProviderConfig struct {
	APIKey       string            `mapstructure:"api_key" json:"api_key,omitempty" yaml:"api_key,omitempty"`
	APIBase      string            `mapstructure:"api_base" json:"api_base,omitempty" yaml:"api_base,omitempty"`
	Model        string            `mapstructure:"model" json:"model,omitempty" yaml:"model,omitempty"`
	ExtraHeaders map[string]string `mapstructure:"extra_headers" json:"extra_headers,omitempty" yaml:"extra_headers,omitempty"`
	Enabled      bool              `mapstructure:"enabled" json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Timeout      time.Duration     `mapstructure:"timeout" json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// ProviderDefaults holds default provider settings
type ProviderDefaults struct {
	Default       string   `mapstructure:"default" json:"default" yaml:"default"`
	FallbackOrder []string `mapstructure:"fallback_order" json:"fallback_order" yaml:"fallback_order"`
}

// AgentDefaults holds default agent configuration.
type AgentDefaults struct {
	Workspace           string  `mapstructure:"workspace" json:"workspace" yaml:"workspace"`
	Model               string  `mapstructure:"model" json:"model" yaml:"model"`
	MaxTokens           int     `mapstructure:"max_tokens" json:"max_tokens" yaml:"max_tokens"`
	Temperature         float64 `mapstructure:"temperature" json:"temperature" yaml:"temperature"`
	MaxToolIterations   int     `mapstructure:"max_tool_iterations" json:"max_tool_iterations" yaml:"max_tool_iterations"`
	MemoryWindow        int     `mapstructure:"memory_window" json:"memory_window" yaml:"memory_window"`
	CompactionThreshold float64 `mapstructure:"compaction_threshold" json:"compaction_threshold" yaml:"compaction_threshold"`
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
	Web                    WebToolsConfig `mapstructure:"web" json:"web" yaml:"web"`
	Exec                   ExecConfig     `mapstructure:"exec" json:"exec" yaml:"exec"`
	RestrictToWorkspace    bool           `mapstructure:"restrict_to_workspace" json:"restrict_to_workspace" yaml:"restrict_to_workspace"`
	ShellAllowList         []string       `mapstructure:"shell_allow_list" json:"shell_allow_list" yaml:"shell_allow_list"`
	FilesystemAllowedPaths []string       `mapstructure:"filesystem_allowed_paths" json:"filesystem_allowed_paths" yaml:"filesystem_allowed_paths"`
	ToolOutputMaxChars     int            `mapstructure:"tool_output_max_chars" json:"tool_output_max_chars" yaml:"tool_output_max_chars"`
}

// GatewayConfig holds gateway server configuration.
type GatewayConfig struct {
	Host string `mapstructure:"host" json:"host" yaml:"host"`
	Port int    `mapstructure:"port" json:"port" yaml:"port"`
}

// UserConfig holds user preferences for personalization.
type UserConfig struct {
	Name string `mapstructure:"name" json:"name,omitempty" yaml:"name,omitempty"`
}

// Config is the root configuration for joshbot.
type Config struct {
	SchemaVersion    int                       `mapstructure:"schema_version" json:"schema_version" yaml:"schema_version"`
	Providers        map[string]ProviderConfig `mapstructure:"providers" json:"providers" yaml:"providers"`
	ProviderDefaults ProviderDefaults          `mapstructure:"provider_defaults" json:"provider_defaults,omitempty" yaml:"provider_defaults,omitempty"`
	Agents           AgentsConfig              `mapstructure:"agents" json:"agents" yaml:"agents"`
	Channels         ChannelsConfig            `mapstructure:"channels" json:"channels" yaml:"channels"`
	Tools            ToolsConfig               `mapstructure:"tools" json:"tools" yaml:"tools"`
	Gateway          GatewayConfig             `mapstructure:"gateway" json:"gateway" yaml:"gateway"`
	LogLevel         string                    `mapstructure:"log_level" json:"log_level" yaml:"log_level"`
	User             UserConfig                `mapstructure:"user" json:"user,omitempty" yaml:"user,omitempty"`
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

	// Compaction threshold
	if v := getEnv("AGENTS__DEFAULTS__COMPACTION_THRESHOLD"); v != "" {
		fmt.Sscanf(v, "%f", &cfg.Agents.Defaults.CompactionThreshold)
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

	// Shell allow list (comma-separated)
	if v := getEnv("TOOLS__SHELL_ALLOW_LIST"); v != "" {
		cfg.Tools.ShellAllowList = strings.Split(v, ",")
		for i := range cfg.Tools.ShellAllowList {
			cfg.Tools.ShellAllowList[i] = strings.TrimSpace(cfg.Tools.ShellAllowList[i])
		}
	}

	// Filesystem allowed paths (comma-separated)
	if v := getEnv("TOOLS__FILESYSTEM_ALLOWED_PATHS"); v != "" {
		cfg.Tools.FilesystemAllowedPaths = strings.Split(v, ",")
		for i := range cfg.Tools.FilesystemAllowedPaths {
			cfg.Tools.FilesystemAllowedPaths[i] = strings.TrimSpace(cfg.Tools.FilesystemAllowedPaths[i])
		}
	}

	// Tool output max chars
	if v := getEnv("TOOLS__TOOL_OUTPUT_MAX_CHARS"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Tools.ToolOutputMaxChars)
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
				Workspace:           DefaultWorkspace,
				Model:               DefaultModel,
				MaxTokens:           DefaultMaxTokens,
				Temperature:         DefaultTemperature,
				MaxToolIterations:   DefaultMaxToolIterations,
				MemoryWindow:        DefaultMemoryWindow,
				CompactionThreshold: DefaultCompactionThreshold,
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
			RestrictToWorkspace:    true,
			ShellAllowList:         []string{},
			FilesystemAllowedPaths: []string{},
			ToolOutputMaxChars:     DefaultToolOutputMaxChars,
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
			if err := migrateConfig(cfg, data); err != nil {
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

// parseExplicitDisable parses raw JSON config to detect providers that were
// explicitly set to enabled: false in the old config format.
func parseExplicitDisable(rawJSON []byte) map[string]bool {
	result := make(map[string]bool)
	if len(rawJSON) == 0 {
		return result
	}

	// Parse JSON to get providers map
	var data map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &data); err != nil {
		return result
	}

	providersJSON, ok := data["providers"]
	if !ok {
		return result
	}

	// Parse providers map
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(providersJSON, &providers); err != nil {
		return result
	}

	// Check each provider for explicit enabled: false
	for name, providerJSON := range providers {
		var providerConfig struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(providerJSON, &providerConfig); err == nil {
			// Check if "enabled" was explicitly present and set to false
			// We need to check if the field was present - if not in JSON, it won't unmarshal
			// Since Enabled is bool (defaults to false), we need a different approach
			// Actually, we can check if "enabled" key exists in the raw JSON for this provider
			if !providerConfig.Enabled && containsEnabledKey(providerJSON) {
				result[name] = true
			}
		}
	}

	return result
}

// containsEnabledKey checks if the JSON object contains an "enabled" key.
func containsEnabledKey(data []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	_, hasEnabled := m["enabled"]
	return hasEnabled
}

// migrateConfig migrates config from older schema versions to current.
// It accepts raw JSON data to detect explicit enable/disable settings.
func migrateConfig(cfg *Config, rawJSON []byte) error {
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

	// Migration from v1 to v2
	if cfg.SchemaVersion < 2 {
		// Initialize ProviderDefaults if not present
		if cfg.ProviderDefaults.Default == "" && len(cfg.ProviderDefaults.FallbackOrder) == 0 {
			cfg.ProviderDefaults = ProviderDefaults{
				Default:       "",
				FallbackOrder: []string{},
			}
			logger.Info("Migrated config to v2: initialized ProviderDefaults")
		}
		cfg.SchemaVersion = 2
	}

	// Migration from v2 to v3
	if cfg.SchemaVersion < 3 {
		// Initialize new tool config fields if not present
		if cfg.Tools.ShellAllowList == nil {
			cfg.Tools.ShellAllowList = []string{}
		}
		if cfg.Tools.FilesystemAllowedPaths == nil {
			cfg.Tools.FilesystemAllowedPaths = []string{}
		}
		logger.Info("Migrated config to v3: added shell allowlist and filesystem allowed paths")
		cfg.SchemaVersion = 3
	}

	// Migration from v3 to v4
	if cfg.SchemaVersion < 4 {
		// Parse raw JSON to detect explicit enable/disable settings
		explicitDisable := parseExplicitDisable(rawJSON)

		// For backward compatibility: cloud providers configured in old config get auto-enabled,
		// but local providers (ollama, github-copilot) require explicit enable to avoid
		// auto-starting local daemons.
		localProviders := map[string]bool{
			"ollama":         true,
			"github-copilot": true,
		}

		for name, p := range cfg.Providers {
			hasConfig := p.APIKey != "" || p.APIBase != "" || p.Model != "" || len(p.ExtraHeaders) > 0

			// Already enabled - keep as-is
			if p.Enabled {
				logger.Info("Provider explicitly enabled in config", "provider", name)
				continue
			}
			// Was explicitly disabled in old config - keep disabled
			if explicitDisable[name] {
				logger.Info("Provider explicitly disabled in old config, remains disabled", "provider", name)
				continue
			}
			// Local providers need explicit enable - don't auto-enable
			if localProviders[name] {
				if hasConfig {
					logger.Info("Local provider remains disabled after migration (explicit enable required)", "provider", name)
				}
				continue
			}
			// Cloud provider with config - auto-enable for backward compatibility
			if hasConfig {
				p.Enabled = true
				cfg.Providers[name] = p
				logger.Info("Provider enabled during migration for backward compatibility", "provider", name)
			}
		}
		logger.Info("Migrated config to v4: provider enabled flags")
		cfg.SchemaVersion = 4
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
