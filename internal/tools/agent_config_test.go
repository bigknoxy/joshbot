package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubagentConfig_Defaults(t *testing.T) {
	cfg := &SubagentConfig{}
	cfg.SetDefaults()

	if cfg.Temperature != 0.3 {
		t.Errorf("expected default Temperature 0.3, got %v", cfg.Temperature)
	}
	if cfg.MaxTokens != 500 {
		t.Errorf("expected default MaxTokens 500, got %d", cfg.MaxTokens)
	}
}

func TestSubagentConfig_DefaultsPreserveExisting(t *testing.T) {
	cfg := &SubagentConfig{
		Temperature: 0.7,
		MaxTokens:   1000,
	}
	cfg.SetDefaults()

	if cfg.Temperature != 0.7 {
		t.Errorf("expected Temperature to remain 0.7, got %v", cfg.Temperature)
	}
	if cfg.MaxTokens != 1000 {
		t.Errorf("expected MaxTokens to remain 1000, got %d", cfg.MaxTokens)
	}
}

func TestSubagentConfigManager_NewManager(t *testing.T) {
	dir := t.TempDir()
	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil manager")
	}

	configs := m.List()
	if len(configs) != 0 {
		t.Errorf("expected empty list for new manager, got %d", len(configs))
	}
}

func TestSubagentConfigManager_DiscoverEmptyDir(t *testing.T) {
	dir := t.TempDir()
	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	if err := m.Discover(); err != nil {
		t.Fatalf("Discover on empty dir should not error: %v", err)
	}

	configs := m.List()
	if len(configs) != 0 {
		t.Errorf("expected empty config list, got %d", len(configs))
	}
}

func TestSubagentConfigManager_DiscoverValidConfigs(t *testing.T) {
	dir := t.TempDir()

	// Write two valid config files.
	config1 := `name: code-reviewer
description: Reviews Go code for bugs and style issues
model: openrouter/anthropic/claude-sonnet-4
temperature: 0.1
max_tokens: 1000
system_prompt: You are a senior Go code reviewer.
tools:
  - filesystem
  - grep
skills:
  - codex
tags:
  - code-review
  - go
`
	config2 := `name: research-agent
description: General-purpose research assistant
model: openrouter/mistral/mixtral-8x22b
temperature: 0.7
max_tokens: 2000
tools:
  - web
  - filesystem
`

	if err := os.WriteFile(filepath.Join(dir, "code-reviewer.yaml"), []byte(config1), 0644); err != nil {
		t.Fatalf("write config1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "research-agent.yml"), []byte(config2), 0644); err != nil {
		t.Fatalf("write config2: %v", err)
	}

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	if err := m.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	configs := m.List()
	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}

	// Verify first config.
	cr, ok := m.Get("code-reviewer")
	if !ok {
		t.Fatal("expected to find code-reviewer config")
	}
	if cr.Description != "Reviews Go code for bugs and style issues" {
		t.Errorf("unexpected description: %q", cr.Description)
	}
	if cr.Model != "openrouter/anthropic/claude-sonnet-4" {
		t.Errorf("unexpected model: %q", cr.Model)
	}
	if cr.Temperature != 0.1 {
		t.Errorf("unexpected temperature: %v", cr.Temperature)
	}
	if cr.MaxTokens != 1000 {
		t.Errorf("unexpected max_tokens: %d", cr.MaxTokens)
	}
	if cr.SystemPrompt != "You are a senior Go code reviewer." {
		t.Errorf("unexpected system_prompt: %q", cr.SystemPrompt)
	}
	if len(cr.Tools) != 2 || cr.Tools[0] != "filesystem" || cr.Tools[1] != "grep" {
		t.Errorf("unexpected tools: %v", cr.Tools)
	}
	if len(cr.Skills) != 1 || cr.Skills[0] != "codex" {
		t.Errorf("unexpected skills: %v", cr.Skills)
	}
	if len(cr.Tags) != 2 || cr.Tags[0] != "code-review" || cr.Tags[1] != "go" {
		t.Errorf("unexpected tags: %v", cr.Tags)
	}

	// Verify second config.
	ra, ok := m.Get("research-agent")
	if !ok {
		t.Fatal("expected to find research-agent config")
	}
	if ra.Model != "openrouter/mistral/mixtral-8x22b" {
		t.Errorf("unexpected model: %q", ra.Model)
	}
	if ra.Temperature != 0.7 {
		t.Errorf("unexpected temperature: %v", ra.Temperature)
	}
	if ra.MaxTokens != 2000 {
		t.Errorf("unexpected max_tokens: %d", ra.MaxTokens)
	}
}

func TestSubagentConfigManager_Get(t *testing.T) {
	dir := t.TempDir()

	config1 := `name: my-agent
description: A test agent
model: openrouter/anthropic/claude-sonnet-4
`
	if err := os.WriteFile(filepath.Join(dir, "my-agent.yaml"), []byte(config1), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}
	if err := m.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	cfg, ok := m.Get("my-agent")
	if !ok {
		t.Fatal("expected Get to return true for my-agent")
	}
	if cfg.Name != "my-agent" {
		t.Errorf("expected name 'my-agent', got %q", cfg.Name)
	}
	if cfg.Description != "A test agent" {
		t.Errorf("expected description 'A test agent', got %q", cfg.Description)
	}
}

func TestSubagentConfigManager_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}
	if err := m.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	_, ok := m.Get("nonexistent")
	if ok {
		t.Error("expected Get to return false for unknown name")
	}
}

func TestSubagentConfigManager_List(t *testing.T) {
	dir := t.TempDir()

	configs := map[string]string{
		"alpha": `name: alpha
description: First agent
model: openrouter/anthropic/claude-sonnet-4
`,
		"beta": `name: beta
description: Second agent
model: openrouter/mistral/mixtral-8x22b
`,
		"gamma": `name: gamma
description: Third agent
model: openrouter/meta-llama/llama-3-70b
`,
	}

	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0644); err != nil {
			t.Fatalf("write config %s: %v", name, err)
		}
	}

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}
	if err := m.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	list := m.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 configs, got %d", len(list))
	}

	// Verify all names are present.
	names := make(map[string]bool)
	for _, cfg := range list {
		names[cfg.Name] = true
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !names[name] {
			t.Errorf("expected config %q to be in list", name)
		}
	}
}

func TestSubagentConfigManager_Save(t *testing.T) {
	dir := t.TempDir()

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	cfg := &SubagentConfig{
		Name:         "my-custom-agent",
		Description:  "A custom agent saved from code",
		Model:        "openrouter/anthropic/claude-opus-4",
		Temperature:  0.5,
		MaxTokens:    1500,
		SystemPrompt: "You are helpful.",
		Tools:        []string{"filesystem", "web", "shell"},
		Skills:       []string{"codex"},
		Tags:         []string{"custom"},
	}

	if err := m.Save(cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify the file exists on disk.
	filePath := filepath.Join(dir, "my-custom-agent.yaml")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("expected config file to exist after Save")
	}

	// Verify the file content is valid YAML by reading it back.
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}
	if !strings.Contains(string(data), "my-custom-agent") {
		t.Error("saved YAML should contain the agent name")
	}

	// Create a new manager and discover to verify persistence.
	m2, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}
	if err := m2.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	loaded, ok := m2.Get("my-custom-agent")
	if !ok {
		t.Fatal("expected to find saved config after Discover")
	}
	if loaded.Model != "openrouter/anthropic/claude-opus-4" {
		t.Errorf("unexpected model: %q", loaded.Model)
	}
	if loaded.Temperature != 0.5 {
		t.Errorf("unexpected temperature: %v", loaded.Temperature)
	}
	if loaded.MaxTokens != 1500 {
		t.Errorf("unexpected max_tokens: %d", loaded.MaxTokens)
	}
	if loaded.SystemPrompt != "You are helpful." {
		t.Errorf("unexpected system_prompt: %q", loaded.SystemPrompt)
	}
	if len(loaded.Tools) != 3 || loaded.Tools[0] != "filesystem" {
		t.Errorf("unexpected tools: %v", loaded.Tools)
	}
}

func TestSubagentConfigManager_SaveEmptyName(t *testing.T) {
	dir := t.TempDir()
	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	cfg := &SubagentConfig{
		Description: "No name agent",
	}
	err = m.Save(cfg)
	if err == nil {
		t.Fatal("expected error when saving config with empty name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected 'name is required' error, got: %v", err)
	}
}

func TestSubagentConfigManager_SaveDefaultsApplied(t *testing.T) {
	dir := t.TempDir()
	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	// Save without Temperature or MaxTokens — defaults should be applied.
	cfg := &SubagentConfig{
		Name:  "defaults-test",
		Model: "openrouter/anthropic/claude-sonnet-4",
	}
	if err := m.Save(cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, ok := m.Get("defaults-test")
	if !ok {
		t.Fatal("expected to find saved config")
	}
	if loaded.Temperature != 0.3 {
		t.Errorf("expected default Temperature 0.3 after save, got %v", loaded.Temperature)
	}
	if loaded.MaxTokens != 500 {
		t.Errorf("expected default MaxTokens 500 after save, got %d", loaded.MaxTokens)
	}
}

func TestSubagentConfigManager_InvalidYaml(t *testing.T) {
	dir := t.TempDir()

	// Write a file with invalid YAML content.
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("{{invalid yaml: [broken}"), 0644); err != nil {
		t.Fatalf("write bad config: %v", err)
	}

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	err = m.Discover()
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	// Error should include the filename.
	if !strings.Contains(err.Error(), "bad.yaml") {
		t.Errorf("expected error to mention filename 'bad.yaml', got: %v", err)
	}
	// Error should mention YAML.
	if !strings.Contains(err.Error(), "YAML") && !strings.Contains(err.Error(), "yaml") {
		t.Errorf("expected error to mention YAML parsing, got: %v", err)
	}
}

func TestSubagentConfigManager_MissingName(t *testing.T) {
	dir := t.TempDir()

	config := `description: I have no name
model: openrouter/anthropic/claude-sonnet-4
`
	if err := os.WriteFile(filepath.Join(dir, "noname.yaml"), []byte(config), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	err = m.Discover()
	if err == nil {
		t.Fatal("expected error for config with missing name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("expected error to mention 'name', got: %v", err)
	}
}

func TestSubagentConfigManager_DuplicateName(t *testing.T) {
	dir := t.TempDir()

	// Write two files with the same name field.
	configA := `name: duplicate
description: First occurrence
model: openrouter/anthropic/claude-sonnet-4
`
	configB := `name: duplicate
description: Second occurrence
model: openrouter/mistral/mixtral-8x22b
`

	if err := os.WriteFile(filepath.Join(dir, "dup-a.yaml"), []byte(configA), 0644); err != nil {
		t.Fatalf("write configA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dup-b.yaml"), []byte(configB), 0644); err != nil {
		t.Fatalf("write configB: %v", err)
	}

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	err = m.Discover()
	if err == nil {
		t.Fatal("expected error for duplicate config names")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected error to mention 'duplicate', got: %v", err)
	}
	if !strings.Contains(err.Error(), "dup-b.yaml") {
		t.Errorf("expected error to mention the duplicate file 'dup-b.yaml', got: %v", err)
	}
}

func TestSubagentConfigManager_NonExistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	err = m.Discover()
	if err == nil {
		t.Fatal("expected error for non-existent config directory")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected error to mention 'does not exist', got: %v", err)
	}
}

func TestSubagentConfigManager_SkipsNonYamlFiles(t *testing.T) {
	dir := t.TempDir()

	// Write a valid config and some non-YAML files.
	config := `name: valid-agent
description: The only valid config
model: openrouter/anthropic/claude-sonnet-4
`
	if err := os.WriteFile(filepath.Join(dir, "valid.yaml"), []byte(config), 0644); err != nil {
		t.Fatalf("write valid config: %v", err)
	}
	// Non-YAML files should be skipped.
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a config"), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# Notes"), 0644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	// Subdirectories should be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.yaml"), []byte(config), 0644); err != nil {
		t.Fatalf("write nested config: %v", err)
	}

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}
	if err := m.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	list := m.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 config (skipping non-yaml and subdirectories), got %d", len(list))
	}
	if list[0].Name != "valid-agent" {
		t.Errorf("expected 'valid-agent', got %q", list[0].Name)
	}
}

func TestSubagentConfigManager_SaveCreatesDirectory(t *testing.T) {
	// Use a non-existent nested directory — Save should create it.
	baseDir := t.TempDir()
	nestedDir := filepath.Join(baseDir, "sub", "agents")

	m, err := NewSubagentConfigManager(nestedDir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	cfg := &SubagentConfig{
		Name:  "deep-agent",
		Model: "openrouter/anthropic/claude-sonnet-4",
	}
	if err := m.Save(cfg); err != nil {
		t.Fatalf("Save should create directory and succeed: %v", err)
	}

	// Verify the file exists.
	filePath := filepath.Join(nestedDir, "deep-agent.yaml")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("expected config file to exist after Save in newly created directory")
	}

	loaded, ok := m.Get("deep-agent")
	if !ok {
		t.Fatal("expected to find deep-agent after save")
	}
	if loaded.Model != "openrouter/anthropic/claude-sonnet-4" {
		t.Errorf("unexpected model: %q", loaded.Model)
	}
}

func TestSubagentConfigManager_GetAfterDiscoverIdempotent(t *testing.T) {
	dir := t.TempDir()

	config := `name: stable-agent
description: This agent should be consistently found
model: openrouter/anthropic/claude-sonnet-4
temperature: 0.2
max_tokens: 800
`
	if err := os.WriteFile(filepath.Join(dir, "stable.yaml"), []byte(config), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m, err := NewSubagentConfigManager(dir)
	if err != nil {
		t.Fatalf("NewSubagentConfigManager failed: %v", err)
	}

	// First discover.
	if err := m.Discover(); err != nil {
		t.Fatalf("first Discover failed: %v", err)
	}

	cfg, ok := m.Get("stable-agent")
	if !ok {
		t.Fatal("expected to find stable-agent after first Discover")
	}
	if cfg.Temperature != 0.2 {
		t.Errorf("expected temperature 0.2, got %v", cfg.Temperature)
	}

	// Second discover should not produce errors or change state.
	if err := m.Discover(); err != nil {
		t.Fatalf("second Discover failed: %v", err)
	}

	cfg2, ok := m.Get("stable-agent")
	if !ok {
		t.Fatal("expected to find stable-agent after second Discover")
	}
	if cfg2.Temperature != 0.2 {
		t.Errorf("expected temperature 0.2 after second discover, got %v", cfg2.Temperature)
	}
}
