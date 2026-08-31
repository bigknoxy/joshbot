package tools

import (
	"os"
	"path/filepath"
	"strings"
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

// The "get" action is the only working path to a skill's full content: the
// summary the model sees never carries a filesystem path, so read_file
// cannot reach it (issue found in the eval-suite audit).
func TestSkillRegistryTool_Get(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	store, err := skills.LoadTrustStore(filepath.Join(tmpDir, "skills.trust"))
	if err != nil {
		t.Fatalf("LoadTrustStore() error = %v", err)
	}
	loader.SetTrustStore(store)

	content := `---
name: gettable-skill
description: A skill to fetch in full
tags: ["test"]
---

Step 1: Do the thing.
Step 2: Do the other thing.`
	if err := loader.Create("gettable-skill", content); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := loader.Trust("gettable-skill"); err != nil {
		t.Fatalf("Trust() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{"action": "get", "name": "gettable-skill"})
	if result.Error != nil {
		t.Fatalf("Get failed: %v", result.Error)
	}
	if !strings.Contains(result.Output, "Step 1: Do the thing.") {
		t.Errorf("Get() output = %q, want the skill body", result.Output)
	}
}

// A skill that exists but has not been approved must not be readable by
// asking for it directly by name — the summary withholds it, and "get" must
// enforce the same boundary, not offer a side door around it.
func TestSkillRegistryTool_Get_UntrustedSkillIsRefused(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	store, err := skills.LoadTrustStore(filepath.Join(tmpDir, "skills.trust"))
	if err != nil {
		t.Fatalf("LoadTrustStore() error = %v", err)
	}
	loader.SetTrustStore(store)

	content := "---\nname: untrusted-skill\ndescription: not yet approved\n---\nbody"
	if err := loader.Create("untrusted-skill", content); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Deliberately not trusted.

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{"action": "get", "name": "untrusted-skill"})
	if result.Error == nil {
		t.Fatal("expected an error for an unapproved skill")
	}
}

func TestSkillRegistryTool_Get_UnknownSkill(t *testing.T) {
	tmpDir := t.TempDir()
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0755)

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{"action": "get", "name": "does-not-exist"})
	if result.Error == nil {
		t.Fatal("expected an error for an unknown skill")
	}
}

// A bundled skill has no real filesystem path at all — Path is a virtual
// "bundled/<name>" string, since the content ships embedded in the binary
// via go:embed. Before "get" existed this was the one class of skill the
// model had no working way to load in full: read_file could not resolve
// its path, and no tool exposed LoadFullSkillContent.
func TestSkillRegistryTool_Get_BundledSkill(t *testing.T) {
	loader, err := skills.NewLoader(t.TempDir())
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{"action": "get", "name": "cron"})
	if result.Error != nil {
		t.Fatalf("Get(cron) failed: %v", result.Error)
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Error("expected the bundled skill's embedded content, got nothing")
	}
}

func TestSkillRegistryTool_Get_MissingName(t *testing.T) {
	loader, err := skills.NewLoader(t.TempDir())
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	tool := NewSkillRegistryTool(loader)
	result := tool.Execute(nil, map[string]any{"action": "get"})
	if result.Error == nil {
		t.Fatal("expected an error for a missing name")
	}
}
