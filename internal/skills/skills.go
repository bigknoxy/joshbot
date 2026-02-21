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

	// bundled first
	_ = filepath.WalkDir(l.bundledDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		// skill directory must contain SKILL.md
		skillFile := filepath.Join(path, "SKILL.md")
		if _, err := os.Stat(skillFile); err == nil {
			if info, _ := os.Stat(path); info.IsDir() {
				name := filepath.Base(path)
				sk := l.parseSkill(path, name)
				if sk != nil {
					l.skills[sk.Name] = sk
				}
			}
		}
		return nil
	})

	// workspace overrides
	_ = filepath.WalkDir(l.workspaceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		skillFile := filepath.Join(path, "SKILL.md")
		if _, err := os.Stat(skillFile); err == nil {
			name := filepath.Base(path)
			sk := l.parseSkill(path, name)
			if sk != nil {
				l.skills[sk.Name] = sk
			}
		}
		return nil
	})

	l.loaded = true
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
	requirements := []string{}
	tags := []string{}

	if strings.HasPrefix(raw, "---") {
		parts := strings.SplitN(raw, "---", 3)
		if len(parts) >= 3 {
			front := parts[1]
			for _, line := range strings.Split(front, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					name = strings.Trim(strings.TrimPrefix(line, "name:"), " \"'")
				} else if strings.HasPrefix(line, "description:") {
					description = strings.Trim(strings.TrimPrefix(line, "description:"), " \"'")
				} else if strings.HasPrefix(line, "always:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "always:"))
					if v == "true" || v == "yes" || v == "1" {
						always = true
					}
				} else if strings.HasPrefix(line, "requirements:") {
					rest := strings.TrimSpace(strings.TrimPrefix(line, "requirements:"))
					if strings.HasPrefix(rest, "[") {
						rest = strings.Trim(rest, "[]")
						for _, r := range strings.Split(rest, ",") {
							r = strings.Trim(r, " \"'")
							if r != "" {
								requirements = append(requirements, r)
							}
						}
					}
				} else if strings.HasPrefix(line, "tags:") {
					rest := strings.TrimSpace(strings.TrimPrefix(line, "tags:"))
					if strings.HasPrefix(rest, "[") {
						rest = strings.Trim(rest, "[]")
						for _, t := range strings.Split(rest, ",") {
							t = strings.Trim(t, " \"'")
							if t != "" {
								tags = append(tags, t)
							}
						}
					}
				}
			}
		}
	}

	if description == "" {
		content := raw
		if strings.HasPrefix(raw, "---") {
			parts := strings.SplitN(raw, "---", 3)
			if len(parts) >= 3 {
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

// LoadSummary returns XML summary of discovered skills. Implements SkillsLoader interface used by agent.
func (l *Loader) LoadSummary(ctx context.Context) (string, error) {
	if !l.loaded {
		if err := l.Discover(); err != nil {
			return "", err
		}
	}

	parts := []string{"Available skills (use read_file to load full skill content when needed):"}
	for _, sk := range l.skills {
		parts = append(parts, sk.ToSummaryXML())
		if sk.Always && sk.Available() {
			content := sk.GetContent()
			if content != "" {
				parts = append(parts, fmt.Sprintf("  <skill-content name=\"%s\">\n%s\n  </skill-content>", sk.Name, content))
			}
		}
	}
	return strings.Join(parts, "\n"), nil
}

// GetSkill returns a discovered skill by name (nil if not found)
func (l *Loader) GetSkill(name string) *Skill {
	if !l.loaded {
		_ = l.Discover()
	}
	return l.skills[name]
}
