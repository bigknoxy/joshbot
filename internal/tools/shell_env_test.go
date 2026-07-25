package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A spawned command inherits the agent's environment unless we say otherwise,
// which hands every provider API key to any command the model chooses to run.
// No filesystem sandbox helps here: the secret is already in the child's own
// environment, so `env` is enough and nothing needs to be read from disk.
func TestShellEnv_SecretsDoNotReachTheChild(t *testing.T) {
	secrets := map[string]string{
		"JOSHBOT_PROVIDERS__OPENROUTER__API_KEY": "sk-or-v1-must-not-leak",
		"JOSHBOT_OPENROUTER_API_KEY":             "sk-or-v1-must-not-leak-2",
		"ANTHROPIC_API_KEY":                      "sk-ant-must-not-leak",
		"GITHUB_TOKEN":                           "ghp_must_not_leak",
		"AWS_SECRET_ACCESS_KEY":                  "aws-must-not-leak",
		"MY_APP_PASSWORD":                        "hunter2-must-not-leak",
		"SOME_PRIVATE_KEY":                       "-----BEGIN-must-not-leak",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}

	tool := NewShellTool(10*time.Second, t.TempDir(), false)
	// Grep rather than dumping the whole environment: `env` output exceeds the
	// tool's output truncation limit, which would hide leaks behind the cut.
	res := tool.Execute(context.Background(), map[string]any{
		"command": "env | grep -F must-not-leak || true",
	})
	if res.Error != nil {
		t.Fatalf("command failed: %v", res.Error)
	}

	// The tool reports "(command completed with no output)" when grep matches
	// nothing, so assert on the marker rather than on emptiness.
	if strings.Contains(res.Output, "must-not-leak") {
		t.Errorf("secrets reached the child environment:\n%s", res.Output)
	}
	for name, value := range secrets {
		if strings.Contains(res.Output, value) {
			t.Errorf("%s leaked its value", name)
		}
		if strings.Contains(res.Output, name+"=") {
			t.Errorf("%s was passed to the child", name)
		}
	}
}

// The tool's job is running builds, tests and git. Stripping the environment
// down to nothing would close the leak by making the tool useless, so the
// variables those commands actually need must survive.
func TestShellEnv_UsefulVariablesSurvive(t *testing.T) {
	t.Setenv("GOCACHE", "/tmp/gocache-probe")
	t.Setenv("LC_ALL", "C.UTF-8")

	tool := NewShellTool(10*time.Second, t.TempDir(), false)
	for _, name := range []string{"PATH", "HOME", "GOCACHE", "LC_ALL"} {
		res := tool.Execute(context.Background(), map[string]any{
			"command": "printenv " + name + " || true",
		})
		if res.Error != nil {
			t.Fatalf("printenv %s failed: %v", name, res.Error)
		}
		if strings.TrimSpace(res.Output) == "" {
			t.Errorf("%s did not survive; the tool needs it to be useful", name)
		}
	}
}

// The async path spawns its own process and was missing the same protection.
func TestShellEnv_AsyncPathIsAlsoFiltered(t *testing.T) {
	t.Setenv("JOSHBOT_PROVIDERS__GROQ__API_KEY", "gsk-must-not-leak-async")

	tool := NewShellTool(10*time.Second, t.TempDir(), false)
	done := make(chan AsyncResult, 1)
	tool.ExecuteAsync(context.Background(), map[string]any{"command": "env"}, func(r AsyncResult) {
		done <- r
	})

	select {
	case r := <-done:
		if strings.Contains(r.Output, "gsk-must-not-leak-async") {
			t.Error("the async path leaked a provider key to the child")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("async command did not complete")
	}
}

func TestIsSecretEnvName(t *testing.T) {
	secret := []string{
		"JOSHBOT_PROVIDERS__OPENROUTER__API_KEY", "JOSHBOT_ANYTHING",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GITHUB_TOKEN", "GH_TOKEN",
		"AWS_SECRET_ACCESS_KEY", "DB_PASSWORD", "MY_PRIVATE_KEY",
		"SLACK_WEBHOOK", "NPM_AUTH_TOKEN", "api_key", "Secret_Thing",
	}
	for _, name := range secret {
		if !isSecretEnvName(name) {
			t.Errorf("isSecretEnvName(%q) = false, want true", name)
		}
	}

	notSecret := []string{"PATH", "HOME", "GOCACHE", "LANG", "TERM", "KEYBOARD_LAYOUT"}
	for _, name := range notSecret {
		if isSecretEnvName(name) {
			t.Errorf("isSecretEnvName(%q) = true, want false", name)
		}
	}
}
