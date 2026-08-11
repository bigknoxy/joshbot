package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		// An unset flag is the historical behaviour, not an error: every
		// existing invocation of these commands passes no --output at all.
		{in: "", want: Text},
		{in: "text", want: Text},
		{in: "json", want: JSON},
		{in: "JSON", wantErr: true},
		{in: "yaml", wantErr: true},
		{in: "stream-json", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseFormat(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseFormat(%q) accepted an unknown format", tc.in)
				continue
			}
			// The message has to say what was wrong and what is allowed;
			// a script author reads this and nothing else.
			if !strings.Contains(err.Error(), tc.in) || !strings.Contains(err.Error(), "json") {
				t.Errorf("ParseFormat(%q) error is unhelpful: %v", tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseFormat(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseFormat(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A consumer diffing two runs of `status --output json` must see a change only
// when something actually changed, so map iteration order must not reach the
// document. Every list field is sorted by its constructor for that reason.
func TestWriteJSONIsDeterministic(t *testing.T) {
	doc := Status{
		SchemaVersion: SchemaVersion,
		Providers: []ProviderStatus{
			{Name: "openrouter", Usable: true},
			{Name: "groq", Usable: false, Reason: ReasonNotEnabled},
		},
	}
	var first bytes.Buffer
	if err := WriteJSON(&first, doc); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	for i := 0; i < 20; i++ {
		var next bytes.Buffer
		if err := WriteJSON(&next, doc); err != nil {
			t.Fatalf("WriteJSON: %v", err)
		}
		if next.String() != first.String() {
			t.Fatalf("WriteJSON is not byte-stable across runs:\n%s\n---\n%s", first.String(), next.String())
		}
	}
}

// Go's encoder escapes <, > and & into \u003c-style sequences by default,
// which mangles model IDs and file paths for anything that is not itself a Go
// JSON reader. WriteJSON turns that off; this pins it.
func TestWriteJSONDoesNotEscapeHTML(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, Status{Model: "a<b>c&d"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if strings.Contains(buf.String(), `\u003c`) {
		t.Errorf("WriteJSON HTML-escaped the payload:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "a<b>c&d") {
		t.Errorf("WriteJSON did not emit the value verbatim:\n%s", buf.String())
	}
}

func TestWriteError(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteError(&buf, 3, errStub("bad --output value")); err != nil {
		t.Fatalf("WriteError: %v", err)
	}
	var doc ErrorDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("error document is not valid JSON: %v\n%s", err, buf.String())
	}
	if doc.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, SchemaVersion)
	}
	if doc.Error.Code != 3 {
		t.Errorf("code = %d, want 3", doc.Error.Code)
	}
	if doc.Error.Message != "bad --output value" {
		t.Errorf("message = %q", doc.Error.Message)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

// The text renderings are the pre-existing output of these commands. Pinning
// them byte for byte is the whole safety argument for routing them through a
// new package: --output text must remain indistinguishable from no flag.
func TestRenderStatusTextGolden(t *testing.T) {
	var buf bytes.Buffer
	RenderStatusText(&buf, Status{
		SchemaVersion:       SchemaVersion,
		Version:             "1.47.1",
		ConfigPath:          "~/.joshbot/config.json",
		ConfigExists:        true,
		Workspace:           "~/.joshbot/workspace",
		WorkspaceExists:     false,
		SessionsDir:         "~/.joshbot/sessions",
		ConfigFormat:        FormatLegacy,
		Model:               "openrouter/x",
		MaxTokens:           2048,
		Temperature:         0.7,
		MemoryWindow:        20,
		Providers:           []ProviderStatus{{Name: "groq", Usable: false, Reason: ReasonNoAPIKey}},
		TelegramEnabled:     true,
		WorkspaceRestricted: false,
		PendingSkills:       []string{"deploy"},
		MemoryBytes:         12,
		HistoryBytes:        34,
	})

	want := "\n" +
		"╔═══════════════════════════════════════════╗\n" +
		"║            joshbot status                ║\n" +
		"╚═══════════════════════════════════════════╝\n" +
		"Version:        1.47.1\n" +
		"Config file:    ~/.joshbot/config.json (exists)\n" +
		"Workspace:      ~/.joshbot/workspace (missing)\n" +
		"Sessions:       ~/.joshbot/sessions\n" +
		"\n" +
		"Model:          openrouter/x\n" +
		"Max tokens:     2048\n" +
		"Temperature:    0.7\n" +
		"Memory window:  20\n" +
		"\n" +
		"Providers:      groq (disabled — missing \"api_key\")\n" +
		"Telegram:       enabled\n" +
		"Workspace restricted: disabled\n" +
		"Skills:         1 awaiting review (deploy)\n" +
		"                not in use — review then run: joshbot skills trust <name>\n" +
		"\n" +
		"MEMORY.md:  12 bytes\n" +
		"HISTORY.md: 34 bytes\n"

	if buf.String() != want {
		t.Errorf("status text changed:\n got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestRenderStatusTextModelCentric(t *testing.T) {
	var buf bytes.Buffer
	RenderStatusText(&buf, Status{
		ConfigFormat: FormatModelCentric,
		Model:        "openrouter/x",
		Fallback:     []string{"groq/y", "ollama/z"},
		Models:       []string{"openrouter/x", "groq/y"},
	})
	got := buf.String()
	for _, want := range []string{
		"Config format:  model-centric\n",
		"Active model:   openrouter/x\n",
		"Fallback:       groq/y, ollama/z\n",
		"Models:         openrouter/x, groq/y\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// The legacy provider line belongs to the other format only.
	if strings.Contains(got, "Providers:") {
		t.Errorf("model-centric status printed the legacy provider line:\n%s", got)
	}
}

// "none" rather than an empty line: an operator whose providers map is empty
// needs to see that the field was read and was empty.
func TestFormatProvidersEmpty(t *testing.T) {
	if got := FormatProviders(nil); got != "none" {
		t.Errorf("FormatProviders(nil) = %q, want %q", got, "none")
	}
}

func TestRenderPreflightTextGolden(t *testing.T) {
	var buf bytes.Buffer
	RenderPreflightText(&buf, Preflight{
		SchemaVersion: SchemaVersion,
		OK:            true,
		PreflightReport: config.PreflightReport{
			ConfigPath:   "/c/config.json",
			ConfigFormat: "legacy",
			Workspace:    "/w",
		},
	})
	want := "config:    /c/config.json\n" +
		"format:    legacy\n" +
		"workspace: /w\n" +
		"\nOK — joshbot would start.\n"
	if buf.String() != want {
		t.Errorf("preflight text changed:\n got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestRenderSkillsText(t *testing.T) {
	doc := NewSkills([]Skill{
		{Name: "zeta", State: SkillPending, Path: "/w/skills/zeta/SKILL.md"},
		{Name: "alpha", State: SkillBundled},
		{Name: "mid", State: SkillApproved},
	})
	if doc.Pending != 1 {
		t.Errorf("Pending = %d, want 1", doc.Pending)
	}
	// Sorted, so a script diffing two runs sees only real changes.
	if doc.Skills[0].Name != "alpha" || doc.Skills[2].Name != "zeta" {
		t.Errorf("NewSkills did not sort: %+v", doc.Skills)
	}

	var buf bytes.Buffer
	RenderSkillsText(&buf, doc)
	got := buf.String()
	for _, want := range []string{
		"Skills:\n",
		"alpha                        bundled\n",
		"mid                          approved\n",
		"zeta                         AWAITING REVIEW  /w/skills/zeta/SKILL.md\n",
		"\n1 skill(s) are not being used until you approve them.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

// The approval nudge is the point of the pending footer; a registry with
// nothing pending must not print it, or it becomes noise operators learn to
// ignore.
func TestRenderSkillsTextNoPendingFooter(t *testing.T) {
	var buf bytes.Buffer
	RenderSkillsText(&buf, NewSkills([]Skill{{Name: "alpha", State: SkillBundled}}))
	if strings.Contains(buf.String(), "awaiting") || strings.Contains(buf.String(), "approve them") {
		t.Errorf("printed the pending footer with nothing pending:\n%s", buf.String())
	}
}

func TestRenderSkillsTextEmpty(t *testing.T) {
	var buf bytes.Buffer
	RenderSkillsText(&buf, NewSkills(nil))
	if buf.String() != "No skills found.\n" {
		t.Errorf("empty registry text = %q", buf.String())
	}
}

func TestRenderAuthText(t *testing.T) {
	var buf bytes.Buffer
	RenderAuthText(&buf, Auth{Providers: []AuthedProvider{{Name: "github-copilot"}}})
	got := buf.String()
	if !strings.Contains(got, "GitHub Copilot: not authenticated\n") {
		t.Errorf("auth text: %s", got)
	}
	// Unauthenticated is only useful with the command that fixes it.
	if !strings.Contains(got, "Run 'joshbot auth github-copilot' to authenticate") {
		t.Errorf("auth text omits the remediation:\n%s", got)
	}
}

func TestRenderProvidersText(t *testing.T) {
	var buf bytes.Buffer
	RenderProvidersText(&buf, Providers{
		Default: "groq",
		Providers: []ConfiguredProvider{
			{Name: "groq", Status: ProviderConfigured, IsDefault: true},
			{Name: "nvidia", Status: ProviderNotConfigured},
		},
	})
	got := buf.String()
	if !strings.Contains(got, "✓ groq         configured (default)\n") {
		t.Errorf("configured default row missing:\n%s", got)
	}
	if !strings.Contains(got, "○ nvidia       not configured\n") {
		t.Errorf("unconfigured row missing:\n%s", got)
	}
}
