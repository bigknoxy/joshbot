package skills

import (
	"fmt"
	"strings"
)

// ValidateSkill checks that content is a well-formed SKILL.md with valid YAML
// frontmatter, a non-empty name, non-empty body, and no name conflicts with the
// provided existing skills slice.
func ValidateSkill(content string, existing []*Skill) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("skill content is empty")
	}

	// Must start with YAML frontmatter delimiter
	if !strings.HasPrefix(content, "---") {
		return fmt.Errorf("missing YAML frontmatter: content must start with '---'")
	}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return fmt.Errorf("invalid YAML frontmatter: missing closing '---'")
	}

	frontmatter := strings.TrimSpace(parts[1])
	body := strings.TrimSpace(parts[2])

	if frontmatter == "" {
		return fmt.Errorf("empty YAML frontmatter")
	}

	// Extract name from frontmatter
	var name string
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.Trim(strings.TrimPrefix(line, "name:"), " \"'")
			break
		}
	}

	if name == "" {
		return fmt.Errorf("missing 'name' in YAML frontmatter")
	}

	// Check for name conflicts with existing skills
	for _, sk := range existing {
		if sk != nil && sk.Name == name {
			return fmt.Errorf("skill name %q already exists", name)
		}
	}

	// Body must be non-empty
	if body == "" {
		return fmt.Errorf("skill body is empty")
	}

	return nil
}
