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
func HashSkillFile(skillDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
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
