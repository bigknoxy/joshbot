package output

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/redact"
)

// Preflight wraps config.PreflightReport for emission. The report type already
// carries json tags — it was designed to be machine-readable — so this adds
// only the schema version and the pass/fail bit a script actually branches on,
// rather than making every consumer re-derive OK() from the entries.
type Preflight struct {
	SchemaVersion int    `json:"schema_version"`
	OK            bool   `json:"ok"`
	ConfigError   string `json:"config_error,omitempty"`
	config.PreflightReport
}

// NewPreflight builds the document. configErr is the reason the config was
// rejected outright, empty when it loaded.
//
// Detail and configErr are the free-text fields of this report — a detail
// quotes a model name and a provider straight out of the operator's config, and
// a load error quotes whatever the parser choked on. Both are redacted here,
// while they are still Go strings, because the JSON form cannot be redacted
// after encoding without destroying it (see the package comment).
func NewPreflight(report config.PreflightReport, configErr string) Preflight {
	entries := make([]config.PreflightEntry, len(report.Entries))
	copy(entries, report.Entries)
	for i := range entries {
		entries[i].Detail = redact.String(entries[i].Detail)
	}
	report.Entries = entries

	return Preflight{
		SchemaVersion:   SchemaVersion,
		OK:              report.OK(),
		ConfigError:     redact.String(configErr),
		PreflightReport: report,
	}
}

// RenderPreflightText writes the human form of a preflight report, byte for
// byte as `joshbot preflight` printed it before --output existed.
func RenderPreflightText(w io.Writer, p Preflight) {
	fmt.Fprintf(w, "config:    %s\n", p.ConfigPath)
	fmt.Fprintf(w, "format:    %s\n", p.ConfigFormat)
	fmt.Fprintf(w, "workspace: %s\n", p.Workspace)
	if p.ConfigError != "" {
		fmt.Fprintf(w, "\nconfig rejected: %s\n", p.ConfigError)
	}

	if len(p.Entries) > 0 {
		fmt.Fprintln(w)
	}
	for _, e := range p.Entries {
		mark := "✓"
		if e.Problem != "" {
			mark = "✗"
		}
		fmt.Fprintf(w, "%s %s\n", mark, e.Summary())
		if e.Problem != "" {
			fmt.Fprintf(w, "    problem %s — %s\n", e.Problem, e.Detail)
		}
	}

	if p.OK {
		fmt.Fprintln(w, "\nOK — joshbot would start.")
		return
	}
	if _, detail := p.FirstProblem(); detail != "" {
		fmt.Fprintf(w, "\nNOT OK — %s\n", detail)
	}
}

// Skills is what `joshbot skills list` reports.
type Skills struct {
	SchemaVersion int     `json:"schema_version"`
	Skills        []Skill `json:"skills"`
	// Pending is the count of skills awaiting review, i.e. discovered but not
	// in use. A script gating a deploy on "no unapproved skills" reads this.
	Pending int `json:"pending"`
}

// Skill is one entry in the registry.
type Skill struct {
	Name string `json:"name"`
	// State is "bundled", "approved" or "awaiting_review".
	State string `json:"state"`
	// Path is the SKILL.md an operator would read before approving. Empty for
	// bundled skills, whose Path is an embed path inside the binary rather
	// than a file anyone can open.
	Path string `json:"path,omitempty"`
}

// Skill states, as they appear in the JSON contract.
const (
	SkillBundled  = "bundled"
	SkillApproved = "approved"
	SkillPending  = "awaiting_review"
)

// NewSkills builds the document, sorting by name so the output is stable.
func NewSkills(entries []Skill) Skills {
	sorted := append([]Skill(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	pending := 0
	for _, s := range sorted {
		if s.State == SkillPending {
			pending++
		}
	}
	return Skills{SchemaVersion: SchemaVersion, Skills: sorted, Pending: pending}
}

// RenderSkillsText writes the human form of the skill registry.
func RenderSkillsText(w io.Writer, s Skills) {
	if len(s.Skills) == 0 {
		fmt.Fprintln(w, "No skills found.")
		return
	}

	fmt.Fprintln(w, "Skills:")
	for _, sk := range s.Skills {
		switch sk.State {
		case SkillBundled:
			fmt.Fprintf(w, "  %-28s bundled\n", sk.Name)
		case SkillApproved:
			fmt.Fprintf(w, "  %-28s approved\n", sk.Name)
		default:
			fmt.Fprintf(w, "  %-28s AWAITING REVIEW  %s\n", sk.Name, sk.Path)
		}
	}

	if s.Pending > 0 {
		fmt.Fprintf(w, "\n%d skill(s) are not being used until you approve them.\n", s.Pending)
		fmt.Fprintln(w, "Read the file, then run: joshbot skills trust <name>")
		fmt.Fprintln(w, "A skill's text becomes part of the agent's instructions, so review it as you would a script you are about to run.")
	}
}

// SkillPath is the file an operator reads before approving a skill. Bundled
// skills return "" because their Path is an embed path, not a file on disk.
func SkillPath(dir string, bundled bool) string {
	if bundled {
		return ""
	}
	return filepath.Join(dir, "SKILL.md")
}

// Auth is what `joshbot auth status` reports. One entry per provider that has
// an auth flow rather than a plain api_key; today that is GitHub Copilot only,
// but the shape is a list so adding a second one is not a breaking change.
type Auth struct {
	SchemaVersion int              `json:"schema_version"`
	Providers     []AuthedProvider `json:"providers"`
}

// AuthedProvider reports presence of a credential, never its value.
type AuthedProvider struct {
	Name          string `json:"name"`
	Authenticated bool   `json:"authenticated"`
}

// RenderAuthText writes the human form of the auth status.
func RenderAuthText(w io.Writer, a Auth) {
	fmt.Fprintln(w, "Authentication Status:")
	fmt.Fprintln(w)
	for _, p := range a.Providers {
		fmt.Fprintf(w, "  %s: ", authDisplayName(p.Name))
		if p.Authenticated {
			fmt.Fprintln(w, "authenticated")
			continue
		}
		fmt.Fprintln(w, "not authenticated")
		fmt.Fprintf(w, "    Run 'joshbot auth %s' to authenticate\n", p.Name)
	}
}

// authDisplayName maps a provider key onto the label the text output uses.
func authDisplayName(name string) string {
	if name == "github-copilot" {
		return "GitHub Copilot"
	}
	return name
}

// Providers is what `joshbot configure --list` reports.
type Providers struct {
	SchemaVersion int                  `json:"schema_version"`
	Default       string               `json:"default"`
	Providers     []ConfiguredProvider `json:"providers"`
}

// ConfiguredProvider is one row of the configure --list table. (The status
// strings are part of the contract: "configured", "authenticated",
// "OAuth required", "not configured".)
type ConfiguredProvider struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	IsDefault bool   `json:"is_default"`
}

// Provider status values shared by both renderings.
const (
	ProviderConfigured    = "configured"
	ProviderAuthenticated = "authenticated"
	ProviderOAuthRequired = "OAuth required"
	ProviderNotConfigured = "not configured"
)

// RenderProvidersText writes the human form of `configure --list`.
func RenderProvidersText(w io.Writer, p Providers) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "╔═══════════════════════════════════════════╗")
	fmt.Fprintln(w, "║        Configured Providers              ║")
	fmt.Fprintln(w, "╚═══════════════════════════════════════════╝")
	fmt.Fprintln(w)

	for _, entry := range p.Providers {
		icon := "○"
		if entry.Status == ProviderConfigured || entry.Status == ProviderAuthenticated {
			icon = "✓"
		}
		text := entry.Status
		if entry.IsDefault {
			text += " (default)"
		}
		fmt.Fprintf(w, "  %s %-12s %s\n", icon, entry.Name, text)
	}

	fmt.Fprintln(w)
}
