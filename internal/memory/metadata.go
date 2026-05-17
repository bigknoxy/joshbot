package memory

import "time"

// UserMetadata tracks structured user information that changes slowly.
// Stored as YAML frontmatter in USER.md.
type UserMetadata struct {
	Name        string            `yaml:"name" json:"name"`
	Preferences map[string]string `yaml:"preferences" json:"preferences"`
	Patterns    map[string]string `yaml:"patterns" json:"patterns"`
	Context     map[string]string `yaml:"context" json:"context"`
	UpdatedAt   time.Time         `yaml:"updated_at" json:"updated_at"`
	Version     int               `yaml:"version" json:"version"`
}
