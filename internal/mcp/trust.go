package mcp

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

// Provenance for MCP servers.
//
// An MCP server supplies tool *definitions* — names, descriptions and JSON
// schemas — and every one of them is written verbatim into the model's context
// and becomes callable. That is the same power a workspace SKILL.md has, so it
// gets the same gate: a configured server is inert until an operator approves
// what it advertises, and approval is bound to the manifest rather than to the
// server's name. A server that changes its tool list, renames a tool, or edits
// a description is no longer the thing that was approved, so it is revoked and
// must be re-read and re-approved.
//
// What this gate does NOT cover, and must not be mistaken for: joshbot still
// *executes* the configured command in order to ask it for a manifest. Running
// the binary is the operator's decision at the moment they write it into
// `mcp.servers.<name>.command`; the trust store governs what that process is
// allowed to put in front of the model, not whether it runs. Anyone who can
// edit the config can already run arbitrary commands, so a gate on execution
// would protect nothing while making the approval flow impossible — there is no
// way to show an operator a manifest without asking the server for one.

// TrustStore records which MCP tool manifests an operator has approved.
//
// It lives in the joshbot home, not the workspace, so a workspace-confined
// shell command cannot approve servers on its own behalf.
type TrustStore struct {
	mu      sync.Mutex
	path    string
	Entries map[string]string `json:"trusted_mcp_servers"`
}

// DefaultTrustStorePath returns the MCP trust file location for a joshbot home.
func DefaultTrustStorePath(joshbotHome string) string {
	return filepath.Join(joshbotHome, "mcp.trust")
}

// LoadTrustStore reads the trust file, returning an empty store if it does not
// exist yet. A malformed file is an error, not an empty store: an operator
// should be told their approvals are unreadable rather than quietly discover
// every server has gone inert.
func LoadTrustStore(path string) (*TrustStore, error) {
	s := &TrustStore{path: path, Entries: map[string]string{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read MCP trust store: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("MCP trust store %s is malformed: %w", path, err)
	}
	if s.Entries == nil {
		s.Entries = map[string]string{}
	}
	return s, nil
}

// HashManifest returns the digest a trust entry is bound to.
//
// It covers every advertised tool's name, description and input schema, sorted
// by name so the digest does not depend on the order the server happened to
// list them in. All three fields are folded in because all three reach the
// model: a description is prompt text, and a schema names the arguments the
// model is invited to fill. Changing any of them changes the digest.
//
// Each field is length-prefixed so that no two distinct manifests collide by
// concatenation — without it, a tool named "ab" described as "c" would hash
// identically to one named "a" described as "bc".
func HashManifest(tools []ToolInfo) string {
	sorted := append([]ToolInfo(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	h := sha256.New()
	for _, t := range sorted {
		fmt.Fprintf(h, "%d\x00%s\x00%d\x00%s\x00%d\x00",
			len(t.Name), t.Name,
			len(t.Description), t.Description,
			len(t.InputSchema))
		h.Write(t.InputSchema)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// IsTrusted reports whether this exact manifest has been approved for name.
func (s *TrustStore) IsTrusted(name string, tools []ToolInfo) bool {
	if s == nil {
		return false
	}
	digest := HashManifest(tools)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Entries[name] == digest
}

// Trust approves a server's current manifest and persists the decision.
func (s *TrustStore) Trust(name string, tools []ToolInfo) error {
	digest := HashManifest(tools)
	s.mu.Lock()
	s.Entries[name] = digest
	s.mu.Unlock()
	return s.save()
}

// Untrust revokes approval for a server.
func (s *TrustStore) Untrust(name string) error {
	s.mu.Lock()
	delete(s.Entries, name)
	s.mu.Unlock()
	return s.save()
}

// TrustedNames lists approved server names, sorted.
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

// save writes the trust file 0600 — it records what the agent may be told, and
// only its owner should be able to change it.
func (s *TrustStore) save() error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode MCP trust store: %w", err)
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create MCP trust store dir: %w", err)
		}
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write MCP trust store: %w", err)
	}
	return nil
}
