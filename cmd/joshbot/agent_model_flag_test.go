package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Under a *model-centric* config `--model` resolves in two ways that the
// legacy-config test next door never reaches: by the entry's `name`, or by its
// `model` id with the provider prefix optionally stripped. Both failure modes
// are silent — a resolution that misses leaves the configured default selected,
// so the run answers normally, bills the wrong model, and says so nowhere.

// modelsEnv writes a model-centric config with two entries and returns its path.
// "cheap" is the configured agent model, so a test asserting "model-b" proves
// the flag changed something rather than agreeing with the config by accident.
func modelsEnv(t *testing.T, apiBase string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".joshbot"), 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"workspace": filepath.Join(home, "workspace"),
				"streaming": false,
			},
		},
		"models_config": map[string]any{
			"agent": map[string]any{"model": "cheap"},
			"models": []any{
				map[string]any{"name": "cheap", "model": "openrouter/model-a",
					"api_key": "sk-test", "api_base": apiBase},
				map[string]any{"name": "big", "model": "openrouter/model-b",
					"api_key": "sk-test", "api_base": apiBase},
			},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	path := filepath.Join(home, ".joshbot", "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// modelSentFor runs one agent turn with the given --model spelling (empty means
// the flag is omitted) and reports what the provider was asked for.
func modelSentFor(t *testing.T, flag string) string {
	t.Helper()

	srv := newRecordingChatServer(t, "ok")
	cfg := modelsEnv(t, srv.URL+"/v1")

	args := []string{"--config", cfg, "agent", "-m", "hi"}
	if flag != "" {
		args = append(args, "--model", flag)
	}
	out, code := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, out)
	}

	reqs := srv.requests()
	if len(reqs) == 0 {
		t.Fatal("the provider was never called")
	}
	got, _ := reqs[0]["model"].(string)
	return got
}

// All three spellings name the same entry and must land on it. The nickname is
// what `joshbot models` prints; the model id (prefixed or bare) is what
// operators copy out of provider dashboards and logs.
func TestAgentModelFlagResolvesAgainstAModelCentricConfig(t *testing.T) {
	for _, flag := range []string{"big", "openrouter/model-b", "model-b"} {
		t.Run(flag, func(t *testing.T) {
			if got := modelSentFor(t, flag); got != "model-b" {
				t.Errorf("--model %q reached the provider as %q, want model-b", flag, got)
			}
		})
	}
}

// The control for the three above: with no flag the configured agent model is
// used, which is exactly what a failed resolution silently falls back to.
// Without this, a run that quietly ignored --model would be indistinguishable
// from one that honoured it.
func TestAgentWithoutModelFlagUsesTheConfiguredModel(t *testing.T) {
	if got := modelSentFor(t, ""); got != "model-a" {
		t.Errorf("provider was asked for %q, want the configured model-a", got)
	}
}
