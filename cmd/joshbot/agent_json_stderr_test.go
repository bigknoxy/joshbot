package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A *setup* failure in JSON mode is the case a wrapper is least able to recover
// from: the run never reached a provider, so there is no result document on
// stdout to read at all. Flag-validation failures are covered elsewhere; this is
// the later emit site, after flags parsed clean and setupComponents failed on
// the config itself. Returning bare there leaves a non-zero exit with an empty
// error channel, which a wrapper cannot tell apart from a crash (issue #220).
func TestAgentJSONSetupFailureIsAnErrorDocumentOnStderr(t *testing.T) {
	// A provider that is present but disabled: registration finds nothing
	// usable and fails, which is a setup failure rather than a flag error.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".joshbot"), 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"workspace": filepath.Join(home, "workspace"),
				"model":     "openrouter/test-model",
				"streaming": false,
			},
		},
		"providers": map[string]any{
			"openrouter": map[string]any{"enabled": false, "api_key": "sk-test"},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	cfgPath := filepath.Join(home, ".joshbot", "config.json")
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var out string
	var code int
	stderr := captureStderr(t, func() {
		out, code = runCLI(t, "--config", cfgPath, "agent", "--output-format", "json", "-m", "hi")
	})

	if code == 0 {
		t.Fatalf("exit code = 0; a config with no usable provider must fail")
	}
	line := lastLine(stderr)
	var doc map[string]any
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		t.Fatalf("stderr is not a JSON document: %q", stderr)
	}
	if doc["type"] != "error" {
		t.Errorf(`stderr document type = %v, want "error": %s`, doc["type"], stderr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout carried %q; in JSON modes stdout is data only", out)
	}
}
