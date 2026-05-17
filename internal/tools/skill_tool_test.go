package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bigknoxy/joshbot/internal/skills"
)

func TestSkillRegistryTool_List(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	// Create a skill first
	content := `---
name: listable-skill
description: A skill to list
tags: ["test"]
---

Body.`
	err = loader.Create("listable-skill", content)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{"action": "list"})

	if result.Error != nil {
		t.Fatalf("List failed: %v", result.Error)
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output from List")
	}
}

func TestSkillRegistryTool_Create(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	content := `---
name: new-skill
description: Created via tool
tags: ["test"]
---

Step 1: Do this.
Step 2: Do that.
`

	result := tool.Execute(nil, map[string]any{
		"action":  "create",
		"name":    "new-skill",
		"content": content,
	})

	if result.Error != nil {
		t.Fatalf("Create failed: %v", result.Error)
	}

	// Verify it exists
	sk := loader.GetSkill("new-skill")
	if sk == nil {
		t.Fatal("expected skill to exist after Create")
	}
}

func TestSkillRegistryTool_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	// Create a skill first
	content := `---
name: deletable-skill
description: Will be deleted
tags: ["test"]
---

Body.`
	err = loader.Create("deletable-skill", content)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{
		"action": "delete",
		"name":   "deletable-skill",
	})

	if result.Error != nil {
		t.Fatalf("Delete failed: %v", result.Error)
	}

	// Verify it's gone
	sk := loader.GetSkill("deletable-skill")
	if sk != nil {
		t.Fatal("expected skill to be nil after Delete")
	}
}

func TestSkillRegistryTool_Create_Duplicate(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	content := `---
name: dup-skill
description: Original
tags: ["test"]
---

Body.`
	err = loader.Create("dup-skill", content)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{
		"action":  "create",
		"name":    "dup-skill",
		"content": content,
	})

	if result.Error == nil {
		t.Fatal("expected error for duplicate skill creation")
	}
}

func TestSkillRegistryTool_InvalidAction(t *testing.T) {
	loader, err := skills.NewLoader("/tmp/not-used")
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{
		"action": "invalid",
	})

	if result.Error == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestSkillRegistryTool_MissingName(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{
		"action": "create",
		// no name
		"content": "---\nname: x\n---\nbody",
	})

	if result.Error == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestSkillRegistryTool_MissingContent(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{
		"action": "create",
		"name":   "some-skill",
		// no content
	})

	if result.Error == nil {
		t.Fatal("expected error for missing content")
	}
}
