package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// configureTestHome points config.Save at a scratch home for the duration of
// one test. The home is process-global state, so nothing here may run in
// parallel.
func configureTestHome(t *testing.T) string {
	t.Helper()
	prevHome := config.DefaultHome
	prevWorkspace := config.DefaultWorkspace
	dir := t.TempDir()
	config.SetHome(dir)
	t.Cleanup(func() {
		config.DefaultHome = prevHome
		config.DefaultWorkspace = prevWorkspace
	})
	return dir
}

func modelCentricConfig() *config.Config {
	cfg := config.Defaults()
	cfg.ModelsConfig.Models = []config.ModelConfig{
		{Name: "zeta", Model: "vendor/zeta", APIKey: "sk-abcdefghijkl", APIBase: "https://z.example", MaxTokens: 8192},
		{Name: "alpha", Model: "vendor/alpha", APIKey: "short", Disabled: true},
	}
	cfg.ModelsConfig.Agent.Model = "alpha"
	cfg.ModelsConfig.Agent.Fallback = []string{"zeta"}
	return cfg
}

func TestConfigureTool_Metadata(t *testing.T) {
	tool := NewConfigureTool(config.Defaults(), nil)
	if tool.Name() != "joshbot_config" {
		t.Errorf("Name() = %q, want joshbot_config", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	params := tool.Parameters()
	// The enum is what a function-calling model is shown; a value present in
	// the enum but missing from Execute's switch is a dead operation, and vice
	// versa. Pin the two against each other.
	var op *Parameter
	for i := range params {
		if params[i].Name == "operation" {
			op = &params[i]
		}
	}
	if op == nil {
		t.Fatal("Parameters() has no 'operation' parameter")
	}
	if !op.Required {
		t.Error("'operation' must be Required")
	}
	tool2 := NewConfigureTool(modelCentricConfig(), nil)
	for _, name := range op.Enum {
		res := tool2.Execute(nil, map[string]any{"operation": name})
		if res.Error != nil && strings.Contains(res.Error.Error(), "unknown operation") {
			t.Errorf("operation %q is advertised in the enum but Execute rejects it as unknown", name)
		}
	}
}

func TestConfigureTool_ExecuteDispatchErrors(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		args    map[string]any
		wantErr string
	}{
		{"nil config", nil, map[string]any{"operation": "status"}, "config not available"},
		{"missing operation", config.Defaults(), map[string]any{}, "'operation' is required"},
		{"empty operation", config.Defaults(), map[string]any{"operation": ""}, "'operation' is required"},
		{"unknown operation", config.Defaults(), map[string]any{"operation": "delete_everything"}, "unknown operation"},
		{"get without setting", config.Defaults(), map[string]any{"operation": "get"}, "'setting' is required"},
		{"get unknown setting", config.Defaults(), map[string]any{"operation": "get", "setting": "nope"}, "unknown setting"},
		{"set without setting", config.Defaults(), map[string]any{"operation": "set"}, "'setting' is required"},
		{"set without value", config.Defaults(), map[string]any{"operation": "set", "setting": "temperature"}, "'value' is required"},
		{"set unknown setting", config.Defaults(), map[string]any{"operation": "set", "setting": "nope", "value": "1"}, "unknown setting"},
		{"switch_model on legacy config", config.Defaults(), map[string]any{"operation": "switch_model", "model": "x"}, "model-centric config"},
		{"switch_model without model", modelCentricConfig(), map[string]any{"operation": "switch_model"}, "'model' is required"},
		{"switch_model unknown model", modelCentricConfig(), map[string]any{"operation": "switch_model", "model": "ghost"}, "not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewConfigureTool(tt.cfg, nil)
			res := tool.Execute(nil, tt.args)
			if res.Error == nil {
				t.Fatalf("expected an error, got output %q", res.Output)
			}
			if !strings.Contains(res.Error.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", res.Error, tt.wantErr)
			}
		})
	}
}

func TestConfigureTool_ListModels(t *testing.T) {
	t.Run("legacy config says so", func(t *testing.T) {
		res := NewConfigureTool(config.Defaults(), nil).Execute(nil, map[string]any{"operation": "list_models"})
		if res.Error != nil {
			t.Fatalf("unexpected error: %v", res.Error)
		}
		if !strings.Contains(res.Output, "legacy provider config") {
			t.Errorf("output = %q, want the legacy-config notice", res.Output)
		}
	})

	t.Run("sorted, marks active and disabled", func(t *testing.T) {
		res := NewConfigureTool(modelCentricConfig(), nil).Execute(nil, map[string]any{"operation": "list_models"})
		if res.Error != nil {
			t.Fatalf("unexpected error: %v", res.Error)
		}
		out := res.Output
		if !strings.Contains(out, "(2)") {
			t.Errorf("output should state the model count, got %q", out)
		}
		iAlpha, iZeta := strings.Index(out, "alpha"), strings.Index(out, "zeta")
		if iAlpha == -1 || iZeta == -1 || iAlpha > iZeta {
			t.Errorf("models must be listed sorted by name, got %q", out)
		}
		// The active marker must land on the active model and nowhere else,
		// otherwise the agent switches away from the model it is already on.
		alphaLine := lineContaining(out, "**alpha**")
		zetaLine := lineContaining(out, "**zeta**")
		if !strings.Contains(alphaLine, "active") {
			t.Errorf("active model line = %q, want the active marker", alphaLine)
		}
		if strings.Contains(zetaLine, "active") {
			t.Errorf("inactive model line = %q, must not claim to be active", zetaLine)
		}
		if !strings.Contains(alphaLine, "[disabled]") {
			t.Errorf("disabled model line = %q, want the disabled marker", alphaLine)
		}
		if strings.Contains(zetaLine, "[disabled]") {
			t.Errorf("enabled model line = %q, must not be marked disabled", zetaLine)
		}
	})

	t.Run("no models configured", func(t *testing.T) {
		cfg := modelCentricConfig()
		cfg.ModelsConfig.Models = nil
		res := NewConfigureTool(cfg, nil).Execute(nil, map[string]any{"operation": "list_models"})
		if !strings.Contains(res.Output, "legacy") && !strings.Contains(res.Output, "No models") {
			t.Errorf("output = %q, want an explanation rather than an empty list", res.Output)
		}
	})
}

func lineContaining(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func TestConfigureTool_StatusMasksAPIKey(t *testing.T) {
	cfg := modelCentricConfig()
	cfg.ModelsConfig.Agent.Model = "zeta"
	res := NewConfigureTool(cfg, nil).Execute(nil, map[string]any{"operation": "status"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	// The whole key must never appear: status output is shown to the model and
	// goes into the transcript.
	if strings.Contains(res.Output, "sk-abcdefghijkl") {
		t.Fatalf("status leaked the full API key: %q", res.Output)
	}
	if !strings.Contains(res.Output, "sk-a") || !strings.Contains(res.Output, "ijkl") {
		t.Errorf("status should show a masked key prefix/suffix, got %q", res.Output)
	}
	for _, want := range []string{"model-centric", "zeta", "vendor/zeta", "Fallback"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("status output missing %q: %q", want, res.Output)
		}
	}
}

func TestConfigureTool_StatusShortKeyIsNotSliced(t *testing.T) {
	cfg := modelCentricConfig()
	cfg.ModelsConfig.Agent.Model = "alpha" // APIKey "short", 5 chars
	// A naive mask would slice [:4] and [len-4:] and overlap or panic.
	res := NewConfigureTool(cfg, nil).Execute(nil, map[string]any{"operation": "status"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if strings.Contains(res.Output, "short") {
		t.Errorf("a short key must not be echoed, got %q", res.Output)
	}
	if !strings.Contains(res.Output, "**API key:** configured") {
		t.Errorf("expected the short-key placeholder, got %q", res.Output)
	}
}

func TestConfigureTool_StatusLegacyFormat(t *testing.T) {
	cfg := config.Defaults()
	cfg.ProviderDefaults.Default = "openrouter"
	cfg.Agents.Defaults.Model = "legacy/model"
	res := NewConfigureTool(cfg, nil).Execute(nil, map[string]any{"operation": "status"})
	if !strings.Contains(res.Output, "legacy provider-based") {
		t.Errorf("output = %q, want the legacy format label", res.Output)
	}
	if !strings.Contains(res.Output, "openrouter") || !strings.Contains(res.Output, "legacy/model") {
		t.Errorf("output = %q, want the legacy provider and model", res.Output)
	}
}

func TestConfigureTool_Get(t *testing.T) {
	cfg := modelCentricConfig()
	cfg.Agents.Defaults.Temperature = 0.7
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.Workspace = "/ws"
	tool := NewConfigureTool(cfg, nil)

	tests := []struct{ setting, want string }{
		{"model", "alpha"},
		{"temperature", "0.7"},
		{"max_tokens", "4096"},
		{"workspace", "/ws"},
	}
	for _, tt := range tests {
		t.Run(tt.setting, func(t *testing.T) {
			res := tool.Execute(nil, map[string]any{"operation": "get", "setting": tt.setting})
			if res.Error != nil {
				t.Fatalf("unexpected error: %v", res.Error)
			}
			if !strings.Contains(res.Output, tt.want) {
				t.Errorf("get %s = %q, want it to contain %q", tt.setting, res.Output, tt.want)
			}
		})
	}

	t.Run("model on legacy config reports the default model", func(t *testing.T) {
		legacy := config.Defaults()
		legacy.Agents.Defaults.Model = "legacy/model"
		res := NewConfigureTool(legacy, nil).Execute(nil, map[string]any{"operation": "get", "setting": "model"})
		if !strings.Contains(res.Output, "legacy/model") {
			t.Errorf("output = %q, want the legacy default model", res.Output)
		}
	})
}

func TestConfigureTool_SetPersistsAndMutates(t *testing.T) {
	configureTestHome(t)
	cfg := config.Defaults()
	cfg.Agents.Defaults.Temperature = 0.2
	cfg.Agents.Defaults.MaxTokens = 100
	tool := NewConfigureTool(cfg, nil)

	res := tool.Execute(nil, map[string]any{"operation": "set", "setting": "temperature", "value": "0.9"})
	if res.Error != nil {
		t.Fatalf("set temperature: %v", res.Error)
	}
	if cfg.Agents.Defaults.Temperature != 0.9 {
		t.Errorf("in-memory temperature = %v, want 0.9", cfg.Agents.Defaults.Temperature)
	}
	res = tool.Execute(nil, map[string]any{"operation": "set", "setting": "max_tokens", "value": "8192"})
	if res.Error != nil {
		t.Fatalf("set max_tokens: %v", res.Error)
	}
	if cfg.Agents.Defaults.MaxTokens != 8192 {
		t.Errorf("in-memory max_tokens = %d, want 8192", cfg.Agents.Defaults.MaxTokens)
	}

	// "Saved to config" must mean the file on disk actually changed — the
	// message is the only feedback the operator gets.
	onDisk, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatalf("config file was not written: %v", err)
	}
	if !strings.Contains(string(onDisk), "8192") {
		t.Errorf("saved config does not contain the new max_tokens: %s", onDisk)
	}
}

func TestConfigureTool_SetRejectsMalformedValues(t *testing.T) {
	configureTestHome(t)
	cfg := config.Defaults()
	cfg.Agents.Defaults.Temperature = 0.2
	cfg.Agents.Defaults.MaxTokens = 100
	tool := NewConfigureTool(cfg, nil)

	for _, tt := range []struct{ setting, value string }{
		{"temperature", "hot"},
		{"max_tokens", "lots"},
	} {
		res := tool.Execute(nil, map[string]any{"operation": "set", "setting": tt.setting, "value": tt.value})
		if res.Error == nil {
			t.Errorf("set %s=%q should have failed, got %q", tt.setting, tt.value, res.Output)
		}
	}
	if cfg.Agents.Defaults.Temperature != 0.2 || cfg.Agents.Defaults.MaxTokens != 100 {
		t.Errorf("a rejected value must not mutate the config, got temp=%v max=%d",
			cfg.Agents.Defaults.Temperature, cfg.Agents.Defaults.MaxTokens)
	}
}

// A failed save must roll the in-memory value back, or the running process
// silently diverges from what a restart would load.
func TestConfigureTool_SetRollsBackWhenSaveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	prevHome := config.DefaultHome
	prevWorkspace := config.DefaultWorkspace
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	config.SetHome(filepath.Join(parent, "unwritable"))
	t.Cleanup(func() {
		_ = os.Chmod(parent, 0o700)
		config.DefaultHome = prevHome
		config.DefaultWorkspace = prevWorkspace
	})

	cfg := modelCentricConfig()
	cfg.Agents.Defaults.Temperature = 0.2
	cfg.Agents.Defaults.MaxTokens = 100
	tool := NewConfigureTool(cfg, nil)

	cases := []struct {
		name   string
		args   map[string]any
		verify func(t *testing.T)
	}{
		{
			name: "temperature",
			args: map[string]any{"operation": "set", "setting": "temperature", "value": "0.9"},
			verify: func(t *testing.T) {
				if cfg.Agents.Defaults.Temperature != 0.2 {
					t.Errorf("temperature = %v after failed save, want the previous 0.2", cfg.Agents.Defaults.Temperature)
				}
			},
		},
		{
			name: "max_tokens",
			args: map[string]any{"operation": "set", "setting": "max_tokens", "value": "8192"},
			verify: func(t *testing.T) {
				if cfg.Agents.Defaults.MaxTokens != 100 {
					t.Errorf("max_tokens = %d after failed save, want the previous 100", cfg.Agents.Defaults.MaxTokens)
				}
			},
		},
		{
			name: "switch_model",
			args: map[string]any{"operation": "switch_model", "model": "zeta"},
			verify: func(t *testing.T) {
				if cfg.ModelsConfig.Agent.Model != "alpha" {
					t.Errorf("active model = %q after failed save, want the previous alpha", cfg.ModelsConfig.Agent.Model)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tool.Execute(nil, tc.args)
			if res.Error == nil {
				t.Fatalf("expected a save failure, got output %q", res.Output)
			}
			if !strings.Contains(res.Error.Error(), "failed to save config") {
				t.Errorf("error = %q, want it to name the save failure", res.Error)
			}
			tc.verify(t)
		})
	}
}

func TestConfigureTool_SwitchModelReloadsProviders(t *testing.T) {
	configureTestHome(t)

	t.Run("reload is called and the switch is reported active", func(t *testing.T) {
		cfg := modelCentricConfig()
		called := 0
		tool := NewConfigureTool(cfg, func() error { called++; return nil })
		res := tool.Execute(nil, map[string]any{"operation": "switch_model", "model": "zeta"})
		if res.Error != nil {
			t.Fatalf("unexpected error: %v", res.Error)
		}
		if called != 1 {
			t.Errorf("reloadProviders called %d times, want 1 — without it the switch does not take effect", called)
		}
		if cfg.ModelsConfig.Agent.Model != "zeta" {
			t.Errorf("active model = %q, want zeta", cfg.ModelsConfig.Agent.Model)
		}
		if !strings.Contains(res.Output, "active now") {
			t.Errorf("output = %q, want it to say the change is live", res.Output)
		}
	})

	t.Run("a failed reload keeps the saved config and warns about restart", func(t *testing.T) {
		cfg := modelCentricConfig()
		tool := NewConfigureTool(cfg, func() error { return os.ErrPermission })
		res := tool.Execute(nil, map[string]any{"operation": "switch_model", "model": "zeta"})
		if res.Error != nil {
			t.Fatalf("a reload failure must not be reported as a config error: %v", res.Error)
		}
		if cfg.ModelsConfig.Agent.Model != "zeta" {
			t.Errorf("the config was saved, so the in-memory model must stay switched, got %q", cfg.ModelsConfig.Agent.Model)
		}
		if !strings.Contains(res.Output, "restart") {
			t.Errorf("output = %q, want it to tell the operator a restart is needed", res.Output)
		}
	})
}

// "set" with setting=model routes to switch_model, so the model-validation and
// persistence rules apply to it too rather than blindly assigning a name.
func TestConfigureTool_SetModelGoesThroughSwitchModel(t *testing.T) {
	configureTestHome(t)
	cfg := modelCentricConfig()
	tool := NewConfigureTool(cfg, nil)

	res := tool.Execute(nil, map[string]any{"operation": "set", "setting": "model", "value": "ghost", "model": "ghost"})
	if res.Error == nil {
		t.Fatalf("set model=ghost should be refused: no such model")
	}
	if cfg.ModelsConfig.Agent.Model != "alpha" {
		t.Errorf("active model = %q, want it unchanged", cfg.ModelsConfig.Agent.Model)
	}

	res = tool.Execute(nil, map[string]any{"operation": "set", "setting": "model", "value": "zeta", "model": "zeta"})
	if res.Error != nil {
		t.Fatalf("set model=zeta: %v", res.Error)
	}
	if cfg.ModelsConfig.Agent.Model != "zeta" {
		t.Errorf("active model = %q, want zeta", cfg.ModelsConfig.Agent.Model)
	}
}
