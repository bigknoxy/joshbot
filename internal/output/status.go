package output

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Status is everything `joshbot status` reports. The struct is the single
// source for both renderings: RenderStatusText reproduces the historical
// output byte for byte, and the json tags define the machine contract.
//
// Nothing here is a credential. The provider entries carry names and a reason
// a provider is inert, never an api_key — see TestStatusJSONHasNoCredential.
type Status struct {
	SchemaVersion int `json:"schema_version"`

	Version         string `json:"version"`
	ConfigPath      string `json:"config_path"`
	ConfigExists    bool   `json:"config_exists"`
	Workspace       string `json:"workspace"`
	WorkspaceExists bool   `json:"workspace_exists"`
	SessionsDir     string `json:"sessions_dir"`

	// ConfigFormat is "model-centric" or "legacy". The two formats report
	// different things below, which is why the field is in the document
	// rather than left for the consumer to infer from which fields are set.
	ConfigFormat string `json:"config_format"`

	Model    string   `json:"model"`
	Fallback []string `json:"fallback,omitempty"`
	// Models is the enabled model list under the model-centric format, empty
	// under the legacy one.
	Models []string `json:"models,omitempty"`
	// Providers is the legacy provider list, empty under the model-centric
	// format. Sorted by name so the document is byte-stable across runs.
	Providers []ProviderStatus `json:"providers,omitempty"`

	MaxTokens    int     `json:"max_tokens"`
	Temperature  float64 `json:"temperature"`
	MemoryWindow int     `json:"memory_window"`

	TelegramEnabled     bool `json:"telegram_enabled"`
	WorkspaceRestricted bool `json:"workspace_restricted"`

	// PendingSkills are workspace skills that are discovered but not trusted,
	// so they are NOT in use. Reported here because in gateway mode the
	// equivalent startup log goes to the journal where nobody reads it.
	PendingSkills []string `json:"pending_skills,omitempty"`

	MemoryBytes  int64 `json:"memory_bytes"`
	HistoryBytes int64 `json:"history_bytes"`
}

// ProviderStatus is one legacy provider and why it will or will not register.
type ProviderStatus struct {
	Name string `json:"name"`
	// Usable mirrors the registration gates in setupComponents: a provider
	// that is not usable is inert at runtime no matter what the config says.
	Usable bool `json:"usable"`
	// Reason is empty when Usable, otherwise the specific fault.
	Reason string `json:"reason,omitempty"`
}

// providerNote is the parenthesised text `status` has always printed after an
// unusable provider's name. Kept next to the struct so the text renderer and
// the JSON reason can never drift.
func providerNote(reason string) string {
	return fmt.Sprintf(" (disabled — %s)", reason)
}

// Reasons a provider fails to register. These strings are part of both the
// text output and the JSON contract.
const (
	ReasonNotEnabled = `set "enabled": true`
	ReasonNoAPIKey   = `missing "api_key"`
)

// FormatProviders renders the legacy provider list the way `status` always
// has: names joined by ", ", each unusable one carrying its reason, and the
// literal "none" when there are no providers at all.
func FormatProviders(ps []ProviderStatus) string {
	if len(ps) == 0 {
		return "none"
	}
	sorted := append([]ProviderStatus(nil), ps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	parts := make([]string, 0, len(sorted))
	for _, p := range sorted {
		if p.Usable {
			parts = append(parts, p.Name)
			continue
		}
		parts = append(parts, p.Name+providerNote(p.Reason))
	}
	return strings.Join(parts, ", ")
}

// RenderStatusText writes the human form. Every byte of this — including the
// box drawing, the column padding and the blank lines — is what the command
// printed before --output existed, and golden tests hold it there.
func RenderStatusText(w io.Writer, s Status) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "╔═══════════════════════════════════════════╗")
	fmt.Fprintln(w, "║            joshbot status                ║")
	fmt.Fprintln(w, "╚═══════════════════════════════════════════╝")
	fmt.Fprintf(w, "Version:        %s\n", s.Version)
	fmt.Fprintf(w, "Config file:    %s %s\n", s.ConfigPath, existsMarker(s.ConfigExists))
	fmt.Fprintf(w, "Workspace:      %s %s\n", s.Workspace, existsMarker(s.WorkspaceExists))
	fmt.Fprintf(w, "Sessions:       %s\n", s.SessionsDir)
	fmt.Fprintln(w)

	if s.ConfigFormat == FormatModelCentric {
		fmt.Fprintln(w, "Config format:  model-centric")
		fmt.Fprintf(w, "Active model:   %s\n", s.Model)
		if len(s.Fallback) > 0 {
			fmt.Fprintf(w, "Fallback:       %s\n", strings.Join(s.Fallback, ", "))
		}
	} else {
		fmt.Fprintf(w, "Model:          %s\n", s.Model)
	}
	fmt.Fprintf(w, "Max tokens:     %d\n", s.MaxTokens)
	fmt.Fprintf(w, "Temperature:    %.1f\n", s.Temperature)
	fmt.Fprintf(w, "Memory window:  %d\n", s.MemoryWindow)
	fmt.Fprintln(w)

	if s.ConfigFormat == FormatModelCentric {
		names := s.Models
		if len(names) == 0 {
			names = []string{"none"}
		}
		fmt.Fprintf(w, "Models:         %s\n", strings.Join(names, ", "))
	} else {
		fmt.Fprintf(w, "Providers:      %s\n", FormatProviders(s.Providers))
	}
	fmt.Fprintf(w, "Telegram:       %s\n", enabledWord(s.TelegramEnabled))
	fmt.Fprintf(w, "Workspace restricted: %s\n", enabledWord(s.WorkspaceRestricted))

	if n := len(s.PendingSkills); n > 0 {
		fmt.Fprintf(w, "Skills:         %d awaiting review (%s)\n", n, strings.Join(s.PendingSkills, ", "))
		fmt.Fprintln(w, "                not in use — review then run: joshbot skills trust <name>")
	}
	fmt.Fprintln(w)

	if s.MemoryBytes > 0 || s.HistoryBytes > 0 {
		fmt.Fprintf(w, "MEMORY.md:  %d bytes\n", s.MemoryBytes)
		fmt.Fprintf(w, "HISTORY.md: %d bytes\n", s.HistoryBytes)
	}
}

// The two config format names, as they appear in both renderings.
const (
	FormatModelCentric = "model-centric"
	FormatLegacy       = "legacy"
)

func existsMarker(b bool) string {
	if b {
		return "(exists)"
	}
	return "(missing)"
}

func enabledWord(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}
