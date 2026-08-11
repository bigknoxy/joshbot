package skills

import (
	"embed"
	"io/fs"
	"path"
	"strings"
)

// bundledFS carries the skills that ship with the release.
//
// They are embedded rather than read from disk because a released joshbot is a
// single binary with nothing beside it. The loader used to look for a relative
// "skills" directory, which resolved against the process's working directory —
// so the bundled set loaded only when joshbot happened to be run from a checkout
// of its own source tree, and every real installation reported "No skills
// found". Embedding is also what makes the claim in trust.go true: bundled
// skills are exempt from the approval gate because they arrive with the binary,
// which was not the case while they were a directory anyone could edit.
//
//go:embed all:bundled
var bundledFS embed.FS

// bundledRoot is the directory inside bundledFS holding one directory per skill.
const bundledRoot = "bundled"

// discoverBundled registers every skill embedded in the binary.
//
// It reads content eagerly, unlike the workspace walk: there is no file to go
// back to later, and the whole bundled set is a few kilobytes.
func (l *Loader) discoverBundled() {
	entries, err := fs.ReadDir(bundledFS, bundledRoot)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := path.Join(bundledRoot, entry.Name())
		data, err := fs.ReadFile(bundledFS, path.Join(dir, "SKILL.md"))
		if err != nil {
			continue // a directory without a SKILL.md is not a skill
		}

		sk := parseSkillContent(string(data), entry.Name())
		if sk == nil {
			continue
		}
		sk.Path = dir
		sk.Bundled = true
		// Bundled skills arrive with the binary, so there is nothing for an
		// operator to approve — see the reasoning in trust.go.
		sk.Trusted = true
		body := skillBody(string(data))
		sk.content = &body

		l.skills[sk.Name] = sk
	}
}

// skillBody returns a SKILL.md's content with the YAML frontmatter removed.
func skillBody(raw string) string {
	if !strings.HasPrefix(raw, "---") {
		return strings.TrimSpace(raw)
	}
	parts := strings.SplitN(raw, "---", 3)
	if len(parts) < 3 {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(parts[2])
}
