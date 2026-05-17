package skills

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Skill represents a discovered skill.
type Skill struct {
	Name         string
	Description  string
	Path         string
	Always       bool
	Requirements []string
	Tags         []string
	content      *string
	available    *bool
}

// Available checks requirements (bin: / env:)
func (s *Skill) Available() bool {
	if s.available != nil {
		return *s.available
	}
	ok := true
	for _, req := range s.Requirements {
		if strings.HasPrefix(req, "bin:") {
			bin := strings.TrimPrefix(req, "bin:")
			if _, err := exec.LookPath(bin); err != nil {
				ok = false
				break
			}
		} else if strings.HasPrefix(req, "env:") {
			env := strings.TrimPrefix(req, "env:")
			if os.Getenv(env) == "" {
				ok = false
				break
			}
		}
	}
	s.available = &ok
	return ok
}

// GetContent reads SKILL.md (caches result)
func (s *Skill) GetContent() string {
	if s.content != nil {
		return *s.content
	}
	p := filepath.Join(s.Path, "SKILL.md")
	data, err := os.ReadFile(p)
	var out string
	if err == nil {
		raw := string(data)
		if strings.HasPrefix(raw, "---") {
			parts := strings.SplitN(raw, "---", 3)
			if len(parts) >= 3 {
				out = strings.TrimSpace(parts[2])
			} else {
				out = raw
			}
		} else {
			out = raw
		}
	} else {
		out = ""
	}
	s.content = &out
	return out
}

// ToSummaryXML returns XML summary line
func (s *Skill) ToSummaryXML() string {
	avail := "false"
	if s.Available() {
		avail = "true"
	}
	return fmt.Sprintf("  <skill name=\"%s\" available=\"%s\">%s</skill>", s.Name, avail, s.Description)
}

// Loader discovers skills in bundled and workspace directories.
type Loader struct {
	bundledDir   string
	workspaceDir string
	skills       map[string]*Skill
	loaded       bool
}

// NewLoader creates a new skills loader. workspace should be the workspace root (contains skills/).
func NewLoader(workspace string) (*Loader, error) {
	ws := filepath.Join(workspace, "skills")
	bundled := filepath.Join("skills")
	l := &Loader{
		bundledDir:   bundled,
		workspaceDir: ws,
		skills:       map[string]*Skill{},
	}
	return l, nil
}

// Discover scans bundled and workspace skills. Workspace overrides bundled.
func (l *Loader) Discover() error {
	l.skills = map[string]*Skill{}

	_ = filepath.WalkDir(l.bundledDir, l.walkSkillDir)
	_ = filepath.WalkDir(l.workspaceDir, l.walkSkillDir)

	l.loaded = true
	return nil
}

// walkSkillDir is the filepath.WalkDir callback that registers discovered skills.
func (l *Loader) walkSkillDir(path string, d fs.DirEntry, err error) error {
	if err != nil || !d.IsDir() {
		return nil
	}
	skillFile := filepath.Join(path, "SKILL.md")
	if _, err := os.Stat(skillFile); err != nil {
		return nil
	}
	name := filepath.Base(path)
	if sk := l.parseSkill(path, name); sk != nil {
		l.skills[sk.Name] = sk
	}
	return nil
}

func (l *Loader) parseSkill(dir, defaultName string) *Skill {
	p := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	raw := string(data)
	name := defaultName
	description := ""
	always := false
	var requirements []string
	var tags []string

	if strings.HasPrefix(raw, "---") {
		parts := strings.SplitN(raw, "---", 3)
		if len(parts) >= 3 {
			for _, line := range strings.Split(parts[1], "\n") {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "name:"):
					name = strings.Trim(strings.TrimPrefix(line, "name:"), " \"'")
				case strings.HasPrefix(line, "description:"):
					description = strings.Trim(strings.TrimPrefix(line, "description:"), " \"'")
				case strings.HasPrefix(line, "always:"):
					v := strings.TrimSpace(strings.TrimPrefix(line, "always:"))
					always = v == "true" || v == "yes" || v == "1"
				case strings.HasPrefix(line, "requirements:"):
					requirements = parseYAMLList(strings.TrimSpace(strings.TrimPrefix(line, "requirements:")))
				case strings.HasPrefix(line, "tags:"):
					tags = parseYAMLList(strings.TrimSpace(strings.TrimPrefix(line, "tags:")))
				}
			}
		}
	}

	if description == "" {
		content := raw
		if strings.HasPrefix(raw, "---") {
			if parts := strings.SplitN(raw, "---", 3); len(parts) >= 3 {
				content = parts[2]
			}
		}
		firstPara := strings.SplitN(strings.TrimSpace(content), "\n\n", 2)[0]
		description = strings.ReplaceAll(firstPara, "\n", " ")
		if len(description) > 200 {
			description = description[:200]
		}
	}

	return &Skill{
		Name:         name,
		Description:  description,
		Path:         dir,
		Always:       always,
		Requirements: requirements,
		Tags:         tags,
	}
}

// parseYAMLList parses a bracket-delimited list like ["a", "b", "c"].
func parseYAMLList(s string) []string {
	if !strings.HasPrefix(s, "[") {
		return nil
	}
	s = strings.Trim(s, "[]")
	var result []string
	for _, item := range strings.Split(s, ",") {
		if cleaned := strings.Trim(item, " \"'"); cleaned != "" {
			result = append(result, cleaned)
		}
	}
	return result
}

// LoadSummary returns XML summary of discovered skills. Implements SkillsLoader interface used by agent.
// This returns ONLY summaries - not full content - to reduce prompt bloat.
// Use LoadFullSkillContent() explicitly if you need the full content of a specific skill.
func (l *Loader) LoadSummary(ctx context.Context) (string, error) {
	if !l.loaded {
		if err := l.Discover(); err != nil {
			return "", err
		}
	}

	parts := []string{"Available skills (use read_file to load full skill content when needed):"}
	for _, sk := range l.skills {
		parts = append(parts, sk.ToSummaryXML())
		// NOTE: Full content is NO LONGER included by default to reduce prompt bloat.
		// If full content is needed for a specific skill, use GetSkillContent(name) explicitly.
	}
	return strings.Join(parts, "\n"), nil
}

// LoadFullSkillContent returns the full content of a specific skill by name.
// Use this when the user explicitly requests a skill's full content.
// Returns empty string if skill not found.
func (l *Loader) LoadFullSkillContent(ctx context.Context, skillName string) (string, error) {
	sk := l.GetSkill(skillName)
	if sk == nil {
		return "", nil
	}
	return sk.GetContent(), nil
}

// GetSkill returns a discovered skill by name (nil if not found)
func (l *Loader) GetSkill(name string) *Skill {
	if !l.loaded {
		_ = l.Discover()
	}
	return l.skills[name]
}

// Invalidate clears the skill cache so the next call to Discover, LoadSummary, or GetSkill
// will re-scan both bundled and workspace skill directories.
func (l *Loader) Invalidate() {
	l.loaded = false
	l.skills = map[string]*Skill{}
}

// Create writes a new skill to the workspace directory and triggers re-discovery.
// content must be a valid SKILL.md with YAML frontmatter.
func (l *Loader) Create(name, content string) error {
	dir := filepath.Join(l.workspaceDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create skill directory: %w", err)
	}
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write SKILL.md: %w", err)
	}
	l.Invalidate()
	return nil
}

// Delete removes a skill by name from the workspace directory and triggers re-discovery.
func (l *Loader) Delete(name string) error {
	sk := l.GetSkill(name)
	if sk == nil {
		return fmt.Errorf("skill %q not found", name)
	}
	if err := os.RemoveAll(sk.Path); err != nil {
		return fmt.Errorf("failed to delete skill %q: %w", name, err)
	}
	l.Invalidate()
	return nil
}

// List returns all discovered skills.
func (l *Loader) List() []*Skill {
	if !l.loaded {
		_ = l.Discover()
	}
	skills := make([]*Skill, 0, len(l.skills))
	for _, sk := range l.skills {
		skills = append(skills, sk)
	}
	return skills
}
