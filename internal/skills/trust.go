package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Provenance for workspace skills.
//
// A SKILL.md found in the workspace becomes part of the system prompt: its
// description always, and with always:true its entire body, on every request,
// permanently. Anything able to write to the workspace can therefore install
// standing instructions — including the agent's own skill_registry tool, which
// means text steered by a fetched page or a chat message can do it.
//
// So workspace skills are inert until an operator approves them, and approval
// is bound to the content rather than the name: editing an approved skill
// revokes it, closing the swap-the-body-after-approval route.
//
// Skills bundled with the release are exempt. They arrive with the binary,
// not through the workspace, and requiring per-install approval for them would
// train operators to approve things reflexively — which is how a trust gate
// stops being a trust gate.

// TrustStore records which skill contents an operator has approved.
//
// It deliberately lives outside the workspace (see DefaultTrustStorePath) so
// that a shell command confined to the workspace — or anything else with write
// access to it — cannot approve skills on its own behalf.
type TrustStore struct {
	mu      sync.Mutex
	path    string
	Entries map[string]string `json:"trusted_skills"`
}

// DefaultTrustStorePath returns the trust file location for a joshbot home.
func DefaultTrustStorePath(joshbotHome string) string {
	return filepath.Join(joshbotHome, "skills.trust")
}

// LoadTrustStore reads the trust file, returning an empty store if it does not
// exist yet. A malformed file is an error rather than an empty store: silently
// treating corruption as "nothing is approved" would be safe, but silently
// treating it as anything else would not, and an operator should know.
func LoadTrustStore(path string) (*TrustStore, error) {
	s := &TrustStore{path: path, Entries: map[string]string{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read trust store: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("trust store %s is malformed: %w", path, err)
	}
	if s.Entries == nil {
		s.Entries = map[string]string{}
	}
	return s, nil
}

// HashSkillFile returns the digest a trust entry is bound to.
//
// It covers the ENTIRE skill directory tree, not just SKILL.md: a skill's
// SKILL.md can tell the agent to run a sibling script, so binding trust to
// SKILL.md alone would let an attacker rewrite that script after approval and
// keep the skill trusted. The digest folds in every regular file's relative
// path and content, walked in sorted order so the result is deterministic
// regardless of filesystem iteration order. Editing, adding, or removing any
// file in the directory changes the digest and so revokes trust.
//
// The hash format changed in a way that is intentionally incompatible with the
// old SKILL.md-only digest: a store written by an older joshbot will simply
// fail to match, which revokes affected skills (they must be re-inspected and
// re-trusted) rather than crashing or silently staying approved.
func HashSkillFile(skillDir string) (string, error) {
	h := sha256.New()

	// Collect regular files with their directory-relative paths first, so the
	// digest does not depend on WalkDir's traversal order.
	var rels []string
	err := filepath.WalkDir(skillDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Only hash regular files. A symlink's target content is not part of
		// the skill, and following it could reach outside the directory.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(skillDir, path)
		if err != nil {
			return err
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(rels)

	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(skillDir, rel))
		if err != nil {
			return "", err
		}
		// Length-prefix path and content so no two distinct trees collide by
		// concatenation (e.g. "ab"+"c" vs "a"+"bc").
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// IsTrusted reports whether the skill's current content has been approved.
// A read failure is untrusted: we cannot approve what we cannot hash.
func (s *TrustStore) IsTrusted(name, skillDir string) bool {
	if s == nil {
		return false
	}
	digest, err := HashSkillFile(skillDir)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Entries[name] == digest
}

// Trust approves the skill's current content and persists the decision.
func (s *TrustStore) Trust(name, skillDir string) error {
	digest, err := HashSkillFile(skillDir)
	if err != nil {
		return fmt.Errorf("hash skill %q: %w", name, err)
	}
	s.mu.Lock()
	s.Entries[name] = digest
	s.mu.Unlock()
	return s.save()
}

// Untrust revokes approval for a skill.
func (s *TrustStore) Untrust(name string) error {
	s.mu.Lock()
	delete(s.Entries, name)
	s.mu.Unlock()
	return s.save()
}

// TrustedNames lists approved skill names, sorted.
func (s *TrustStore) TrustedNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.Entries))
	for name := range s.Entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// save writes the trust file 0600 — it is the record of what the agent is
// allowed to be told, and only its owner should be able to change it.
func (s *TrustStore) save() error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode trust store: %w", err)
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create trust store dir: %w", err)
		}
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write trust store: %w", err)
	}
	return nil
}
