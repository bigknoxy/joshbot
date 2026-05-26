package configure

import (
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Providers: make(map[string]config.ProviderConfig),
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Model: "arcee-ai/trinity-large-preview:free",
			},
		},
	}
}

func TestConfigureProvider_FirstProvider_SetsDefaultAndModel(t *testing.T) {
	cfg := newTestConfig(t)
	c := New(cfg)

	err := c.ConfigureProvider(ProviderOptions{
		Name:    "nvidia",
		APIKey:  "nvapi-test-key",
		APIBase: "https://integrate.api.nvidia.com/v1",
		Model:   "stepfun-ai/step-3.5-flash",
	})
	if err != nil {
		t.Fatalf("ConfigureProvider failed: %v", err)
	}

	if c.cfg.ProviderDefaults.Default != "nvidia" {
		t.Errorf("expected default provider 'nvidia', got %q", c.cfg.ProviderDefaults.Default)
	}
	if c.cfg.Agents.Defaults.Model != "stepfun-ai/step-3.5-flash" {
		t.Errorf("expected model 'stepfun-ai/step-3.5-flash', got %q", c.cfg.Agents.Defaults.Model)
	}
	p := c.cfg.Providers["nvidia"]
	if p.APIKey != "nvapi-test-key" {
		t.Errorf("expected API key 'nvapi-test-key', got %q", p.APIKey)
	}
	if !p.Enabled {
		t.Error("expected provider to be enabled")
	}
	if p.Model != "stepfun-ai/step-3.5-flash" {
		t.Errorf("expected per-provider model 'stepfun-ai/step-3.5-flash', got %q", p.Model)
	}
}

func TestConfigureProvider_FirstProviderNoModel_FallsBackToRegistryDefault(t *testing.T) {
	cfg := newTestConfig(t)
	c := New(cfg)

	err := c.ConfigureProvider(ProviderOptions{
		Name:   "groq",
		APIKey: "gsk-test-key",
	})
	if err != nil {
		t.Fatalf("ConfigureProvider failed: %v", err)
	}

	if c.cfg.Agents.Defaults.Model != providers.GetDefaultModel("groq") {
		t.Errorf("expected model %q, got %q", providers.GetDefaultModel("groq"), c.cfg.Agents.Defaults.Model)
	}
}

func TestConfigureProvider_SecondProvider_DoesNotOverrideDefault(t *testing.T) {
	cfg := newTestConfig(t)
	c := New(cfg)

	err := c.ConfigureProvider(ProviderOptions{
		Name:   "nvidia",
		APIKey: "nvapi-1",
		Model:  "model-a",
	})
	if err != nil {
		t.Fatalf("first ConfigureProvider failed: %v", err)
	}

	err = c.ConfigureProvider(ProviderOptions{
		Name:   "groq",
		APIKey: "gsk-2",
		Model:  "model-b",
	})
	if err != nil {
		t.Fatalf("second ConfigureProvider failed: %v", err)
	}

	if c.cfg.ProviderDefaults.Default != "nvidia" {
		t.Errorf("expected default to remain 'nvidia', got %q", c.cfg.ProviderDefaults.Default)
	}
	if c.cfg.Agents.Defaults.Model != "model-a" {
		t.Errorf("expected model to remain 'model-a', got %q", c.cfg.Agents.Defaults.Model)
	}
}

func TestConfigureProvider_ReconfigureDefault_UpdatesModel(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Providers["nvidia"] = config.ProviderConfig{
		APIKey:  "nvapi-old",
		APIBase: "https://integrate.api.nvidia.com/v1",
		Model:   "stepfun-ai/step-3.5-flash",
		Enabled: true,
	}
	cfg.Providers["openrouter"] = config.ProviderConfig{
		APIKey:  "sk-or-old",
		Enabled: true,
	}
	cfg.ProviderDefaults.Default = "nvidia"
	cfg.Agents.Defaults.Model = "stepfun-ai/step-3.5-flash"
	c := New(cfg)

	err := c.ConfigureProvider(ProviderOptions{
		Name:   "nvidia",
		APIKey: "nvapi-new",
		Model:  "meta/llama-4-405b",
	})
	if err != nil {
		t.Fatalf("ConfigureProvider on default provider failed: %v", err)
	}

	if c.cfg.Agents.Defaults.Model != "meta/llama-4-405b" {
		t.Errorf("expected agents.defaults.model updated to 'meta/llama-4-405b', got %q", c.cfg.Agents.Defaults.Model)
	}
	if c.cfg.ProviderDefaults.Default != "nvidia" {
		t.Errorf("expected default to remain 'nvidia', got %q", c.cfg.ProviderDefaults.Default)
	}
	p := c.cfg.Providers["nvidia"]
	if p.Model != "meta/llama-4-405b" {
		t.Errorf("expected per-provider model 'meta/llama-4-405b', got %q", p.Model)
	}
}

func TestConfigureProvider_ReconfigureNonDefault_DoesNotChangeModel(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Providers["nvidia"] = config.ProviderConfig{
		APIKey:  "nvapi-key",
		APIBase: "https://integrate.api.nvidia.com/v1",
		Model:   "stepfun-ai/step-3.5-flash",
		Enabled: true,
	}
	cfg.ProviderDefaults.Default = "nvidia"
	cfg.Agents.Defaults.Model = "stepfun-ai/step-3.5-flash"
	c := New(cfg)

	err := c.ConfigureProvider(ProviderOptions{
		Name:   "openrouter",
		APIKey: "sk-or-new",
		Model:  "some-other-model",
	})
	if err != nil {
		t.Fatalf("ConfigureProvider on non-default provider failed: %v", err)
	}

	if c.cfg.Agents.Defaults.Model != "stepfun-ai/step-3.5-flash" {
		t.Errorf("expected agents.defaults.model unchanged, got %q", c.cfg.Agents.Defaults.Model)
	}
	if c.cfg.ProviderDefaults.Default != "nvidia" {
		t.Errorf("expected default to remain 'nvidia', got %q", c.cfg.ProviderDefaults.Default)
	}
}

func TestConfigureProvider_ExistingProvider_UpdatesFields(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Providers["openrouter"] = config.ProviderConfig{
		APIKey:  "sk-or-old",
		APIBase: "https://openrouter.ai/api/v1",
		Model:   "old-model",
		Enabled: true,
	}
	cfg.ProviderDefaults.Default = "openrouter"
	cfg.Agents.Defaults.Model = "old-model"
	c := New(cfg)

	err := c.ConfigureProvider(ProviderOptions{
		Name:   "openrouter",
		APIKey: "sk-or-new",
		Model:  "new-model",
	})
	if err != nil {
		t.Fatalf("ConfigureProvider failed: %v", err)
	}

	p := c.cfg.Providers["openrouter"]
	if p.APIKey != "sk-or-new" {
		t.Errorf("expected updated API key 'sk-or-new', got %q", p.APIKey)
	}
	if p.Model != "new-model" {
		t.Errorf("expected updated model 'new-model', got %q", p.Model)
	}
}

func TestSetDefault_UsesPerProviderModel(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Providers["nvidia"] = config.ProviderConfig{
		APIKey:  "nvapi-key",
		Model:   "stepfun-ai/step-3.5-flash",
		Enabled: true,
	}
	cfg.Providers["groq"] = config.ProviderConfig{
		APIKey:  "gsk-key",
		Model:   "qwen/qwen3-32b",
		Enabled: true,
	}
	cfg.ProviderDefaults.Default = "groq"
	cfg.Agents.Defaults.Model = "qwen/qwen3-32b"
	c := New(cfg)

	err := c.SetDefault("nvidia")
	if err != nil {
		t.Fatalf("SetDefault failed: %v", err)
	}

	if c.cfg.ProviderDefaults.Default != "nvidia" {
		t.Errorf("expected default 'nvidia', got %q", c.cfg.ProviderDefaults.Default)
	}
	if c.cfg.Agents.Defaults.Model != "stepfun-ai/step-3.5-flash" {
		t.Errorf("expected model 'stepfun-ai/step-3.5-flash', got %q", c.cfg.Agents.Defaults.Model)
	}
}

func TestSetDefault_NoPerProviderModel_FallsBack(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Providers["groq"] = config.ProviderConfig{
		APIKey:  "gsk-key",
		Enabled: true,
	}
	c := New(cfg)

	c.SetDefault("groq")
	expectedModel := providers.GetDefaultModel("groq")
	if c.cfg.Agents.Defaults.Model != expectedModel {
		t.Errorf("expected fallback model %q, got %q", expectedModel, c.cfg.Agents.Defaults.Model)
	}
}

func TestSetDefault_NotConfigured_ReturnsError(t *testing.T) {
	cfg := newTestConfig(t)
	c := New(cfg)
	err := c.SetDefault("nvidia")
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestRemoveProvider_ClearsDefaultAndFallsBack(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Providers["nvidia"] = config.ProviderConfig{APIKey: "key1", Enabled: true}
	cfg.Providers["groq"] = config.ProviderConfig{APIKey: "key2", Enabled: true}
	cfg.ProviderDefaults.Default = "nvidia"
	cfg.Agents.Defaults.Model = "model-nvidia"
	c := New(cfg)

	err := c.RemoveProvider("nvidia")
	if err != nil {
		t.Fatalf("RemoveProvider failed: %v", err)
	}

	if _, exists := c.cfg.Providers["nvidia"]; exists {
		t.Error("expected nvidia to be removed from providers")
	}
	if c.cfg.ProviderDefaults.Default != "groq" {
		t.Errorf("expected default to fall back to 'groq', got %q", c.cfg.ProviderDefaults.Default)
	}
}

func TestListProviders_ShowsCorrectStatus(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Providers["nvidia"] = config.ProviderConfig{
		APIKey:  "nvapi-key",
		Enabled: true,
		Model:   "stepfun-ai/step-3.5-flash",
	}
	cfg.ProviderDefaults.Default = "nvidia"
	c := New(cfg)

	items := c.ListProviders()

	var nvidiaItem *ProviderListItem
	for i := range items {
		if items[i].Name == "nvidia" {
			nvidiaItem = &items[i]
			break
		}
	}
	if nvidiaItem == nil {
		t.Fatal("expected nvidia item in list")
	}
	if !nvidiaItem.Configured {
		t.Error("expected nvidia to be shown as configured")
	}
	if !nvidiaItem.IsDefault {
		t.Error("expected nvidia to be shown as default")
	}
	if nvidiaItem.Model != "stepfun-ai/step-3.5-flash" {
		t.Errorf("expected model 'stepfun-ai/step-3.5-flash', got %q", nvidiaItem.Model)
	}
}

func TestConfigureProvider_CLIFlags_EquivalentToInteractive(t *testing.T) {
	cfgCLI := newTestConfig(t)
	cCLI := New(cfgCLI)

	err := cCLI.ConfigureProvider(ProviderOptions{
		Name:    "nvidia",
		APIKey:  "nvapi-cli-test",
		APIBase: "https://integrate.api.nvidia.com/v1",
		Model:   "stepfun-ai/step-3.5-flash",
	})
	if err != nil {
		t.Fatalf("CLI-style ConfigureProvider failed: %v", err)
	}

	cfgInteractive := newTestConfig(t)
	cInteractive := New(cfgInteractive)

	err = cInteractive.ConfigureProvider(ProviderOptions{
		Name:    "nvidia",
		APIKey:  "nvapi-cli-test",
		APIBase: "https://integrate.api.nvidia.com/v1",
		Model:   "stepfun-ai/step-3.5-flash",
	})
	if err != nil {
		t.Fatalf("interactive-style ConfigureProvider failed: %v", err)
	}

	if cfgCLI.ProviderDefaults.Default != cfgInteractive.ProviderDefaults.Default {
		t.Error("CLI and interactive paths produced different defaults")
	}
	if cfgCLI.Agents.Defaults.Model != cfgInteractive.Agents.Defaults.Model {
		t.Errorf("CLI model=%q, interactive model=%q", cfgCLI.Agents.Defaults.Model, cfgInteractive.Agents.Defaults.Model)
	}
	if cfgCLI.Providers["nvidia"].APIKey != cfgInteractive.Providers["nvidia"].APIKey {
		t.Error("CLI and interactive paths produced different API keys")
	}
}

func TestGetProviderDisplayName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"nvidia", "NVIDIA NIM"},
		{"openrouter", "OpenRouter"},
		{"groq", "Groq"},
		{"ollama", "Ollama"},
		{"github-copilot", "GitHub Copilot"},
	}
	for _, tc := range cases {
		got := GetProviderDisplayName(tc.name)
		if got != tc.want {
			t.Errorf("getProviderDisplayName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMaskAPIKey(t *testing.T) {
	cases := []struct {
		input string
	}{
		{"sk-or-v1-test1234567890abcdef"},
		{"nvapi-test1234567890abcdef"},
		{"abc"},
	}
	for _, tc := range cases {
		masked := MaskAPIKey(tc.input)
		if masked == "" {
			t.Error("masked key should not be empty")
		}
		if len(masked) != len(tc.input) {
			t.Errorf("masked key length %d != input length %d", len(masked), len(tc.input))
		}
	}
}
