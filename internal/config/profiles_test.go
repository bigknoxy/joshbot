package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProfileConfig writes a config file into a temp home and points
// DefaultHome at it, returning the config path.
func writeProfileConfig(t *testing.T, body map[string]any) string {
	t.Helper()
	home := t.TempDir()
	SetHome(home)
	t.Cleanup(func() { SetHome(filepath.Join(os.Getenv("HOME"), ".joshbot")) })
	path := filepath.Join(home, "config.json")
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// baseProfileConfig is a minimal valid config carrying two profiles, used by
// the precedence table so every case differs only in the selectors.
func baseProfileConfig() map[string]any {
	return map[string]any{
		"schema_version": CurrentSchemaVersion,
		"agents":         map[string]any{"defaults": map[string]any{"model": "openrouter/legacy-model"}},
		"providers": map[string]any{
			"openrouter": map[string]any{"api_key": "legacy-key", "enabled": true},
		},
		"profiles": map[string]any{
			"local": map[string]any{
				"provider":    "ollama",
				"model":       "qwen3:8b",
				"api_base":    "http://localhost:11434/v1",
				"description": "local dev",
			},
			"cloud": map[string]any{
				"provider":    "openrouter",
				"model":       "z-ai/glm-4.6",
				"api_key_env": "TEST_PROFILE_KEY",
			},
		},
	}
}

// TestProfilePrecedence is the compatibility contract. The flag beats
// default_profile, default_profile beats nothing, and — the case that matters
// most — a config that has profiles but selects none must behave exactly as it
// did before profiles existed, or every existing install changes behaviour on
// upgrade the moment someone adds a profile block.
func TestProfilePrecedence(t *testing.T) {
	tests := []struct {
		name           string
		defaultProfile string
		flag           string
		wantModel      string
		wantActive     string
	}{
		{
			name:      "neither selector: legacy config untouched",
			wantModel: "openrouter/legacy-model",
		},
		{
			name:           "default_profile only",
			defaultProfile: "local",
			wantModel:      "ollama/qwen3:8b",
			wantActive:     "local",
		},
		{
			name:       "flag only",
			flag:       "local",
			wantModel:  "ollama/qwen3:8b",
			wantActive: "local",
		},
		{
			name:           "flag overrides default_profile",
			defaultProfile: "local",
			flag:           "cloud",
			wantModel:      "openrouter/z-ai/glm-4.6",
			wantActive:     "cloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_PROFILE_KEY", "sk-profile-secret-value")
			body := baseProfileConfig()
			if tt.defaultProfile != "" {
				body["default_profile"] = tt.defaultProfile
			}
			writeProfileConfig(t, body)

			cfg, err := LoadStrict()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if err := cfg.ApplyProfile(cfg.SelectProfile(tt.flag)); err != nil {
				t.Fatalf("apply profile: %v", err)
			}
			if got := cfg.Agents.Defaults.Model; got != tt.wantModel {
				t.Fatalf("model = %q, want %q", got, tt.wantModel)
			}
			if got := cfg.ActiveProfile(); got != tt.wantActive {
				t.Fatalf("active profile = %q, want %q", got, tt.wantActive)
			}
			if tt.wantActive == "" {
				// The legacy path must be completely untouched: an upgrade
				// that quietly switched an install to the models-config path
				// would change provider routing with no config change.
				if cfg.UseModelsConfig() {
					t.Fatal("no profile selected must leave the models block alone")
				}
				if cfg.Providers["openrouter"].APIKey != "legacy-key" {
					t.Fatal("no profile selected must leave legacy providers alone")
				}
			}
		})
	}
}

// TestProfileWithRawAPIKeyIsRejectedAtLoad is the design's whole point. A
// profile is the block most likely to be pasted into an issue or committed to
// dotfiles, so a credential written here has to be refused where the operator
// can still act on it — at load — not accepted and used.
func TestProfileWithRawAPIKeyIsRejectedAtLoad(t *testing.T) {
	body := baseProfileConfig()
	body["profiles"].(map[string]any)["cloud"].(map[string]any)["api_key"] = "sk-raw-inline-secret"
	writeProfileConfig(t, body)

	_, err := LoadStrict()
	if err == nil {
		t.Fatal("a profile carrying a raw api_key must be a load error")
	}
	if !strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("the error must direct the operator to api_key_env, got %v", err)
	}
	if strings.Contains(err.Error(), "sk-raw-inline-secret") {
		t.Fatalf("the error must not echo the credential it is rejecting: %v", err)
	}
}

// TestUnknownProfileFailsBeforeAnyRequest pins that the failure is a startup
// error naming the alternatives. Discovering a typo as a provider 404 sends
// the operator to the wrong system entirely.
func TestUnknownProfileFailsBeforeAnyRequest(t *testing.T) {
	writeProfileConfig(t, baseProfileConfig())
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	err = cfg.ApplyProfile("clod")
	if err == nil {
		t.Fatal("an unknown profile must fail")
	}
	for _, want := range []string{"clod", "cloud", "local"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must name the profile and list the configured ones, missing %q in %v", want, err)
		}
	}
	if cfg.Agents.Defaults.Model != "openrouter/legacy-model" {
		t.Fatal("a failed selection must leave the config unmodified")
	}
}

// TestDisabledProfileFailsDistinctly separates "you typed it wrong" from "you
// turned it off" — the same message for both sends the operator hunting for a
// typo that is not there.
func TestDisabledProfileFailsDistinctly(t *testing.T) {
	body := baseProfileConfig()
	body["profiles"].(map[string]any)["local"].(map[string]any)["disabled"] = true
	writeProfileConfig(t, body)
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	err = cfg.ApplyProfile("local")
	if err == nil {
		t.Fatal("a disabled profile must not be selectable")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("a disabled profile needs its own message, got %v", err)
	}
	unknown := cfg.ApplyProfile("nope")
	if unknown != nil && unknown.Error() == err.Error() {
		t.Fatal("disabled and unknown must not report the same thing")
	}
}

// TestProfileWithMissingCredentialFailsAtStartup keeps an unset env var from
// becoming a 401 twenty seconds into a conversation, which reads as a revoked
// key rather than a missing variable.
func TestProfileWithMissingCredentialFailsAtStartup(t *testing.T) {
	t.Setenv("TEST_PROFILE_KEY", "")
	writeProfileConfig(t, baseProfileConfig())
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	err = cfg.ApplyProfile("cloud")
	if err == nil {
		t.Fatal("a profile whose api_key_env is unset must fail at startup")
	}
	if !strings.Contains(err.Error(), "TEST_PROFILE_KEY") {
		t.Fatalf("the error must name the variable to set, got %v", err)
	}
}

// TestDefaultProfileNamingNothingIsALoadError catches the config-file typo at
// the only moment the operator is looking at the config file.
func TestDefaultProfileNamingNothingIsALoadError(t *testing.T) {
	body := baseProfileConfig()
	body["default_profile"] = "ghost"
	writeProfileConfig(t, body)

	if _, err := LoadStrict(); err == nil {
		t.Fatal("default_profile naming an unconfigured profile must be a load error")
	}
}

// TestConfigWithoutProfilesIsUnchanged covers both config formats: adding the
// feature must be invisible to every install that does not use it.
func TestConfigWithoutProfilesIsUnchanged(t *testing.T) {
	t.Run("legacy provider-centric", func(t *testing.T) {
		writeProfileConfig(t, map[string]any{
			"schema_version": CurrentSchemaVersion,
			"agents":         map[string]any{"defaults": map[string]any{"model": "openrouter/legacy-model"}},
			"providers":      map[string]any{"openrouter": map[string]any{"api_key": "k", "enabled": true}},
		})
		cfg, err := LoadStrict()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if err := cfg.ApplyProfile(cfg.SelectProfile("")); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if cfg.UseModelsConfig() || cfg.Agents.Defaults.Model != "openrouter/legacy-model" {
			t.Fatal("a config with no profiles must be untouched")
		}
	})

	t.Run("model-centric", func(t *testing.T) {
		writeProfileConfig(t, map[string]any{
			"schema_version": CurrentSchemaVersion,
			"agents":         map[string]any{"defaults": map[string]any{"model": "openrouter/m"}},
			"models_config": map[string]any{
				"models": []any{map[string]any{"name": "m", "model": "openrouter/m", "api_key": "k"}},
				"agent":  map[string]any{"model": "m"},
			},
		})
		cfg, err := LoadStrict()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if err := cfg.ApplyProfile(cfg.SelectProfile("")); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if len(cfg.ModelsConfig.Models) != 1 || cfg.ModelsConfig.Models[0].Name != "m" {
			t.Fatalf("a config with no profiles must keep its models block, got %+v", cfg.ModelsConfig)
		}
	})
}

// TestAppliedProfileResolvesToADialableModel checks the profile actually
// reaches the provider layer: a profile that sets the config but does not
// resolve is a failure the operator would only meet on the first request.
func TestAppliedProfileResolvesToADialableModel(t *testing.T) {
	writeProfileConfig(t, baseProfileConfig())
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.ApplyProfile("local"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	resolved, err := cfg.ResolveModelConfig("local")
	if err != nil {
		t.Fatalf("an applied profile must resolve: %v", err)
	}
	if resolved.APIBase != "http://localhost:11434/v1" {
		t.Fatalf("the profile's api_base must reach the provider, got %q", resolved.APIBase)
	}
	if !strings.Contains(resolved.ModelID, "qwen3:8b") {
		t.Fatalf("the profile's model must reach the provider, got %q", resolved.ModelID)
	}
}

// TestProfileModelIDKeepsAnExplicitPrefix — a model already carrying a prefix
// must not be double-prefixed into "openrouter/openrouter/x", which routes
// nowhere.
func TestProfileModelIDKeepsAnExplicitPrefix(t *testing.T) {
	p := Profile{Provider: "openrouter", Model: "openrouter/z-ai/glm-4.6"}
	if got := p.ProfileModelID(); got != "openrouter/z-ai/glm-4.6" {
		t.Fatalf("an explicit prefix must be preserved, got %q", got)
	}
	bare := Profile{Provider: "ollama", Model: "qwen3:8b"}
	if got := bare.ProfileModelID(); got != "ollama/qwen3:8b" {
		t.Fatalf("a bare model must take the provider prefix, got %q", got)
	}
}
