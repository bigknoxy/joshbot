package skills

import (
	"testing"
)

func TestValidateSkill_Valid(t *testing.T) {
	content := `---
name: my-test-skill
description: A test skill with all required fields
tags: ["test", "automation"]
requirements: ["bin:git"]
always: false
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

func TestValidateSkill_EmptyContent(t *testing.T) {
	err := ValidateSkill("", nil)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if err.Error() != "skill content is empty" {
		t.Errorf("expected 'skill content is empty', got: %v", err)
	}
}

func TestValidateSkill_WhitespaceOnly(t *testing.T) {
	err := ValidateSkill("   \n  \t  \n  ", nil)
	if err == nil {
		t.Fatal("expected error for whitespace-only content")
	}
	if err.Error() != "skill content is empty" {
		t.Errorf("expected 'skill content is empty', got: %v", err)
	}
}

func TestValidateSkill_MissingFrontmatter(t *testing.T) {
	content := `# Just a markdown file

This has no YAML frontmatter.`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
	if err.Error() != "missing YAML frontmatter: content must start with '---'" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSkill_NoClosingFrontmatter(t *testing.T) {
	content := `---
name: broken-skill
description: Missing closing delimiter
This should fail because there is no closing delimiter`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for missing closing frontmatter")
	}
	if err.Error() != "invalid YAML frontmatter: missing closing '---'" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSkill_EmptyFrontmatter(t *testing.T) {
	content := `---
---

body content here`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for empty frontmatter")
	}
	if err.Error() != "empty YAML frontmatter" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSkill_MissingName(t *testing.T) {
	content := `---
description: A skill without a name
tags: ["test"]
---

Body content here.`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if err.Error() != "missing 'name' in YAML frontmatter" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSkill_EmptyName(t *testing.T) {
	content := `---
name: ""
description: Skill with empty name
---

Body content here.`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if err.Error() != "missing 'name' in YAML frontmatter" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSkill_NameConflict(t *testing.T) {
	existing := []*Skill{
		{Name: "existing-skill"},
	}

	content := `---
name: existing-skill
description: A duplicate name
tags: ["test"]
---

Body content here.
`

	err := ValidateSkill(content, existing)
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	expected := `skill name "existing-skill" already exists`
	if err.Error() != expected {
		t.Errorf("expected %q, got: %v", expected, err)
	}
}

func TestValidateSkill_NameConflictWithMultipleExisting(t *testing.T) {
	existing := []*Skill{
		{Name: "skill-one"},
		{Name: "skill-two"},
		{Name: "skill-three"},
	}

	content := `---
name: skill-two
description: Conflicts with skill-two
---

Body content here.
`

	err := ValidateSkill(content, existing)
	if err == nil {
		t.Fatal("expected error for duplicate name among multiple existing")
	}
}

func TestValidateSkill_NoConflictWithDifferentName(t *testing.T) {
	existing := []*Skill{
		{Name: "other-skill"},
	}

	content := `---
name: new-skill
description: A brand new skill
---

Body content here.
`

	err := ValidateSkill(content, existing)
	if err != nil {
		t.Errorf("expected no error for different name, got: %v", err)
	}
}

func TestValidateSkill_NilExistingSkills(t *testing.T) {
	// Should not panic when existing is nil
	content := `---
name: valid-skill
description: A valid skill
---

Body content.
`

	err := ValidateSkill(content, nil)
	if err != nil {
		t.Errorf("expected no error with nil existing skills, got: %v", err)
	}
}

func TestValidateSkill_NilElementInExistingSkills(t *testing.T) {
	// Should not panic when an element in existing is nil
	existing := []*Skill{
		{Name: "real-skill"},
		nil,
		{Name: "another-skill"},
	}

	content := `---
name: unique-skill
description: Not conflicting
---

Body.
`

	err := ValidateSkill(content, existing)
	if err != nil {
		t.Errorf("expected no error with nil element in existing, got: %v", err)
	}
}

func TestValidateSkill_EmptyBody(t *testing.T) {
	content := `---
name: empty-body-skill
description: This skill has no body
---
`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if err.Error() != "skill body is empty" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSkill_BodyWithOnlyWhitespace(t *testing.T) {
	content := `---
name: whitespace-body-skill
description: Body is just whitespace
---
   
   
   
`

	err := ValidateSkill(content, nil)
	if err == nil {
		t.Fatal("expected error for whitespace-only body")
	}
	if err.Error() != "skill body is empty" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateSkill_SpecialCharactersInName(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name: "hyphens allowed",
			content: `---
name: git-commit-skill
description: A skill with hyphens
---

Body.`,
			wantErr: false,
		},
		{
			name: "underscores allowed",
			content: `---
name: git_commit_skill
description: A skill with underscores
---

Body.`,
			wantErr: false,
		},
		{
			name: "dots allowed",
			content: `---
name: my.skill.name
description: A skill with dots
---

Body.`,
			wantErr: false,
		},
		{
			name: "numbers allowed",
			content: `---
name: skill123
description: A skill with numbers
---

Body.`,
			wantErr: false,
		},
		{
			name: "path traversal with slashes",
			content: `---
name: ../../../etc/passwd
description: Path traversal attempt
---

Body.`,
			wantErr: false, // Name is a string, validation doesn't forbid slashes
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSkill(tt.content, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSkill() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSkill_ExtraWhitespaceInFrontmatter(t *testing.T) {
	content := `---
  name:   spaced-out-skill  
  description:   Has extra whitespace  
---

Body content.
`

	err := ValidateSkill(content, nil)
	if err != nil {
		t.Errorf("expected no error with extra whitespace, got: %v", err)
	}
}

func TestValidateSkill_NameWithQuotesInFrontmatter(t *testing.T) {
	content := `---
name: "quoted-name-skill"
description: Name is quoted
---

Body.
`

	err := ValidateSkill(content, nil)
	if err != nil {
		t.Errorf("expected no error for quoted name, got: %v", err)
	}
}

func TestValidateSkill_NameWithSingleQuotes(t *testing.T) {
	content := `---
name: 'single-quoted-skill'
description: Name is single-quoted
---

Body.
`

	err := ValidateSkill(content, nil)
	if err != nil {
		t.Errorf("expected no error for single-quoted name, got: %v", err)
	}
}
