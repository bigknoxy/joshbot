package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillAvailableNoRequirements(t *testing.T) {
	sk := &Skill{
		Name:         "test",
		Description:  "Test skill",
		Path:         "/tmp",
		Requirements: []string{},
	}
	if !sk.Available() {
		t.Error("Skill with no requirements should be available")
	}
}

func TestSkillAvailableBinRequirement(t *testing.T) {
	sk := &Skill{
		Name:         "test",
		Description:  "Test skill",
		Path:         "/tmp",
		Requirements: []string{"bin:ls"},
	}
	if !sk.Available() {
		t.Error("Skill with bin:ls requirement should be available")
	}
}

func TestSkillNotAvailableBinRequirement(t *testing.T) {
	sk := &Skill{
		Name:         "test",
		Description:  "Test skill",
		Path:         "/tmp",
		Requirements: []string{"bin:nonexistent_binary_xyz"},
	}
	if sk.Available() {
		t.Error("Skill with nonexistent bin requirement should not be available")
	}
}

func TestSkillAvailableEnvRequirement(t *testing.T) {
	os.Setenv("TEST_SKILL_ENV", "value")
	defer os.Unsetenv("TEST_SKILL_ENV")

	sk := &Skill{
		Name:         "test",
		Description:  "Test skill",
		Path:         "/tmp",
		Requirements: []string{"env:TEST_SKILL_ENV"},
	}
	if !sk.Available() {
		t.Error("Skill with satisfied env requirement should be available")
	}
}

func TestSkillNotAvailableEnvRequirement(t *testing.T) {
	os.Unsetenv("TEST_SKILL_MISSING_ENV")

	sk := &Skill{
		Name:         "test",
		Description:  "Test skill",
		Path:         "/tmp",
		Requirements: []string{"env:TEST_SKILL_MISSING_ENV"},
	}
	if sk.Available() {
		t.Error("Skill with unsatisfied env requirement should not be available")
	}
}

func TestSkillGetContent(t *testing.T) {
	tmpDir := t.TempDir()
	content := `---
name: test
description: Test skill
---
This is the skill content.`
	err := os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	sk := &Skill{
		Name:        "test",
		Description: "Test skill",
		Path:        tmpDir,
	}

	got := sk.GetContent()
	if got != "This is the skill content." {
		t.Errorf("GetContent() = %q, want %q", got, "This is the skill content.")
	}
}

func TestSkillGetContentNoFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	content := `This skill has no frontmatter.`
	err := os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	sk := &Skill{
		Name:        "test",
		Description: "Test skill",
		Path:        tmpDir,
	}

	got := sk.GetContent()
	if got != "This skill has no frontmatter." {
		t.Errorf("GetContent() = %q, want %q", got, "This skill has no frontmatter.")
	}
}

func TestSkillGetContentMissingFile(t *testing.T) {
	sk := &Skill{
		Name:        "test",
		Description: "Test skill",
		Path:        "/nonexistent/path",
	}

	got := sk.GetContent()
	if got != "" {
		t.Errorf("GetContent() for missing file = %q, want empty", got)
	}
}

func TestSkillToSummaryXML(t *testing.T) {
	sk := &Skill{
		Name:        "my-skill",
		Description: "A test skill",
		Path:        "/tmp",
	}

	xml := sk.ToSummaryXML()
	if xml == "" {
		t.Error("ToSummaryXML() returned empty string")
	}
	// Should contain skill name and available attribute
	if !containsAll(xml, `<skill name="my-skill"`, `available="`, `>A test skill</skill>`) {
		t.Errorf("ToSummaryXML() = %q, expected XML with name and description", xml)
	}
}

func TestLoaderNew(t *testing.T) {
	loader, err := NewLoader("/tmp")
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	if loader == nil {
		t.Fatal("NewLoader() returned nil")
	}
}

func TestLoaderDiscoverNoSkills(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "skills"), 0755)

	loader, err := NewLoader(tmpDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	err = loader.Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
}

func TestLoaderDiscoverWithSkill(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "test-skill")
	os.MkdirAll(skillDir, 0755)

	content := `---
name: test-skill
description: A test skill for testing
---
Full skill content here.`
	err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	loader, err := NewLoader(tmpDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}

	err = loader.Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	sk := loader.GetSkill("test-skill")
	if sk == nil {
		t.Fatal("GetSkill(\"test-skill\") returned nil")
	}
	if sk.Description != "A test skill for testing" {
		t.Errorf("GetSkill() description = %q, want %q", sk.Description, "A test skill for testing")
	}
}

func TestLoadSummary(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "test-skill")
	os.MkdirAll(skillDir, 0755)

	content := `---
name: test-skill
description: Summary test skill
---
Content.`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)

	loader, _ := NewLoader(tmpDir)
	loader.Discover()

	summary, err := loader.LoadSummary(nil)
	if err != nil {
		t.Fatalf("LoadSummary() error = %v", err)
	}
	if summary == "" {
		t.Error("LoadSummary() returned empty string")
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > 0 && containsAt(s, substr, 0)))
}

func containsAt(s, substr string, start int) bool {
	if start+len(substr) > len(s) {
		return false
	}
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
