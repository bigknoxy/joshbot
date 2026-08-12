package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// The whole reason NewPreflight redacts before encoding: the JSON form bypasses
// the redacting writer, so anything free-text in the report — an entry Detail
// or a config load error — is the one path a credential can reach a document
// that exists to be pasted into an issue.
func TestNewPreflightRedactsFreeTextAndHomePaths(t *testing.T) {
	t.Setenv("HOME", "/Users/operator")

	const key = "sk-or-v1-abcdefghijklmnopqrstuvwxyz012345"
	report := config.PreflightReport{
		ConfigPath:   "/Users/operator/.joshbot/config.json",
		Workspace:    "/Users/operator/joshbot-workspace",
		ConfigFormat: "model-centric",
		Entries: []config.PreflightEntry{
			{Name: "gpt", Role: "active", Problem: config.ProblemNoCredential,
				Detail: "provider rejected api_key=" + key},
		},
	}
	p := NewPreflight(report, "parse error near authorization: Bearer "+key)

	buf := &bytes.Buffer{}
	if err := WriteJSON(buf, p); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	out := buf.String()

	// Must still be a document a script can parse — redaction that produces
	// invalid JSON is the bug the package comment describes.
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("preflight JSON does not parse: %v\n%s", err, out)
	}
	if strings.Contains(out, key) {
		t.Errorf("credential reached the preflight document:\n%s", out)
	}
	// The scheme is kept and the token redacted, never the reverse.
	if !strings.Contains(out, "Bearer [REDACTED]") {
		t.Errorf("Authorization scheme not preserved with redacted token:\n%s", out)
	}
	if strings.Contains(out, "/Users/operator") {
		t.Errorf("operator home path reached the preflight document:\n%s", out)
	}
	if !strings.Contains(out, "~/.joshbot/config.json") || !strings.Contains(out, "~/joshbot-workspace") {
		t.Errorf("home paths not stripped to ~:\n%s", out)
	}
	if back["ok"] != false {
		t.Errorf("ok = %v, want false for a report with a problem entry", back["ok"])
	}

	// NewPreflight must copy the entries rather than rewrite the caller's
	// slice: the same report is also rendered as text, from the caller's copy.
	if got := report.Entries[0].Detail; !strings.Contains(got, key) {
		t.Errorf("NewPreflight mutated the caller's entries: %q", got)
	}
}

func TestRenderPreflightTextConfigRejected(t *testing.T) {
	buf := &bytes.Buffer{}
	RenderPreflightText(buf, Preflight{
		ConfigError: "invalid character '}' looking for beginning of value",
		PreflightReport: config.PreflightReport{
			ConfigPath:   "~/.joshbot/config.json",
			ConfigFormat: "unknown",
			Workspace:    "~/joshbot-workspace",
		},
	})
	out := buf.String()
	if !strings.Contains(out, "config rejected: invalid character") {
		t.Errorf("a rejected config must say so:\n%s", out)
	}
	// No entries, not OK, no FirstProblem detail: the renderer must not claim
	// joshbot would start.
	if strings.Contains(out, "OK — joshbot would start") {
		t.Errorf("rejected config reported as OK:\n%s", out)
	}
}

func TestRenderPreflightTextMarksFailedEntriesAndReportsFirstProblem(t *testing.T) {
	buf := &bytes.Buffer{}
	RenderPreflightText(buf, Preflight{
		PreflightReport: config.PreflightReport{
			ConfigPath:   "~/.joshbot/config.json",
			ConfigFormat: "legacy",
			Workspace:    "~/ws",
			Entries: []config.PreflightEntry{
				{Name: "fb", Role: "fallback", CredentialSource: "config",
					Problem: config.ProblemNotEnabled, Detail: "fallback is off"},
				{Name: "act", Role: "active", CredentialSource: "config",
					Problem: config.ProblemNoCredential, Detail: "no api_key for act"},
			},
		},
	})
	out := buf.String()
	if strings.Count(out, "✗ ") != 2 {
		t.Errorf("both failing entries must be marked ✗:\n%s", out)
	}
	if !strings.Contains(out, "problem missing-credential — no api_key for act") {
		t.Errorf("entry problem line missing:\n%s", out)
	}
	// FirstProblem prefers the active route even though it is listed second;
	// the summary line is what the operator acts on.
	if !strings.Contains(out, "NOT OK — no api_key for act") {
		t.Errorf("summary must name the active route's problem:\n%s", out)
	}
}

func TestRenderPreflightTextOKPath(t *testing.T) {
	buf := &bytes.Buffer{}
	RenderPreflightText(buf, Preflight{
		OK: true,
		PreflightReport: config.PreflightReport{
			ConfigPath: "~/c.json", ConfigFormat: "model-centric", Workspace: "~/ws",
			Entries: []config.PreflightEntry{{Name: "m", Role: "active", CredentialSource: "config"}},
		},
	})
	out := buf.String()
	if !strings.Contains(out, "✓ m (active)") {
		t.Errorf("healthy entry must be marked ✓:\n%s", out)
	}
	if !strings.HasSuffix(out, "OK — joshbot would start.\n") {
		t.Errorf("OK report must end with the start line:\n%q", out)
	}
	if strings.Contains(out, "NOT OK") {
		t.Errorf("OK report must not also print NOT OK:\n%s", out)
	}
}

// A bundled skill's Path is an embed path inside the binary, not a file. The
// listing must not hand the operator a path they cannot open.
func TestSkillPath(t *testing.T) {
	if got := SkillPath("/ws/skills/foo", true); got != "" {
		t.Errorf("bundled skill path = %q, want empty", got)
	}
	if got := SkillPath("/ws/skills/foo", false); got != "/ws/skills/foo/SKILL.md" {
		t.Errorf("workspace skill path = %q", got)
	}
}

func TestAuthDisplayNameFallsBackToTheKey(t *testing.T) {
	buf := &bytes.Buffer{}
	RenderAuthText(buf, Auth{Providers: []AuthedProvider{{Name: "future-provider"}}})
	out := buf.String()
	if !strings.Contains(out, "future-provider: not authenticated") {
		t.Errorf("unknown provider must be listed under its key:\n%s", out)
	}
	// The remediation must name the key the CLI accepts, not a display label.
	if !strings.Contains(out, "joshbot auth future-provider") {
		t.Errorf("remediation must use the provider key:\n%s", out)
	}
}

// "no MCP servers configured" is the ordinary first-run state; it must encode
// as [] so a consumer iterating servers does not break on null.
func TestNewMCPServersEmptyEncodesAsList(t *testing.T) {
	buf := &bytes.Buffer{}
	if err := WriteJSON(buf, NewMCPServers(nil)); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"servers": []`) {
		t.Errorf("empty server list must encode as []:\n%s", buf.String())
	}
}

func TestNewMCPServersSortsAndCountsPending(t *testing.T) {
	doc := NewMCPServers([]MCPServer{
		{Name: "zed", State: MCPPending},
		{Name: "alpha", State: MCPApproved},
		{Name: "mid", State: MCPPending},
		{Name: "off", State: MCPDisabled},
	})
	var names []string
	for _, s := range doc.Servers {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "alpha,mid,off,zed" {
		t.Errorf("servers not sorted by name: %v", names)
	}
	// Only pending counts — approved, disabled and unreachable are not
	// "awaiting your approval", and a deploy gate reads this number.
	if doc.Pending != 2 {
		t.Errorf("Pending = %d, want 2", doc.Pending)
	}
}

func TestRenderMCPServersTextPerState(t *testing.T) {
	buf := &bytes.Buffer{}
	RenderMCPServersText(buf, MCPServers{})
	if buf.String() != "No MCP servers configured.\n" {
		t.Errorf("empty listing = %q", buf.String())
	}

	long := strings.Repeat("x", 100)
	buf.Reset()
	RenderMCPServersText(buf, NewMCPServers([]MCPServer{
		{Name: "ok", State: MCPApproved, Tools: []MCPTool{{Name: "read", Description: "reads\nsecond line"}}},
		{Name: "new", State: MCPPending, Tools: []MCPTool{{Name: "wipe", Description: long}}},
		{Name: "dis", State: MCPDisabled},
		{Name: "dead", State: MCPUnreachable, Error: "exec: no such file"},
	}))
	out := buf.String()
	for _, want := range []string{
		"ok                           approved         1 tool(s)",
		"new                          AWAITING REVIEW  1 tool(s)",
		"dis                          disabled",
		"dead                         UNREACHABLE      exec: no such file",
		"1 MCP server(s) are not being used until you approve them.",
		"joshbot mcp trust <name>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The point of listing a pending server is that the operator can read what
	// it advertises before approving it.
	if !strings.Contains(out, "wipe") {
		t.Errorf("pending server's tool names must be shown:\n%s", out)
	}
	// firstLine: one line only, truncated so the listing stays scannable.
	if strings.Contains(out, "second line") {
		t.Errorf("description must be cut at the first newline:\n%s", out)
	}
	if !strings.Contains(out, strings.Repeat("x", 72)+"...") || strings.Contains(out, strings.Repeat("x", 73)) {
		t.Errorf("long description not truncated to 72 chars:\n%s", out)
	}
}

func TestRenderMCPServersTextNoPendingFooter(t *testing.T) {
	buf := &bytes.Buffer{}
	RenderMCPServersText(buf, NewMCPServers([]MCPServer{{Name: "ok", State: MCPApproved}}))
	if strings.Contains(buf.String(), "not being used") {
		t.Errorf("approved-only listing must not nag about approval:\n%s", buf.String())
	}
}

// The profile document names the credential's environment variable and reports
// only whether it is set — never its value.
func TestNewProfilesCarriesNoCredential(t *testing.T) {
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	doc := NewProfiles([]Profile{
		{Name: "zeta", Provider: "openai", Model: "gpt", CredentialEnv: "OPENAI_API_KEY", CredentialSet: true},
		{Name: "alpha", Provider: "anthropic", Model: "sonnet",
			Description: "work profile, api_key=" + key},
	}, "alpha")

	if doc.Profiles[0].Name != "alpha" || doc.Profiles[1].Name != "zeta" {
		t.Fatalf("profiles not sorted by name: %+v", doc.Profiles)
	}
	buf := &bytes.Buffer{}
	if err := WriteJSON(buf, doc); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	out := buf.String()
	if err := json.Unmarshal(buf.Bytes(), &map[string]any{}); err != nil {
		t.Fatalf("profiles JSON does not parse: %v\n%s", err, out)
	}
	if strings.Contains(out, key) {
		t.Errorf("credential reached the profiles document:\n%s", out)
	}
	// The variable *name* must survive — it is the thing the listing exists to
	// show, and redacting it would make the output useless.
	if !strings.Contains(out, `"credential_env": "OPENAI_API_KEY"`) {
		t.Errorf("credential env var name must be reported verbatim:\n%s", out)
	}
	if !strings.Contains(out, `"default_profile": "alpha"`) {
		t.Errorf("default profile missing:\n%s", out)
	}
}

func TestRenderProfilesTextEmpty(t *testing.T) {
	buf := &bytes.Buffer{}
	RenderProfilesText(buf, NewProfiles(nil, ""))
	out := buf.String()
	if !strings.Contains(out, "No profiles configured.") || !strings.Contains(out, `"profiles" block`) {
		t.Errorf("empty listing must explain how to add one:\n%s", out)
	}
}

func TestRenderProfilesTextMarkersAndCredentialLines(t *testing.T) {
	buf := &bytes.Buffer{}
	RenderProfilesText(buf, NewProfiles([]Profile{
		{Name: "act", Provider: "openai", Model: "gpt-4", Endpoint: "api.openai.com",
			CredentialEnv: "OPENAI_API_KEY", CredentialSet: true, Active: true,
			Description: "the one in use\nsecond line"},
		{Name: "def", Provider: "groq", Model: "llama", Default: true,
			CredentialEnv: "GROQ_API_KEY"},
		{Name: "loc", Provider: "ollama", Model: "qwen", Disabled: true},
	}, "def"))
	out := buf.String()
	for _, want := range []string{
		" * act                  gpt-4",
		" . def                  llama",
		"   loc                  qwen  (disabled)",
		"      provider openai   endpoint api.openai.com",
		"      provider ollama   endpoint provider default",
		"      credential from $OPENAI_API_KEY (set)",
		"      credential from $GROQ_API_KEY (NOT SET)",
		"      credential not required",
		"\nDefault profile: def\n",
		"joshbot agent --profile <name>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The credential lines are deliberately phrased without a "name: value"
	// separator, because the text form goes through the redacting writer and an
	// assignment shape would blank the variable name this line exists to show.
	if strings.Contains(out, "credential:") || strings.Contains(out, "credential =") {
		t.Errorf("credential line must not be assignment-shaped:\n%s", out)
	}
	if strings.Contains(out, "second line") {
		t.Errorf("description must be cut at the first newline:\n%s", out)
	}
}

func TestFormatProvidersNamesTheReason(t *testing.T) {
	got := FormatProviders([]ProviderStatus{
		{Name: "openrouter", Usable: true},
		{Name: "groq", Usable: false, Reason: ReasonNotEnabled},
		{Name: "azure", Usable: false, Reason: ReasonNoAPIKey},
	})
	want := `azure (disabled — missing "api_key"), groq (disabled — set "enabled": true), openrouter`
	if got != want {
		t.Errorf("FormatProviders =\n%q\nwant\n%q", got, want)
	}
}

// FormatProviders must not reorder the caller's slice: `status` renders the
// text form and the JSON document from the same Status value.
func TestFormatProvidersDoesNotMutateCaller(t *testing.T) {
	ps := []ProviderStatus{{Name: "zed", Usable: true}, {Name: "alpha", Usable: true}}
	FormatProviders(ps)
	if ps[0].Name != "zed" {
		t.Errorf("FormatProviders sorted the caller's slice in place: %+v", ps)
	}
}
