package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()

	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("expected schema version %d, got %d", CurrentSchemaVersion, cfg.SchemaVersion)
	}

	if cfg.Agents.Defaults.Model != DefaultModel {
		t.Errorf("expected default model %s, got %s", DefaultModel, cfg.Agents.Defaults.Model)
	}

	if cfg.Agents.Defaults.MaxTokens != DefaultMaxTokens {
		t.Errorf("expected max tokens %d, got %d", DefaultMaxTokens, cfg.Agents.Defaults.MaxTokens)
	}

	if cfg.Agents.Defaults.Temperature != DefaultTemperature {
		t.Errorf("expected temperature %f, got %f", DefaultTemperature, cfg.Agents.Defaults.Temperature)
	}

	if cfg.Agents.Defaults.MaxToolIterations != DefaultMaxToolIterations {
		t.Errorf("expected max tool iterations %d, got %d", DefaultMaxToolIterations, cfg.Agents.Defaults.MaxToolIterations)
	}

	if cfg.Agents.Defaults.MemoryWindow != DefaultMemoryWindow {
		t.Errorf("expected memory window %d, got %d", DefaultMemoryWindow, cfg.Agents.Defaults.MemoryWindow)
	}

	if cfg.Tools.Exec.Timeout != DefaultExecTimeout {
		t.Errorf("expected exec timeout %d, got %d", DefaultExecTimeout, cfg.Tools.Exec.Timeout)
	}

	if cfg.Gateway.Host != DefaultGatewayHost {
		t.Errorf("expected gateway host %s, got %s", DefaultGatewayHost, cfg.Gateway.Host)
	}

	if cfg.Gateway.Port != DefaultGatewayPort {
		t.Errorf("expected gateway port %d, got %d", DefaultGatewayPort, cfg.Gateway.Port)
	}

	if cfg.LogLevel != "info" {
		t.Errorf("expected log level 'info', got %s", cfg.LogLevel)
	}

	if cfg.Channels.Telegram.Enabled {
		t.Error("expected telegram to be disabled by default")
	}

	if cfg.Channels.Telegram.Token != "" {
		t.Errorf("expected empty telegram token, got %s", cfg.Channels.Telegram.Token)
	}

	if cfg.Providers == nil {
		t.Error("expected providers map to be initialized")
	}

	if _, ok := cfg.Providers["openrouter"]; !ok {
		t.Error("expected openrouter provider to be in providers map")
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			cfg: func() *Config {
				cfg := Defaults()
				return cfg
			}(),
			wantErr: false,
		},
		{
			name: "empty model",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Agents.Defaults.Model = ""
				return cfg
			}(),
			wantErr: true,
			errMsg:  "model cannot be empty",
		},
		{
			name: "invalid max_tokens",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Agents.Defaults.MaxTokens = 0
				return cfg
			}(),
			wantErr: true,
			errMsg:  "max_tokens must be positive",
		},
		{
			name: "invalid temperature too low",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Agents.Defaults.Temperature = -0.1
				return cfg
			}(),
			wantErr: true,
			errMsg:  "temperature must be between 0 and 2",
		},
		{
			name: "invalid temperature too high",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Agents.Defaults.Temperature = 2.1
				return cfg
			}(),
			wantErr: true,
			errMsg:  "temperature must be between 0 and 2",
		},
		{
			name: "valid temperature boundary low",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Agents.Defaults.Temperature = 0
				return cfg
			}(),
			wantErr: false,
		},
		{
			name: "valid temperature boundary high",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Agents.Defaults.Temperature = 2.0
				return cfg
			}(),
			wantErr: false,
		},
		{
			name: "invalid max_tool_iterations",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Agents.Defaults.MaxToolIterations = 0
				return cfg
			}(),
			wantErr: true,
			errMsg:  "max_tool_iterations must be positive",
		},
		{
			name: "invalid memory_window",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Agents.Defaults.MemoryWindow = 0
				return cfg
			}(),
			wantErr: true,
			errMsg:  "memory_window must be positive",
		},
		{
			name: "invalid exec timeout",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Tools.Exec.Timeout = 0
				return cfg
			}(),
			wantErr: true,
			errMsg:  "exec timeout must be positive",
		},
		{
			name: "invalid gateway port too low",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Gateway.Port = 0
				return cfg
			}(),
			wantErr: true,
			errMsg:  "gateway port must be between 1 and 65535",
		},
		{
			name: "invalid gateway port too high",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Gateway.Port = 65536
				return cfg
			}(),
			wantErr: true,
			errMsg:  "gateway port must be between 1 and 65535",
		},
		{
			name: "valid gateway port boundary low",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Gateway.Port = 1
				return cfg
			}(),
			wantErr: false,
		},
		{
			name: "valid gateway port boundary high",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.Gateway.Port = 65535
				return cfg
			}(),
			wantErr: false,
		},
		{
			name: "invalid log level",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.LogLevel = "invalid"
				return cfg
			}(),
			wantErr: true,
			errMsg:  "log_level must be one of: debug, info, warn, error",
		},
		{
			name: "valid log level debug",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.LogLevel = "debug"
				return cfg
			}(),
			wantErr: false,
		},
		{
			name: "valid log level warn",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.LogLevel = "warn"
				return cfg
			}(),
			wantErr: false,
		},
		{
			name: "valid log level error",
			cfg: func() *Config {
				cfg := Defaults()
				cfg.LogLevel = "error"
				return cfg
			}(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && err.Error() != tt.errMsg {
				t.Errorf("Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestConfigDirectories(t *testing.T) {
	cfg := Defaults()

	homeDir := cfg.HomeDir()
	if homeDir != DefaultHome {
		t.Errorf("expected home dir %s, got %s", DefaultHome, homeDir)
	}

	workspaceDir := cfg.WorkspaceDir()
	if workspaceDir != cfg.Agents.Defaults.Workspace {
		t.Errorf("expected workspace dir %s, got %s", cfg.Agents.Defaults.Workspace, workspaceDir)
	}

	sessionsDir := cfg.SessionsDir()
	expectedSessionsDir := filepath.Join(DefaultHome, "sessions")
	if sessionsDir != expectedSessionsDir {
		t.Errorf("expected sessions dir %s, got %s", expectedSessionsDir, sessionsDir)
	}

	mediaDir := cfg.MediaDir()
	expectedMediaDir := filepath.Join(DefaultHome, "media")
	if mediaDir != expectedMediaDir {
		t.Errorf("expected media dir %s, got %s", expectedMediaDir, mediaDir)
	}

	cronDir := cfg.CronDir()
	expectedCronDir := filepath.Join(DefaultHome, "cron")
	if cronDir != expectedCronDir {
		t.Errorf("expected cron dir %s, got %s", expectedCronDir, cronDir)
	}
}

func TestConfigEnsureDirs(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Temporarily override DefaultHome
	oldHome := DefaultHome
	DefaultHome = tmpDir
	oldWorkspace := DefaultWorkspace
	DefaultWorkspace = filepath.Join(tmpDir, "workspace")

	defer func() {
		DefaultHome = oldHome
		DefaultWorkspace = oldWorkspace
	}()

	cfg := Defaults()
	err := cfg.EnsureDirs()
	if err != nil {
		t.Errorf("EnsureDirs() error = %v", err)
	}

	// Check that directories were created
	expectedDirs := []string{
		cfg.HomeDir(),
		cfg.WorkspaceDir(),
		cfg.SessionsDir(),
		cfg.MediaDir(),
		cfg.CronDir(),
		filepath.Join(cfg.WorkspaceDir(), "memory"),
		filepath.Join(cfg.WorkspaceDir(), "skills"),
	}

	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("expected directory %s to exist", dir)
		}
	}
}

func TestConfigString(t *testing.T) {
	cfg := Defaults()
	str := cfg.String()

	expected := "Config{SchemaVersion: 1, Model: openai/gpt-4, LogLevel: info, Gateway: 0.0.0.0:18790}"
	if str != expected {
		t.Errorf("String() = %v, want %v", str, expected)
	}
}

func TestLoadNoConfigFile(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Temporarily override DefaultHome
	oldHome := DefaultHome
	DefaultHome = tmpDir

	defer func() {
		DefaultHome = oldHome
	}()

	cfg, err := Load()
	if err != nil {
		t.Errorf("Load() error = %v", err)
	}

	// Should return defaults
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("expected schema version %d, got %d", CurrentSchemaVersion, cfg.SchemaVersion)
	}
}

func TestLoadWithConfigFile(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Temporarily override DefaultHome
	oldHome := DefaultHome
	DefaultHome = tmpDir

	defer func() {
		DefaultHome = oldHome
	}()

	// Create a config file
	configPath := filepath.Join(tmpDir, "config.json")
	configContent := `{
		"schema_version": 1,
		"agents": {
			"defaults": {
				"model": "custom/model",
				"max_tokens": 4096
			}
		},
		"gateway": {
			"host": "localhost",
			"port": 8080
		}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Errorf("Load() error = %v", err)
	}

	// Check that config was loaded
	if cfg.Agents.Defaults.Model != "custom/model" {
		t.Errorf("expected model custom/model, got %s", cfg.Agents.Defaults.Model)
	}

	if cfg.Agents.Defaults.MaxTokens != 4096 {
		t.Errorf("expected max_tokens 4096, got %d", cfg.Agents.Defaults.MaxTokens)
	}

	if cfg.Gateway.Host != "localhost" {
		t.Errorf("expected gateway host localhost, got %s", cfg.Gateway.Host)
	}

	if cfg.Gateway.Port != 8080 {
		t.Errorf("expected gateway port 8080, got %d", cfg.Gateway.Port)
	}
}

func TestSaveConfig(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Temporarily override DefaultHome
	oldHome := DefaultHome
	DefaultHome = tmpDir

	defer func() {
		DefaultHome = oldHome
	}()

	cfg := Defaults()
	cfg.Agents.Defaults.Model = "test/model"
	cfg.Gateway.Port = 9999

	err := Save(cfg)
	if err != nil {
		t.Errorf("Save() error = %v", err)
	}

	// Check that config file was created
	configPath := filepath.Join(tmpDir, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("expected config file to exist")
	}

	// Load and verify
	loadedCfg, err := Load()
	if err != nil {
		t.Errorf("Load() error = %v", err)
	}

	if loadedCfg.Agents.Defaults.Model != "test/model" {
		t.Errorf("expected model test/model, got %s", loadedCfg.Agents.Defaults.Model)
	}

	if loadedCfg.Gateway.Port != 9999 {
		t.Errorf("expected gateway port 9999, got %d", loadedCfg.Gateway.Port)
	}
}

func TestEnvOverrides(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Temporarily override DefaultHome
	oldHome := DefaultHome
	DefaultHome = tmpDir

	// Set environment variables
	os.Setenv("JOSHBOT_AGENTS__DEFAULTS__MODEL", "env-model")
	os.Setenv("JOSHBOT_GATEWAY__PORT", "9000")
	os.Setenv("JOSHBOT_CHANNELS__TELEGRAM__ENABLED", "true")

	defer func() {
		DefaultHome = oldHome
		os.Unsetenv("JOSHBOT_AGENTS__DEFAULTS__MODEL")
		os.Unsetenv("JOSHBOT_GATEWAY__PORT")
		os.Unsetenv("JOSHBOT_CHANNELS__TELEGRAM__ENABLED")
	}()

	cfg, err := Load()
	if err != nil {
		t.Errorf("Load() error = %v", err)
	}

	// Check env overrides - note that model comes from Defaults() which is set before env overrides
	// The env override should work when there's no config file
	if cfg.Agents.Defaults.Model != "env-model" {
		t.Errorf("expected model env-model, got %s", cfg.Agents.Defaults.Model)
	}

	if cfg.Gateway.Port != 9000 {
		t.Errorf("expected gateway port 9000, got %d", cfg.Gateway.Port)
	}

	if !cfg.Channels.Telegram.Enabled {
		t.Error("expected telegram enabled from env")
	}
}

func TestMigrateConfig(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Temporarily override DefaultHome
	oldHome := DefaultHome
	DefaultHome = tmpDir

	defer func() {
		DefaultHome = oldHome
	}()

	// Create a config file with old schema version
	configPath := filepath.Join(tmpDir, "config.json")
	configContent := `{
		"schema_version": 0,
		"agents": {
			"defaults": {
				"model": "google/gemma-2-9b-it:free"
			}
		}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Errorf("Load() error = %v", err)
	}

	// Check migration
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("expected schema version %d after migration, got %d", CurrentSchemaVersion, cfg.SchemaVersion)
	}

	// Model should be migrated
	if cfg.Agents.Defaults.Model == "google/gemma-2-9b-it:free" {
		t.Error("model should have been migrated")
	}
}

func TestProviderConfig(t *testing.T) {
	cfg := Defaults()

	// Test setting provider config
	cfg.Providers = map[string]ProviderConfig{
		"openrouter": {
			APIKey:  "test-key",
			APIBase: "https://api.openrouter.ai/v1",
			ExtraHeaders: map[string]string{
				"X-Custom": "value",
			},
		},
	}

	if cfg.Providers["openrouter"].APIKey != "test-key" {
		t.Errorf("expected API key test-key, got %s", cfg.Providers["openrouter"].APIKey)
	}

	if cfg.Providers["openrouter"].APIBase != "https://api.openrouter.ai/v1" {
		t.Errorf("expected API base https://api.openrouter.ai/v1, got %s", cfg.Providers["openrouter"].APIBase)
	}

	if cfg.Providers["openrouter"].ExtraHeaders["X-Custom"] != "value" {
		t.Errorf("expected header X-Custom: value, got %s", cfg.Providers["openrouter"].ExtraHeaders["X-Custom"])
	}
}

func TestTelegramConfig(t *testing.T) {
	cfg := Defaults()

	cfg.Channels.Telegram = TelegramConfig{
		Enabled:   true,
		Token:     "test-token",
		AllowFrom: []string{"user1", "user2"},
		Proxy:     "socks5://proxy:1080",
	}

	if !cfg.Channels.Telegram.Enabled {
		t.Error("expected telegram to be enabled")
	}

	if cfg.Channels.Telegram.Token != "test-token" {
		t.Errorf("expected token test-token, got %s", cfg.Channels.Telegram.Token)
	}

	if len(cfg.Channels.Telegram.AllowFrom) != 2 {
		t.Errorf("expected 2 allow from entries, got %d", len(cfg.Channels.Telegram.AllowFrom))
	}

	if cfg.Channels.Telegram.Proxy != "socks5://proxy:1080" {
		t.Errorf("expected proxy socks5://proxy:1080, got %s", cfg.Channels.Telegram.Proxy)
	}
}

func TestToolsConfig(t *testing.T) {
	cfg := Defaults()

	cfg.Tools = ToolsConfig{
		Web: WebToolsConfig{
			Search: WebSearchConfig{
				APIKey: "search-key",
			},
		},
		Exec: ExecConfig{
			Timeout: 120,
		},
		RestrictToWorkspace: true,
	}

	if cfg.Tools.Web.Search.APIKey != "search-key" {
		t.Errorf("expected search API key search-key, got %s", cfg.Tools.Web.Search.APIKey)
	}

	if cfg.Tools.Exec.Timeout != 120 {
		t.Errorf("expected exec timeout 120, got %d", cfg.Tools.Exec.Timeout)
	}

	if !cfg.Tools.RestrictToWorkspace {
		t.Error("expected restrict_to_workspace to be true")
	}
}
