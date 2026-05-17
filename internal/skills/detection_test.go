package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetect_SinglePattern(t *testing.T) {
	d := NewSkillDetector()

	// A single tool call should not trigger detection (< 3 tools)
	trace := Trace{
		UserMessage: "read a file",
		ToolCalls: []ToolCallRecord{
			{Tool: "read_file", Args: map[string]any{"path": "/tmp/test.txt"}, Result: "contents"},
		},
		FinalOutput: "Here are the contents.",
	}

	d.RecordTrace("session-1", trace)
	candidate := d.Detect(trace)

	if candidate != nil {
		t.Errorf("expected nil candidate for single tool call, got confidence=%.1f", candidate.Confidence)
	}
}

func TestDetect_ThreeToolSequence(t *testing.T) {
	d := NewSkillDetector()

	// 3+ tools combined with reusable-template signal meets the 3.0 threshold
	trace := Trace{
		UserMessage: "find and update the file",
		ToolCalls: []ToolCallRecord{
			{Tool: "grep", Args: map[string]any{"pattern": "TODO"}, Result: "found in file1.go"},
			{Tool: "read_file", Args: map[string]any{"path": "file1.go"}, Result: "package main"},
			{Tool: "write_file", Args: map[string]any{"path": "file1.go"}, Result: "written"},
		},
		FinalOutput: "Here is the reusable script. Step 1: grep for pattern. Step 2: read the file. Step 3: update the file.",
	}

	d.RecordTrace("session-1", trace)
	candidate := d.Detect(trace)

	if candidate == nil {
		t.Fatal("expected candidate for 3+ tool calls with reusable output")
	}
	if candidate.Confidence < 1.5 {
		t.Errorf("expected confidence >= 1.5 for 3+ tool sequence with reusable output, got %.1f", candidate.Confidence)
	}
}

func TestDetect_RepeatedPattern(t *testing.T) {
	d := NewSkillDetector()

	// First occurrence - in session-1
	trace1 := Trace{
		UserMessage: "find and replace in files",
		ToolCalls: []ToolCallRecord{
			{Tool: "grep", Result: "match1"},
			{Tool: "read_file", Result: "content"},
			{Tool: "write_file", Result: "done"},
		},
		FinalOutput: "Done.",
	}
	d.RecordTrace("session-1", trace1)

	// Second occurrence - same pattern in session-2
	trace2 := Trace{
		UserMessage: "search and update config",
		ToolCalls: []ToolCallRecord{
			{Tool: "grep", Result: "match2"},
			{Tool: "read_file", Result: "config content"},
			{Tool: "write_file", Result: "updated"},
		},
		FinalOutput: "Config updated.",
	}
	d.RecordTrace("session-2", trace2)

	// Detect on the second occurrence - should have higher confidence
	candidate := d.Detect(trace2)

	if candidate == nil {
		t.Fatal("expected candidate for repeated pattern")
	}
	// 1.0 (3+ tools) + 2.0 (repeated across sessions) = 3.0
	if candidate.Confidence < 3.0 {
		t.Errorf("expected confidence >= 3.0 for repeated pattern, got %.1f", candidate.Confidence)
	}
}

func TestDetect_ExplicitCommand(t *testing.T) {
	d := NewSkillDetector()

	trace := Trace{
		UserMessage: "please create a skill for this git workflow",
		ToolCalls: []ToolCallRecord{
			{Tool: "shell", Result: "done"},
		},
		FinalOutput: "Created the skill.",
	}

	d.RecordTrace("session-1", trace)
	candidate := d.Detect(trace)

	if candidate == nil {
		t.Fatal("expected candidate for explicit create-a-skill command")
	}
	if candidate.Confidence < 3.0 {
		t.Errorf("expected confidence >= 3.0 for explicit command, got %.1f", candidate.Confidence)
	}
}

func TestDetect_AlwaysCommand(t *testing.T) {
	d := NewSkillDetector()

	trace := Trace{
		UserMessage: "can you always do this formatting automatically",
		ToolCalls: []ToolCallRecord{
			{Tool: "shell", Result: "formatting done"},
			{Tool: "read_file", Result: "config content"},
			{Tool: "write_file", Result: "config updated"},
		},
		FinalOutput: "Sure, I can automate that formatting for you.",
	}

	d.RecordTrace("session-1", trace)
	candidate := d.Detect(trace)

	if candidate == nil {
		t.Fatal("expected candidate for 'always do this' command with 3+ tools")
	}
	// "always" signal (2.0) + 3+ tools (1.0) = 3.0
	if candidate.Confidence < 3.0 {
		t.Errorf("expected confidence >= 3.0 for 'always' command with tool sequence, got %.1f", candidate.Confidence)
	}
}

func TestValidate_GoodSkill(t *testing.T) {
	content := `---
name: test-skill
description: A test skill
tags: ["test"]
---

This is the skill body content.

Step 1: Do something.
Step 2: Do something else.
`

	err := ValidateSkill(content, nil)
	if err != nil {
		t.Errorf("expected no error for valid skill, got: %v", err)
	}
}

func TestValidate_MissingFrontmatter(t *testing.T) {
	content := `# Just a markdown file with no frontmatter`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestValidate_EmptyBody(t *testing.T) {
	content := `---
name: empty-skill
description: Empty body
---

`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestValidate_NameConflict(t *testing.T) {
	existing := []*Skill{
		{Name: "existing-skill"},
	}

	content := `---
name: existing-skill
description: A duplicate
---

Body content here.
`

	err := ValidateSkill(content, existing)
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func TestCreateSkill_Persists(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(tmpDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	content := `---
name: my-skill
description: A test skill
---

Skill body here.`

	err = loader.Create("my-skill", content)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Verify the file was written
	skillPath := filepath.Join(tmpDir, "skills", "my-skill", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("failed to read created SKILL.md: %v", err)
	}
	if string(data) != content {
		t.Errorf("SKILL.md content mismatch")
	}
}

func TestCreateSkill_InvalidatesCache(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(tmpDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	// Initially no skills
	err = loader.Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	// Create a skill
	content := `---
name: new-skill
description: Newly created
tags: ["test"]
---

Body.`
	err = loader.Create("new-skill", content)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Verify it shows up immediately
	sk := loader.GetSkill("new-skill")
	if sk == nil {
		t.Fatal("expected skill to be available after Create")
	}
}

func TestList(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(tmpDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	// Create a skill
	content := `---
name: listed-skill
description: Listed skill
---

Body.`
	err = loader.Create("listed-skill", content)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	skills := loader.List()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "listed-skill" {
		t.Errorf("expected name 'listed-skill', got %q", skills[0].Name)
	}
}

func TestInvalidate(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(tmpDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	// Create a skill
	content := `---
name: invalidate-skill
description: Test invalidation
---

Body.`
	err = loader.Create("invalidate-skill", content)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Initial discovery works
	if loader.GetSkill("invalidate-skill") == nil {
		t.Fatal("expected skill to be found")
	}

	// Invalidate the cache
	loader.Invalidate()

	// After invalidation, skills map should be empty (re-discovery needed)
	// Calling Discover manually to prove re-discovery works
	err = loader.Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if loader.GetSkill("invalidate-skill") == nil {
		t.Fatal("expected skill to be found after re-discovery")
	}
}

func TestDelete(t *testing.T) {
	tmpDir := t.TempDir()
	loader, err := NewLoader(tmpDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	content := `---
name: deletable-skill
description: Will be deleted
---

Body.`
	err = loader.Create("deletable-skill", content)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if loader.GetSkill("deletable-skill") == nil {
		t.Fatal("expected skill to exist after create")
	}

	err = loader.Delete("deletable-skill")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if loader.GetSkill("deletable-skill") != nil {
		t.Fatal("expected skill to be nil after delete")
	}
}

func TestPatternKey(t *testing.T) {
	tests := []struct {
		name     string
		calls    []ToolCallRecord
		expected string
	}{
		{
			name:     "empty",
			calls:    []ToolCallRecord{},
			expected: "",
		},
		{
			name: "single",
			calls: []ToolCallRecord{
				{Tool: "read_file"},
			},
			expected: "read_file",
		},
		{
			name: "multiple",
			calls: []ToolCallRecord{
				{Tool: "grep"},
				{Tool: "read_file"},
				{Tool: "write_file"},
			},
			expected: "grep→read_file→write_file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := patternKey(tt.calls)
			if got != tt.expected {
				t.Errorf("patternKey() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCandidates(t *testing.T) {
	d := NewSkillDetector()

	trace := Trace{
		UserMessage: "test",
		ToolCalls: []ToolCallRecord{
			{Tool: "read_file", Result: "data"},
			{Tool: "write_file", Result: "done"},
		},
		FinalOutput: "output",
	}

	d.RecordTrace("session-1", trace)
	d.RecordTrace("session-2", trace)

	candidates := d.Candidates()
	if len(candidates) == 0 {
		t.Fatal("expected at least one candidate")
	}
	if len(candidates[0].Name) == 0 {
		t.Error("expected candidate to have a non-empty name")
	}
}
