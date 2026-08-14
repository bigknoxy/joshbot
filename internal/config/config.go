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

	"github.com/bigknoxy/joshbot/internal/redact"
)

// Logger is a simple logger interface for the config package.
type Logger interface {
	Warn(msg string, args ...interface{})
	Info(msg string, args ...interface{})
}

// defaultLogger is the default logger used when none is provided.
type defaultLogger struct{}

// format renders a message and its arguments, redacted.
//
// This logger writes straight to the standard library's log package, so it
// never passed through the redacting writer that internal/log installs — and
// it is the logger in force before joshbot has configured its own. That is why
// `joshbot configure` printed the operator's full home directory path while
// every other command printed "~". Credentials are stripped here too: this
// package handles config values, which is exactly where they live.
func (d *defaultLogger) format(level, msg string, args ...interface{}) string {
	out := level + ": " + msg
	for _, a := range args {
		if s, ok := a.(string); ok {
			out += " " + s
		} else {
			out += " " + fmt.Sprint(a)
		}
	}
	return redact.String(out)
}

func (d *defaultLogger) Warn(msg string, args ...interface{}) {
	log.Print(d.format("WARN", msg, args...))
}

func (d *defaultLogger) Info(msg string, args ...interface{}) {
	log.Print(d.format("INFO", msg, args...))
}

// logger is the package-level logger.
var logger Logger = &defaultLogger{}

// SetLogger sets the logger for the config package.
func SetLogger(l Logger) {
	logger = l
}

const (
	// DefaultModel is the default LLM model.
	DefaultModel = "openrouter/free"
	// DefaultExecTimeout is the default shell execution timeout in seconds.
	DefaultExecTimeout = 60
	// DefaultGatewayHost is the default gateway host.
	DefaultGatewayHost = "0.0.0.0"
	// DefaultGatewayPort is the default gateway port.
	DefaultGatewayPort = 18790
	// DefaultAPIListen is the default bind address for `joshbot serve`. It is
	// loopback, not 0.0.0.0: the OpenAI-compatible endpoint runs the full agent
	// with its shell and filesystem tools, so reaching the network is a
	// decision the operator makes deliberately.
	DefaultAPIListen = "127.0.0.1:18791"
	// DefaultMaxTokens is the default max tokens for LLM responses.
	DefaultMaxTokens = 8192
	// DefaultTemperature is the default temperature for LLM responses.
	DefaultTemperature = 0.7
	// DefaultMaxToolIterations is the default max tool iterations in ReAct loop.
	// Increased from 20 to 50 (issue #192) to support longer reasoning chains.
	DefaultMaxToolIterations = 50
	// DefaultMemoryWindow is the default memory window size.
	DefaultMemoryWindow = 50
	// DefaultStreaming is the default for agents.defaults.streaming.
	DefaultStreaming = true
	// DefaultCompactionThreshold is the default threshold for proactive context compaction.
	DefaultCompactionThreshold = 0.7
	// DefaultToolOutputMaxChars is the default max characters for tool output truncation.
	DefaultToolOutputMaxChars = 4000
	// DefaultMaxMemorySize is the default maximum bytes for MEMORY.md content loaded into context.
	DefaultMaxMemorySize = 4096
	// DefaultSubagentMaxDepth is the default maximum nesting depth for
	// delegate_subagent chains. Mirrors subagent.DefaultMaxDepth.
	DefaultSubagentMaxDepth = 2

	// Dream consolidation modes for agents.defaults.dream_mode.
	DreamModeOff    = "off"
	DreamModeRecord = "record"
	DreamModeFull   = "full"

	// CurrentSchemaVersion is the current config schema version.
	CurrentSchemaVersion = 5
)

// DefaultHome is the default joshbot home directory.
var DefaultHome = filepath.Join(os.Getenv("HOME"), ".joshbot")

// DefaultWorkspace is the default workspace directory.
var DefaultWorkspace = filepath.Join(DefaultHome, "workspace")

// ProviderConfig holds configuration for a single LLM provider.
type ProviderConfig struct {
	APIKey string `mapstructure:"api_key" json:"api_key,omitempty" yaml:"api_key,omitempty"`
	// APIKeyEnv names the environment variable holding the credential, so a
	// config file that is backed up, synced or pasted carries a variable name
	// rather than a secret. Setting it together with APIKey is a load error:
	// silently preferring one leaves the operator unable to tell which
	// credential is in use.
	APIKeyEnv    string            `mapstructure:"api_key_env" json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
	APIKeys      []string          `mapstructure:"api_keys" json:"api_keys,omitempty" yaml:"api_keys,omitempty"`
	APIBase      string            `mapstructure:"api_base" json:"api_base,omitempty" yaml:"api_base,omitempty"`
	Model        string            `mapstructure:"model" json:"model,omitempty" yaml:"model,omitempty"`
	ExtraHeaders map[string]string `mapstructure:"extra_headers" json:"extra_headers,omitempty" yaml:"extra_headers,omitempty"`
	ExtraBody    map[string]any    `mapstructure:"extra_body" json:"extra_body,omitempty" yaml:"extra_body,omitempty"`
	Enabled      bool              `mapstructure:"enabled" json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// Timeout bounds a single request to this provider. It reads a duration
	// string ("600s", "10m") or a bare number of seconds; see config.Duration
	// for why a bare number needs a rule at all.
	Timeout Duration `mapstructure:"timeout" json:"timeout,omitempty" yaml:"timeout,omitempty"`
	// MaxRetries is the same-provider retry budget for transient failures
	// (429/5xx/network) before the fallback chain moves on. A pointer so
	// that absent means "use the default" (2) while an explicit 0 means
	// "fail over immediately" — a plain int cannot tell those apart.
	MaxRetries *int `mapstructure:"max_retries" json:"max_retries,omitempty" yaml:"max_retries,omitempty"`
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
	MaxMemorySize       int     `mapstructure:"max_memory_size" json:"max_memory_size" yaml:"max_memory_size"`
	// Streaming enables incremental text delivery via ChatStream when a
	// stream sink is attached to the request context. Default true; set it to
	// false in the config file to restore whole-reply delivery.
	Streaming bool `mapstructure:"streaming" json:"streaming" yaml:"streaming"`
	// DreamMode selects the Dream two-stage memory consolidation mode:
	// "off" (default), "record" (log raw turns only) or "full" (log and
	// consolidate). It is a string rather than a bool on purpose: a bool
	// default can never be flipped later without a schema migration, because
	// every config joshbot has ever saved serializes the field. An empty
	// value means off, so a config written before this key existed keeps the
	// old behaviour without a migration.
	DreamMode string `mapstructure:"dream_mode" json:"dream_mode,omitempty" yaml:"dream_mode,omitempty"`
	// SubagentMaxDepth bounds how deep a delegate_subagent chain may nest. A
	// zero value falls back to DefaultSubagentMaxDepth.
	SubagentMaxDepth int `mapstructure:"subagent_max_depth" json:"subagent_max_depth,omitempty" yaml:"subagent_max_depth,omitempty"`
	// Timeout bounds one agent turn. A zero value means agent.DefaultTimeout,
	// which is why the key is omitempty: it is new, so no config in the wild
	// carries it, and an absent value must keep the old behaviour without a
	// schema migration (the trap the streaming bool hit at v4→v5).
	//
	// It exists because 120s is wrong for exactly the deployment that cannot
	// patch the binary: a cold local model with a large prompt runs past it,
	// and the operator had no knob at all (#241).
	Timeout Duration `mapstructure:"timeout" json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// ModelConfig defines a single model with its API configuration.
type ModelConfig struct {
	Name      string            `mapstructure:"name" json:"name" yaml:"name"`
	Model     string            `mapstructure:"model" json:"model" yaml:"model"`
	APIKey    string            `mapstructure:"api_key" json:"api_key,omitempty" yaml:"api_key,omitempty"`
	APIKeys   []string          `mapstructure:"api_keys" json:"api_keys,omitempty" yaml:"api_keys,omitempty"`
	APIBase   string            `mapstructure:"api_base" json:"api_base,omitempty" yaml:"api_base,omitempty"`
	Extra     map[string]string `mapstructure:"extra" json:"extra,omitempty" yaml:"extra,omitempty"`
	ExtraBody map[string]any    `mapstructure:"extra_body" json:"extra_body,omitempty" yaml:"extra_body,omitempty"`
	Disabled  bool              `mapstructure:"disabled" json:"disabled,omitempty" yaml:"disabled,omitempty"`
	MaxTokens int               `mapstructure:"max_tokens" json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
}

// AgentModelConfig holds agent model configuration (new simplified structure).
type AgentModelConfig struct {
	Model    string   `mapstructure:"model" json:"model" yaml:"model"`
	Fallback []string `mapstructure:"fallback" json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

// ModelsConfig holds all model configurations.
type ModelsConfig struct {
	Models []ModelConfig    `mapstructure:"models" json:"models" yaml:"models"`
	Agent  AgentModelConfig `mapstructure:"agent" json:"agent" yaml:"agent"`
}

// ProviderInfo contains detected provider information.
type ProviderInfo struct {
	Name      string
	APIFormat string
	BaseURL   string
}

// ResolvedModelConfig is a fully resolved model configuration.
type ResolvedModelConfig struct {
	Name      string
	ModelID   string
	Provider  string
	APIFormat string
	APIBase   string
	APIKey    string
	APIKeys   []string
	Extra     map[string]string
	ExtraBody map[string]any
	MaxTokens int
}

// providerPrefixes maps model prefixes to provider info.
var providerPrefixes = map[string]ProviderInfo{
	"anthropic/":  {Name: "anthropic", APIFormat: "anthropic", BaseURL: "https://api.anthropic.com"},
	"openai/":     {Name: "openai", APIFormat: "openai", BaseURL: "https://api.openai.com/v1"},
	"groq/":       {Name: "groq", APIFormat: "openai", BaseURL: "https://api.groq.com/openai/v1"},
	"ollama/":     {Name: "ollama", APIFormat: "openai", BaseURL: "http://localhost:11434/v1"},
	"poolside/":   {Name: "poolside", APIFormat: "openai", BaseURL: "https://inference.poolside.ai/v1"},
	"openrouter/": {Name: "openrouter", APIFormat: "openai", BaseURL: "https://openrouter.ai/api/v1"},
	"nvidia/":     {Name: "nvidia", APIFormat: "openai", BaseURL: "https://integrate.api.nvidia.com/v1"},
	"deepseek/":   {Name: "deepseek", APIFormat: "openai", BaseURL: "https://api.deepseek.com/v1"},
	"gemini/":     {Name: "gemini", APIFormat: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta"},
	"cerebras/":   {Name: "cerebras", APIFormat: "openai", BaseURL: "https://api.cerebras.ai/v1"},
	"custom/":     {Name: "custom", APIFormat: "openai", BaseURL: ""},
}

// DetectProvider extracts provider info from a model string.
func DetectProvider(model string) ProviderInfo {
	for prefix, info := range providerPrefixes {
		if strings.HasPrefix(model, prefix) {
			return info
		}
	}
	return ProviderInfo{Name: "unknown", APIFormat: "openai", BaseURL: ""}
}

// prefixesPartOfModelID lists providers whose published model IDs genuinely
// begin with the prefix — it identifies the model, it is not a routing hint
// joshbot added. Stripping it produces a name the API does not know.
//
// Poolside is one: https://inference.poolside.ai/v1/models lists
// "poolside/laguna-s-2.1", and the chat endpoint answers 200 for that and
// 404 "please check the model you provided" for "laguna-s-2.1".
var prefixesPartOfModelID = map[string]bool{
	"poolside/": true,
}

// StripProviderPrefix removes the provider prefix from a model name, except
// where the prefix is part of the model ID the provider expects.
func StripProviderPrefix(model string) string {
	for prefix := range providerPrefixes {
		if strings.HasPrefix(model, prefix) {
			if prefixesPartOfModelID[prefix] {
				return model
			}
			return strings.TrimPrefix(model, prefix)
		}
	}
	return model
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

// DiscordConfig holds Discord channel configuration.
type DiscordConfig struct {
	Enabled bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Token   string `mapstructure:"token" json:"token" yaml:"token"`
	// AllowFrom is enforced deny-by-default: an empty list rejects every
	// sender. Entries are numeric Discord user IDs (snowflakes) or usernames.
	AllowFrom []string `mapstructure:"allow_from" json:"allow_from" yaml:"allow_from"`
}

// ChannelsConfig holds channels configuration.
type ChannelsConfig struct {
	Telegram TelegramConfig `mapstructure:"telegram" json:"telegram" yaml:"telegram"`
	Discord  DiscordConfig  `mapstructure:"discord" json:"discord" yaml:"discord"`
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
	// ShellSandbox selects OS-level containment for shell commands:
	// "off" (default) or "workspace". Enforced on Linux (Landlock) and macOS
	// (Seatbelt); on a platform with no sandbox a non-off value is reported as
	// an error rather than silently ignored, and the shell tool there defaults
	// to allowlist-only instead of trusting the bypassable deny list.
	ShellSandbox string `mapstructure:"shell_sandbox" json:"shell_sandbox,omitempty" yaml:"shell_sandbox,omitempty"`
	// ShellSandboxAllowNetwork permits outbound TCP from sandboxed commands.
	// Off by default: exfiltrating what was read is the usual goal of an
	// attack that gets as far as running commands.
	ShellSandboxAllowNetwork bool `mapstructure:"shell_sandbox_allow_network" json:"shell_sandbox_allow_network,omitempty" yaml:"shell_sandbox_allow_network,omitempty"`
	// ShellApproval gates shell commands behind a human decision:
	// "off" (default), "interactive" (ask, and allow a remembered "yes to
	// everything" for the session) or "always" (ask for every command, with
	// no remembered answer). An unknown value is a startup error, like
	// ShellSandbox. Turns nobody is watching — cron, heartbeat — carry no
	// approver and are denied outright rather than left blocking.
	ShellApproval string `mapstructure:"shell_approval" json:"shell_approval,omitempty" yaml:"shell_approval,omitempty"`
}

// GatewayConfig holds gateway server configuration.
type GatewayConfig struct {
	Host string `mapstructure:"host" json:"host" yaml:"host"`
	Port int    `mapstructure:"port" json:"port" yaml:"port"`
}

// APIConfig configures the OpenAI-compatible HTTP server started by
// `joshbot serve`. There is deliberately no Enabled flag: running the command
// is the opt-in, and a bool here would be written into every saved config
// without omitempty, which makes its default impossible to change later
// without a schema migration (see the streaming v4→v5 note in AGENTS.md).
type APIConfig struct {
	// Listen is the address the server binds, host:port. It defaults to
	// loopback because the endpoint hands callers the full agent, tools
	// included; binding a wider interface is an explicit operator act.
	Listen string `mapstructure:"listen" json:"listen,omitempty" yaml:"listen,omitempty"`

	// APIKeys are the accepted bearer credentials. At least one is required —
	// `joshbot serve` refuses to start without one, because an unauthenticated
	// endpoint that reaches the shell tool is remote code execution.
	APIKeys []string `mapstructure:"api_keys" json:"api_keys,omitempty" yaml:"api_keys,omitempty"`
}

// HeartbeatConfig configures the HEARTBEAT.md proactive task scanner.
type HeartbeatConfig struct {
	// Interval is how often HEARTBEAT.md is scanned for unchecked tasks, as a Go
	// duration string (e.g. "30m", "1h", "1h30m"). Empty or unparseable falls
	// back to the 30m default. Overridable with JOSHBOT_HEARTBEAT__INTERVAL.
	Interval string `mapstructure:"interval" json:"interval,omitempty" yaml:"interval,omitempty"`
}

// UserConfig holds user preferences for personalization.
type UserConfig struct {
	Name string `mapstructure:"name" json:"name,omitempty" yaml:"name,omitempty"`
}

// Config is the root configuration for joshbot.
// MCPServerConfig configures a single stdio MCP (Model Context Protocol) server
// that joshbot spawns and connects to over its stdin/stdout. Its discovered
// tools are registered under a namespaced name (mcp__<server>__<tool>) so a
// server can never shadow a built-in tool such as shell or filesystem.
//
// Enabled mirrors the provider convention: a server is inert until it is set,
// so a half-configured entry never spawns a process.
type MCPServerConfig struct {
	Command string            `mapstructure:"command" json:"command,omitempty" yaml:"command,omitempty"`
	Args    []string          `mapstructure:"args" json:"args,omitempty" yaml:"args,omitempty"`
	Env     map[string]string `mapstructure:"env" json:"env,omitempty" yaml:"env,omitempty"`
	Enabled bool              `mapstructure:"enabled" json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// MCPConfig holds the configured MCP servers keyed by operator-chosen name.
type MCPConfig struct {
	Servers map[string]MCPServerConfig `mapstructure:"servers" json:"servers,omitempty" yaml:"servers,omitempty"`
}

type Config struct {
	SchemaVersion int `mapstructure:"schema_version" json:"schema_version" yaml:"schema_version"`

	// New model-centric config (preferred)
	ModelsConfig ModelsConfig `mapstructure:"models_config" json:"models_config,omitempty" yaml:"models_config,omitempty"`

	// Legacy provider-centric config (still supported for backward compatibility)
	Providers        map[string]ProviderConfig `mapstructure:"providers" json:"providers,omitempty" yaml:"providers,omitempty"`
	ProviderDefaults ProviderDefaults          `mapstructure:"provider_defaults" json:"provider_defaults,omitempty" yaml:"provider_defaults,omitempty"`
	Agents           AgentsConfig              `mapstructure:"agents" json:"agents" yaml:"agents"`

	// Other config sections
	Channels  ChannelsConfig  `mapstructure:"channels" json:"channels" yaml:"channels"`
	Tools     ToolsConfig     `mapstructure:"tools" json:"tools" yaml:"tools"`
	Gateway   GatewayConfig   `mapstructure:"gateway" json:"gateway" yaml:"gateway"`
	API       APIConfig       `mapstructure:"api" json:"api,omitempty" yaml:"api,omitempty"`
	Heartbeat HeartbeatConfig `mapstructure:"heartbeat" json:"heartbeat,omitempty" yaml:"heartbeat,omitempty"`
	LogLevel  string          `mapstructure:"log_level" json:"log_level" yaml:"log_level"`
	User      UserConfig      `mapstructure:"user" json:"user,omitempty" yaml:"user,omitempty"`

	// MCP configures Model Context Protocol servers whose tools are exposed to
	// the agent. Declaring a server here is a privileged, operator-only act:
	// config.json lives outside the workspace and cannot be written by a
	// workspace-confined tool, so it is the trust boundary for MCP (see
	// SECURITY.md). A server runs only when its Enabled flag is set.
	MCP MCPConfig `mapstructure:"mcp" json:"mcp,omitempty" yaml:"mcp,omitempty"`

	// Profiles are named endpoint/model setups selectable with --profile. See
	// profiles.go; a profile never holds a credential.
	Profiles map[string]Profile `mapstructure:"profiles" json:"profiles,omitempty" yaml:"profiles,omitempty"`
	// DefaultProfile names the profile used when --profile is absent. Empty
	// means no profile is applied and the rest of the config is used as-is.
	DefaultProfile string `mapstructure:"default_profile" json:"default_profile,omitempty" yaml:"default_profile,omitempty"`

	// activeProfile records the profile ApplyProfile installed, for status
	// output. Unexported for the same reason as credentialSource: derived
	// state must not round-trip through Save.
	activeProfile string

	// credentialSource records where each provider's API key came from, for
	// `joshbot preflight`. Unexported and unserialised on purpose: it is
	// derived state, and a round-trip through Save must not write it back.
	credentialSource map[string]string
}

// HeartbeatInterval returns the configured heartbeat scan interval, falling back
// to 30m when it is unset, unparseable, or non-positive.
func (c *Config) HeartbeatInterval() time.Duration {
	const def = 30 * time.Minute
	if c == nil || c.Heartbeat.Interval == "" {
		return def
	}
	d, err := time.ParseDuration(c.Heartbeat.Interval)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// parseConfigFromFile parses JSON config data into the Config struct.
func parseConfigFromFile(data []byte, cfg *Config) error {
	return json.Unmarshal(data, cfg)
}

// serializeConfig serializes the Config struct to JSON.
func serializeConfig(cfg *Config) ([]byte, error) {
	return json.MarshalIndent(withoutEnvCredentials(cfg), "", "  ")
}

// splitEnvList parses a comma-separated env var into a trimmed list, dropping
// empty entries so a stray comma cannot add a blank allowlist member.
func splitEnvList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// applyEnvOverrides applies environment variable overrides to the config.
func applyEnvOverrides(cfg *Config) error {
	// Helper to get env var with prefix
	getEnv := func(key string) string {
		return os.Getenv("JOSHBOT_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_")))
	}

	// Schema version
	if v := getEnv("SCHEMA_VERSION"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.SchemaVersion)
	}

	// Heartbeat interval, e.g. JOSHBOT_HEARTBEAT__INTERVAL=30m
	if v := getEnv("HEARTBEAT__INTERVAL"); v != "" {
		cfg.Heartbeat.Interval = v
	}

	// OpenAI-compatible API server, e.g. JOSHBOT_API__LISTEN=127.0.0.1:9000
	if v := getEnv("API__LISTEN"); v != "" {
		cfg.API.Listen = v
	}
	// Comma-separated, because the whole point of this variable is to supply the
	// credential without writing it to disk — a JSON array in an env var would
	// be worse ergonomics for the container and systemd cases it exists for.
	// It replaces the configured list rather than adding to it, so an operator
	// can revoke a key in the file by overriding here.
	if v := getEnv("API__API_KEYS"); v != "" {
		cfg.API.APIKeys = nil
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				cfg.API.APIKeys = append(cfg.API.APIKeys, k)
			}
		}
	}

	// Model-centric config support
	// Format: JOSHBOT_MODELS_CONFIG__AGENT__MODEL=smart
	//         JOSHBOT_MODELS_CONFIG__MODELS__0__NAME=smart
	//         JOSHBOT_MODELS_CONFIG__MODELS__0__MODEL=openai/gpt-4
	//         JOSHBOT_MODELS_CONFIG__MODELS__0__API_KEY=sk-xxx
	if v := getEnv("MODELS_CONFIG__AGENT__MODEL"); v != "" {
		cfg.ModelsConfig.Agent.Model = v
	}

	if v := getEnv("MODELS_CONFIG__AGENT__FALLBACK"); v != "" {
		cfg.ModelsConfig.Agent.Fallback = strings.Split(v, ",")
		for i := range cfg.ModelsConfig.Agent.Fallback {
			cfg.ModelsConfig.Agent.Fallback[i] = strings.TrimSpace(cfg.ModelsConfig.Agent.Fallback[i])
		}
	}

	// Parse model configs from env (indexed approach)
	for i := 0; ; i++ {
		prefix := fmt.Sprintf("MODELS_CONFIG__MODELS__%d__", i)
		name := getEnv(prefix + "NAME")
		if name == "" {
			break
		}

		apiKey := getEnv(prefix + "API_KEY")
		apiBase := getEnv(prefix + "API_BASE")
		modelID := getEnv(prefix + "MODEL")

		// Check if model already exists (update mode)
		found := false
		for j := range cfg.ModelsConfig.Models {
			if cfg.ModelsConfig.Models[j].Name == name {
				// Update existing model
				if apiKey != "" {
					cfg.ModelsConfig.Models[j].APIKey = apiKey
				}
				if apiBase != "" {
					cfg.ModelsConfig.Models[j].APIBase = apiBase
				}
				if modelID != "" {
					cfg.ModelsConfig.Models[j].Model = modelID
				}
				if v := getEnv(prefix + "DISABLED"); v != "" {
					cfg.ModelsConfig.Models[j].Disabled = v == "true" || v == "1"
				}
				if v := getEnv(prefix + "MAX_TOKENS"); v != "" {
					fmt.Sscanf(v, "%d", &cfg.ModelsConfig.Models[j].MaxTokens)
				}
				found = true
				break
			}
		}

		// If not found, create new model
		if !found {
			model := ModelConfig{
				Name:    name,
				Model:   modelID,
				APIKey:  apiKey,
				APIBase: apiBase,
			}

			if v := getEnv(prefix + "DISABLED"); v != "" {
				model.Disabled = v == "true" || v == "1"
			}

			if v := getEnv(prefix + "MAX_TOKENS"); v != "" {
				fmt.Sscanf(v, "%d", &model.MaxTokens)
			}

			cfg.ModelsConfig.Models = append(cfg.ModelsConfig.Models, model)
		}
	}

	// Model (legacy)
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

	// Max memory size
	if v := getEnv("AGENTS__DEFAULTS__MAX_MEMORY_SIZE"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Agents.Defaults.MaxMemorySize)
	}

	// Dream consolidation mode
	if v := getEnv("AGENTS__DEFAULTS__DREAM_MODE"); v != "" {
		cfg.Agents.Defaults.DreamMode = v
	}

	// Agent turn timeout. Parsed rather than Sscanf'd, and reported rather
	// than ignored: this key exists for the env-only deployment (#241), where
	// silently discarding a mistyped value leaves the operator on the default
	// they were trying to raise, with nothing anywhere saying so.
	if v := getEnv("AGENTS__DEFAULTS__TIMEOUT"); v != "" {
		d, err := parseDurationEnv(v)
		if err != nil {
			return fatalConfigError{fmt.Errorf("JOSHBOT_AGENTS__DEFAULTS__TIMEOUT: %w", err)}
		}
		cfg.Agents.Defaults.Timeout = d
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

	// Telegram allowlist (comma-separated). Both channels warn at startup that
	// this env var is the way to set the allowlist, so it has to exist.
	if v := getEnv("CHANNELS__TELEGRAM__ALLOW_FROM"); v != "" {
		cfg.Channels.Telegram.AllowFrom = splitEnvList(v)
	}

	// Discord enabled
	if v := getEnv("CHANNELS__DISCORD__ENABLED"); v != "" {
		cfg.Channels.Discord.Enabled = v == "true" || v == "1"
	}

	// Discord token
	if v := getEnv("CHANNELS__DISCORD__TOKEN"); v != "" {
		cfg.Channels.Discord.Token = v
	}

	// Discord allowlist (comma-separated)
	if v := getEnv("CHANNELS__DISCORD__ALLOW_FROM"); v != "" {
		cfg.Channels.Discord.AllowFrom = splitEnvList(v)
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
	// Canonical: JOSHBOT_PROVIDERS__OPENROUTER__API_KEY
	// Shorthand (also accepted): JOSHBOT_OPENROUTER_API_KEY
	orKey := getEnv("PROVIDERS__OPENROUTER__API_KEY")
	if orKey == "" {
		orKey = os.Getenv("JOSHBOT_OPENROUTER_API_KEY")
	}
	if orKey != "" {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]ProviderConfig)
		}
		if p, ok := cfg.Providers["openrouter"]; ok {
			p.APIKey = orKey
			cfg.Providers["openrouter"] = p
		} else {
			cfg.Providers["openrouter"] = ProviderConfig{APIKey: orKey, Enabled: true}
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

	// Shorthand: JOSHBOT_NVIDIA_API_KEY
	nvKey := getEnv("PROVIDERS__NVIDIA__API_KEY")
	if nvKey == "" {
		nvKey = os.Getenv("JOSHBOT_NVIDIA_API_KEY")
	}
	if nvKey != "" {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]ProviderConfig)
		}
		if p, ok := cfg.Providers["nvidia"]; ok {
			p.APIKey = nvKey
			cfg.Providers["nvidia"] = p
		} else {
			cfg.Providers["nvidia"] = ProviderConfig{APIKey: nvKey, Enabled: true}
		}
	}

	// The canonical per-provider override, for every configured provider
	// rather than the two that had hand-written blocks above. It runs last
	// because it is the highest-precedence source, ahead of api_key_env and
	// ahead of the literal api_key.
	for name, p := range cfg.Providers {
		key := providerEnvKey(name)
		if v := os.Getenv(key); v != "" {
			p.APIKey = strings.TrimSpace(v)
			cfg.Providers[name] = p
			cfg.noteCredentialSource(name, CredentialFromEnv(key))
		}
	}
	if os.Getenv("JOSHBOT_OPENROUTER_API_KEY") != "" && os.Getenv(providerEnvKey("openrouter")) == "" {
		cfg.noteCredentialSource("openrouter", CredentialFromEnv("JOSHBOT_OPENROUTER_API_KEY"))
	}
	if os.Getenv("JOSHBOT_NVIDIA_API_KEY") != "" && os.Getenv(providerEnvKey("nvidia")) == "" {
		cfg.noteCredentialSource("nvidia", CredentialFromEnv("JOSHBOT_NVIDIA_API_KEY"))
	}
	return nil
}

// Defaults returns a Config with all default values set.
func Defaults() *Config {
	return &Config{
		SchemaVersion: CurrentSchemaVersion,
		// New model-centric config (empty by default, user must configure)
		ModelsConfig: ModelsConfig{
			Models: []ModelConfig{},
			Agent: AgentModelConfig{
				Model:    "",
				Fallback: []string{},
			},
		},
		// Legacy provider config (still supported)
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
				MaxMemorySize:       DefaultMaxMemorySize,
				Streaming:           DefaultStreaming,
			},
		},
		Channels: ChannelsConfig{
			Telegram: TelegramConfig{
				Enabled:   false,
				Token:     "",
				AllowFrom: []string{},
				Proxy:     "",
			},
			Discord: DiscordConfig{
				Enabled:   false,
				Token:     "",
				AllowFrom: []string{},
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
		// API.Listen is deliberately left empty rather than seeded with
		// DefaultAPIListen. `omitempty` on a struct field is a no-op — Go emits
		// the object either way — so a non-empty default here would be written
		// into every config joshbot ever saves, by onboard, by configure, by any
		// save at all. Changing the default port later would then reach nobody
		// who had saved a config since, exactly the trap that made the
		// `streaming` default a v4→v5 schema migration. The default is resolved
		// at use time instead, in cmd/joshbot's runServe.
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

// LogsDir is where the gateway and the installed service write their logs.
func (c *Config) LogsDir() string {
	return filepath.Join(DefaultHome, "logs")
}

// EnsureDirs creates all required directories for joshbot.
func (c *Config) EnsureDirs() error {
	dirs := []string{
		c.HomeDir(),
		c.WorkspaceDir(),
		c.SessionsDir(),
		c.MediaDir(),
		c.CronDir(),
		c.LogsDir(),
		filepath.Join(c.WorkspaceDir(), "memory"),
		filepath.Join(c.WorkspaceDir(), "skills"),
	}

	// Owner-only, and re-applied to directories that already exist.
	//
	// MkdirAll leaves an existing directory's mode alone, so an install
	// created before this was tightened keeps whatever it had. It also means
	// these 0755 directories used to win over the 0750 that
	// session.NewManager asks for, since onboarding creates the tree first:
	// session *files* were 0600 while the directory holding them stayed
	// world-listable, and a session filename is "telegram:<senderID>" — the
	// identity of everyone who talks to this bot.
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return err
		}
		if err := os.Chmod(dir, dirMode); err != nil {
			return err
		}
	}
	return nil
}

// UseModelsConfig returns true if the new model-centric config is being used.
func (c *Config) UseModelsConfig() bool {
	return len(c.ModelsConfig.Models) > 0
}

// GetModel returns a model config by name.
func (c *Config) GetModel(name string) (ModelConfig, bool) {
	for _, m := range c.ModelsConfig.Models {
		if m.Name == name {
			return m, true
		}
	}
	return ModelConfig{}, false
}

// GetActiveModel returns the currently configured model.
func (c *Config) GetActiveModel() (ModelConfig, error) {
	modelName := c.ModelsConfig.Agent.Model
	if modelName == "" {
		return ModelConfig{}, errors.New("no model configured")
	}

	model, ok := c.GetModel(modelName)
	if !ok {
		return ModelConfig{}, fmt.Errorf("model not found: %s", modelName)
	}

	return model, nil
}

// GetFallbackModels returns the fallback model chain.
func (c *Config) GetFallbackModels() []ModelConfig {
	var models []ModelConfig
	for _, name := range c.ModelsConfig.Agent.Fallback {
		if m, ok := c.GetModel(name); ok && !m.Disabled {
			models = append(models, m)
		}
	}
	return models
}

// ResolveModelConfig resolves the full configuration for a model.
func (c *Config) ResolveModelConfig(name string) (ResolvedModelConfig, error) {
	model, ok := c.GetModel(name)
	if !ok {
		return ResolvedModelConfig{}, fmt.Errorf("model not found: %s", name)
	}

	if model.Disabled {
		return ResolvedModelConfig{}, fmt.Errorf("model is disabled: %s", name)
	}

	provider := DetectProvider(model.Model)

	apiBase := model.APIBase
	if apiBase == "" {
		apiBase = provider.BaseURL
	}

	if apiBase == "" {
		return ResolvedModelConfig{}, fmt.Errorf("model %s: api_base required for unknown provider", name)
	}

	// Validate API key for providers that require it
	if model.APIKey == "" {
		// Some providers don't require API keys (e.g., local Ollama)
		if provider.Name != "ollama" {
			return ResolvedModelConfig{}, fmt.Errorf("model %s: api_key required for provider %s", name, provider.Name)
		}
	}

	modelID := StripProviderPrefix(model.Model)

	apiKeys := model.APIKeys
	if model.APIKey != "" {
		hasExplicit := false
		for _, k := range apiKeys {
			if k == model.APIKey {
				hasExplicit = true
				break
			}
		}
		if !hasExplicit {
			apiKeys = append([]string{model.APIKey}, apiKeys...)
		}
	}

	return ResolvedModelConfig{
		Name:      model.Name,
		ModelID:   modelID,
		Provider:  provider.Name,
		APIFormat: provider.APIFormat,
		APIBase:   apiBase,
		APIKey:    model.APIKey,
		APIKeys:   apiKeys,
		Extra:     model.Extra,
		ExtraBody: model.ExtraBody,
		MaxTokens: model.MaxTokens,
	}, nil
}

// GetAllModelConfigs returns all enabled model configurations resolved.
func (c *Config) GetAllModelConfigs() []ResolvedModelConfig {
	var configs []ResolvedModelConfig

	activeModel := c.ModelsConfig.Agent.Model
	if resolved, err := c.ResolveModelConfig(activeModel); err == nil {
		configs = append(configs, resolved)
	}

	for _, fallback := range c.ModelsConfig.Agent.Fallback {
		if fallback == activeModel {
			continue
		}
		if resolved, err := c.ResolveModelConfig(fallback); err == nil {
			configs = append(configs, resolved)
		}
	}

	for _, m := range c.ModelsConfig.Models {
		if m.Name == activeModel {
			continue
		}
		found := false
		for _, f := range c.ModelsConfig.Agent.Fallback {
			if f == m.Name {
				found = true
				break
			}
		}
		if found {
			continue
		}
		if resolved, err := c.ResolveModelConfig(m.Name); err == nil {
			configs = append(configs, resolved)
		}
	}

	return configs
}

// Validate validates the configuration and returns an error if invalid.
func (c *Config) Validate() error {
	// Validate new model-centric config if present
	if c.UseModelsConfig() {
		if len(c.ModelsConfig.Models) == 0 {
			return errors.New("no models configured")
		}
		// Expand ~ in API keys if present
		for i, m := range c.ModelsConfig.Models {
			if strings.HasPrefix(m.APIKey, "~/") {
				c.ModelsConfig.Models[i].APIKey = filepath.Join(os.Getenv("HOME"), m.APIKey[1:])
			}
		}

		if c.ModelsConfig.Agent.Model == "" {
			return errors.New("no active model configured")
		}

		// Check active model exists
		if _, ok := c.GetModel(c.ModelsConfig.Agent.Model); !ok {
			return fmt.Errorf("active model not found: %s", c.ModelsConfig.Agent.Model)
		}

		// Check fallback models exist
		for _, name := range c.ModelsConfig.Agent.Fallback {
			if _, ok := c.GetModel(name); !ok {
				return fmt.Errorf("fallback model not found: %s", name)
			}
		}

		// Validate each model has required fields
		for _, m := range c.ModelsConfig.Models {
			if m.Name == "" {
				return errors.New("model name cannot be empty")
			}
			if m.Model == "" {
				return fmt.Errorf("model %s: model ID cannot be empty", m.Name)
			}

			// Check API base is provided for unknown providers
			provider := DetectProvider(m.Model)
			if provider.Name == "unknown" && m.APIBase == "" {
				return fmt.Errorf("model %s: api_base required for unknown provider", m.Name)
			}
		}
	} else {
		// Legacy provider-centric validation
		if c.Agents.Defaults.Model == "" {
			return errors.New("model cannot be empty")
		}
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

	// Validate subagent_max_depth is positive
	if c.Agents.Defaults.SubagentMaxDepth < 0 {
		return errors.New("subagent_max_depth must not be negative")
	}

	// Validate memory_window is positive
	if c.Agents.Defaults.MemoryWindow <= 0 {
		return errors.New("memory_window must be positive")
	}

	// Validate compaction_threshold is in valid range
	if c.Agents.Defaults.CompactionThreshold <= 0 || c.Agents.Defaults.CompactionThreshold > 1 {
		return errors.New("compaction_threshold must be between 0 and 1")
	}

	// Validate max_memory_size is positive
	if c.Agents.Defaults.MaxMemorySize <= 0 {
		return errors.New("max_memory_size must be positive")
	}

	// Validate dream_mode. An unknown value is a startup error rather than a
	// silent fallback to off: an operator who typed "record-only" would
	// otherwise get no recording and no explanation.
	switch c.Agents.Defaults.DreamMode {
	case "", DreamModeOff, DreamModeRecord, DreamModeFull:
	default:
		return fmt.Errorf("dream_mode must be one of %q, %q or %q, got %q",
			DreamModeOff, DreamModeRecord, DreamModeFull, c.Agents.Defaults.DreamMode)
	}

	// Validate exec timeout is positive
	if c.Tools.Exec.Timeout <= 0 {
		return errors.New("exec timeout must be positive")
	}

	// A sub-second timeout is never intentional and fails every request the
	// moment it is used, blaming the context rather than the config. Catching
	// it here names the key and the value it parsed to, at load, instead of at
	// the first request (#240).
	if err := validateTimeout("agents.defaults.timeout", c.Agents.Defaults.Timeout); err != nil {
		return err
	}
	for name, p := range c.Providers {
		if err := validateTimeout("providers."+name+".timeout", p.Timeout); err != nil {
			return err
		}
		if p.MaxRetries != nil && (*p.MaxRetries < 0 || *p.MaxRetries > 10) {
			return fmt.Errorf("providers.%s.max_retries must be between 0 and 10, got %d", name, *p.MaxRetries)
		}
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
// fatalConfigError marks a failure Load must not paper over with defaults.
type fatalConfigError struct{ error }

func (e fatalConfigError) Unwrap() error { return e.error }

// loadFileConfig builds a config from raw config-file bytes, reporting every
// failure instead of substituting defaults.
//
// It returns the config it built alongside any error, because the broken config
// is exactly what `joshbot preflight` has to describe: a config that Load has
// silently replaced with defaults is the condition preflight exists to catch,
// and reporting the defaults instead would send the operator to inspect a file
// that has nothing to do with what they are seeing.
func loadFileConfig(data []byte) (*Config, error) {
	cfg := Defaults()
	if err := parseConfigFromFile(data, cfg); err != nil {
		return cfg, fmt.Errorf("parse config file: %w", err)
	}

	// Before the env overrides, so JOSHBOT_PROVIDERS__<NAME>__API_KEY still
	// wins and the both-fields-set check sees the operator's file rather than
	// a key the environment supplied.
	if err := resolveProviderCredentials(cfg); err != nil {
		return cfg, fatalConfigError{err}
	}

	// Fatal for the same reason: a profile carrying a raw api_key, or a
	// default_profile naming something that does not exist, must not be
	// papered over with a default config that silently dials somewhere else.
	if err := validateProfiles(cfg); err != nil {
		return cfg, fatalConfigError{err}
	}

	if err := applyEnvOverrides(cfg); err != nil {
		return cfg, err
	}

	// Sanitize string fields that may have whitespace from user input
	for name, p := range cfg.Providers {
		p.APIKey = strings.TrimSpace(p.APIKey)
		p.APIBase = strings.TrimSpace(p.APIBase)
		cfg.Providers[name] = p
	}
	// Also sanitize model API keys
	for i := range cfg.ModelsConfig.Models {
		cfg.ModelsConfig.Models[i].APIKey = strings.TrimSpace(cfg.ModelsConfig.Models[i].APIKey)
		cfg.ModelsConfig.Models[i].APIBase = strings.TrimSpace(cfg.ModelsConfig.Models[i].APIBase)
	}
	cfg.Channels.Telegram.Token = strings.TrimSpace(cfg.Channels.Telegram.Token)
	cfg.Agents.Defaults.Model = strings.TrimSpace(cfg.Agents.Defaults.Model)

	if cfg.SchemaVersion < CurrentSchemaVersion {
		if err := migrateConfig(cfg, data); err != nil {
			return cfg, fmt.Errorf("migrate config: %w", err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// LoadStrict loads the config file without Load's fall back to defaults,
// returning the config as written alongside the first reason it is unusable.
//
// Load warns and substitutes defaults so that a broken file cannot stop joshbot
// from starting at all. That is the right behaviour for the daemon and the
// wrong behaviour for diagnostics: an operator running `joshbot preflight`
// wants to be told their active model is undefined, not handed a report about
// a default config they never wrote.
func LoadStrict() (*Config, error) {
	configPath := ConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := Defaults()
			if envErr := applyEnvOverrides(cfg); envErr != nil {
				return cfg, envErr
			}
			return cfg, fmt.Errorf("no config file at %s; run `joshbot onboard`", configPath)
		}
		return nil, err
	}
	return loadFileConfig(data)
}

func Load() (*Config, error) {
	// Check for config file
	configPath := ConfigPath()
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

		cfg, err := loadFileConfig(data)
		if err != nil {
			// A credential the operator asked for and did not get must not be
			// downgraded to a config nothing can dial: the failure would
			// otherwise resurface as a 401 mid-turn, which reads as a revoked
			// key and sends them to the provider dashboard instead of their
			// shell profile.
			var fatal fatalConfigError
			if errors.As(err, &fatal) {
				return nil, err
			}
			logger.Warn("Config unusable, using defaults", "error", err)
			cfg = Defaults()
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
		}
		return cfg, nil
	}

	// No config file, use defaults
	cfg := Defaults()

	// Apply environment variable overrides even without config file
	// A bad value here is fatal for the same reason it is in loadFileConfig:
	// an env-only deployment has no file to fall back to, and running on the
	// default the operator was trying to change is the silent failure.
	if err := applyEnvOverrides(cfg); err != nil {
		return nil, err
	}

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

// dirMode is the mode for every directory joshbot creates under ~/.joshbot.
//
// Owner-only: the tree holds conversations, memory, downloaded media and the
// config file with live provider keys. Even where the files themselves are
// 0600, a world-listable directory discloses the file names.
const dirMode = 0o700

// Save saves the configuration to the config file.
func Save(cfg *Config) error {
	// Ensure config directory exists
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), dirMode); err != nil {
		return err
	}

	// Write config to JSON file
	data, err := serializeConfig(cfg)
	if err != nil {
		return err
	}

	configPath := ConfigPath()
	// 0600: this file holds live provider API keys, so group and other must
	// have no access to it.
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
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
			cfg.Agents.Defaults.Model = "openrouter/free"
			logger.Info("Migrated model from google/gemma-2-9b-it:free to openrouter/free")
		}
		cfg.SchemaVersion = 1

		// Backup old config
		configPath := ConfigPath()
		if _, err := os.Stat(configPath); err == nil {
			backupPath := configPath + ".bak"
			if data, err := os.ReadFile(configPath); err == nil {
				// The backup is a verbatim copy of config.json, API keys
				// included, so it gets the same 0600 treatment.
				_ = os.WriteFile(backupPath, data, 0o600)
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

	// Migration from v4 to v5
	if cfg.SchemaVersion < 5 {
		// streaming flipped from opt-in to on by default. A stored false cannot
		// be taken at face value here: the field has no omitempty, so every
		// config any v1.47.x wrote — onboard, configure, any save at all —
		// carries "streaming": false whether or not the operator ever saw the
		// key, and honouring that would ship the new default to nobody. The
		// window in which someone could have disabled it deliberately is one
		// patch release of an opt-in feature, so the flag is reset to the
		// default once, here, and respected from v5 on.
		if !cfg.Agents.Defaults.Streaming {
			cfg.Agents.Defaults.Streaming = DefaultStreaming
			logger.Info("Migrated config to v5: streaming is now on by default (set agents.defaults.streaming to false to opt out)")
		}
		cfg.SchemaVersion = 5
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
