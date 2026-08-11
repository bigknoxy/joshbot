package skills

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Skill represents a discovered skill.
type Skill struct {
	Name        string
	Description string
	Path        string
	// Always when true injects full skill content into the system prompt at session start.
	Always       bool
	Requirements []string
	Tags         []string
	// Bundled marks a skill that shipped with the release rather than being
	// found in the workspace. Bundled skills need no per-install approval.
	Bundled bool
	// Trusted reports whether this exact content has been approved. Untrusted
	// workspace skills are never offered to the model — not even their name,
	// which is attacker-controlled text like everything else in the file.
	Trusted   bool
	content   *string
	available *bool
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
	// bundledDir is empty in production: the bundled set is embedded in the
	// binary (see bundled.go). It exists so tests can point discovery at a
	// fixture directory. It was previously the relative path "skills", which
	// resolved against the working directory and so found nothing unless
	// joshbot was run from its own source tree.
	bundledDir   string
	workspaceDir string
	skills       map[string]*Skill
	loaded       bool
	// trust gates workspace skills. A nil store means nothing from the
	// workspace is approved, which is the safe direction to fail.
	trust *TrustStore
}

// NewLoader creates a new skills loader. workspace should be the workspace root (contains skills/).
func NewLoader(workspace string) (*Loader, error) {
	ws := filepath.Join(workspace, "skills")
	l := &Loader{
		workspaceDir: ws,
		skills:       map[string]*Skill{},
	}
	return l, nil
}

// SetTrustStore attaches the approval record used to gate workspace skills.
// Without one, no workspace skill is trusted — failing closed, because the
// alternative is treating "provenance not configured" as "everything is fine".
func (l *Loader) SetTrustStore(store *TrustStore) {
	l.trust = store
	l.Invalidate()
}

// TrustStore returns the attached approval record, which may be nil.
func (l *Loader) TrustStore() *TrustStore { return l.trust }

// Discover scans bundled and workspace skills. Workspace overrides bundled.
func (l *Loader) Discover() error {
	l.skills = map[string]*Skill{}

	l.discoverBundled()
	if l.bundledDir != "" {
		// A directory override, used by tests to stand in a fixture set for
		// the embedded one.
		_ = filepath.WalkDir(l.bundledDir, l.walk(true))
	}
	_ = filepath.WalkDir(l.workspaceDir, l.walk(false))

	l.loaded = true
	return nil
}

// Trust approves a discovered workspace skill's current content.
//
// This is an operator action. Nothing the agent can reach calls it: the
// skill_registry tool writes skills but never approves them, so whatever
// induced the agent to write a skill has not also caused it to be believed.
func (l *Loader) Trust(name string) error {
	if l.trust == nil {
		return fmt.Errorf("no trust store configured; cannot approve skill %q", name)
	}
	sk := l.GetSkill(name)
	if sk == nil {
		return fmt.Errorf("skill %q not found", name)
	}
	if sk.Bundled {
		return fmt.Errorf("skill %q ships with joshbot and does not need approval", name)
	}
	if err := l.trust.Trust(name, sk.Path); err != nil {
		return err
	}
	l.Invalidate()
	return nil
}

// Untrust revokes approval, making the skill inert again.
func (l *Loader) Untrust(name string) error {
	if l.trust == nil {
		return fmt.Errorf("no trust store configured")
	}
	if err := l.trust.Untrust(name); err != nil {
		return err
	}
	l.Invalidate()
	return nil
}

// Untrusted lists discovered workspace skills awaiting approval, sorted by
// name. These are withheld from the model but must stay visible to the
// operator, or there is no way to approve them.
func (l *Loader) Untrusted() []*Skill {
	if !l.loaded {
		_ = l.Discover()
	}
	var out []*Skill
	for _, sk := range l.skills {
		if !sk.Trusted {
			out = append(out, sk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// walk returns the filepath.WalkDir callback that registers discovered
// skills, recording whether they came from the bundled set or the workspace.
func (l *Loader) walk(bundled bool) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		skillFile := filepath.Join(path, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			return nil
		}
		name := filepath.Base(path)
		sk := l.parseSkill(path, name)
		if sk == nil {
			return nil
		}
		sk.Bundled = bundled
		// Bundled skills arrive with the binary. Workspace skills are content
		// of unknown origin until an operator says otherwise.
		sk.Trusted = bundled || l.trust.IsTrusted(sk.Name, sk.Path)
		l.skills[sk.Name] = sk
		return nil
	}
}

func (l *Loader) parseSkill(dir, defaultName string) *Skill {
	p := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	sk := parseSkillContent(string(data), defaultName)
	if sk == nil {
		return nil
	}
	sk.Path = dir
	return sk
}

// parseSkillContent parses a SKILL.md body. It is split out from parseSkill so
// the embedded bundled set, which has no file to read, shares exactly the same
// frontmatter handling as a workspace skill on disk.
func parseSkillContent(raw, defaultName string) *Skill {
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
// Skills with Always=true have their full content injected inline.
func (l *Loader) LoadSummary(ctx context.Context) (string, error) {
	if !l.loaded {
		if err := l.Discover(); err != nil {
			return "", err
		}
	}

	parts := []string{"Available skills (use read_file to load full skill content when needed):"}
	for _, sk := range l.skills {
		// Untrusted workspace skills are withheld entirely. Offering only the
		// name and description would still put attacker-chosen text in the
		// prompt, and would advertise a skill the model could then be talked
		// into loading in full.
		if !sk.Trusted {
			continue
		}
		parts = append(parts, sk.ToSummaryXML())
		if sk.Always {
			if content := sk.GetContent(); content != "" {
				parts = append(parts, fmt.Sprintf("  <skill-content name=\"%s\">\n%s\n  </skill-content>", sk.Name, content))
			}
		}
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
	// The summary withholds untrusted skills, but this is reachable by name
	// from elsewhere; an unapproved skill must not be loadable by guessing it.
	if !sk.Trusted {
		return "", fmt.Errorf("skill %q has not been approved; run 'joshbot skills trust %s' after reviewing it", skillName, skillName)
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
	if sk.Bundled {
		// There is nothing on disk to remove, and the path is an embed path.
		// Before the bundled set was embedded this would have handed a
		// relative source-tree path to os.RemoveAll.
		return fmt.Errorf("skill %q ships with joshbot and cannot be deleted", name)
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
