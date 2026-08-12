package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	joshlog "github.com/bigknoxy/joshbot/internal/log"
)

// A workspace skill is inert until `joshbot skills trust <name>` approves it,
// and trust is bound to a digest of the whole skill directory — so editing any
// file in a trusted skill silently revokes it. That is the right default, but
// the operator has no other signal: the assistant simply stops doing what the
// skill told it to. Startup has to name the withheld skills and say what to
// run, or the only symptom is an assistant that quietly got worse.

// captureLogOutput redirects the global logger for the duration of fn. The
// logger holds the writer it was built with, so swapping os.Stdout would not
// reach it.
func captureLogOutput(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	lg := joshlog.Get().Logger
	prevLevel := lg.GetLevel()
	lg.SetOutput(&buf)
	lg.SetLevel(joshlog.Get().Logger.GetLevel())
	t.Cleanup(func() {
		lg.SetOutput(os.Stdout)
		lg.SetLevel(prevLevel)
	})
	fn()
	return buf.String()
}

// skillsConfig is setupConfig plus a usable provider, since setupComponents
// refuses a config with none and would never reach the skills block.
func skillsConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := setupConfig(t)
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "sk-test"},
	}
	cfg.ProviderDefaults.Default = "openrouter"
	if err := os.MkdirAll(cfg.Agents.Defaults.Workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// writeWorkspaceSkill plants an untrusted workspace skill. Nothing approves it,
// which is exactly the state an operator lands in after adding a skill by hand
// or after editing one they had already trusted.
func writeWorkspaceSkill(t *testing.T, workspace, name string) {
	t.Helper()
	dir := filepath.Join(workspace, "skills", name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: \"planted by a test\"\nalways: false\n---\n\n# " + name + "\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The warning has to carry the skill's name and the command that approves it.
// "Some skills are not in use" sends the operator looking through a config file
// that has nothing to do with it.
func TestSetupComponentsAnnouncesWithheldWorkspaceSkills(t *testing.T) {
	cfg := skillsConfig(t)
	writeWorkspaceSkill(t, cfg.Agents.Defaults.Workspace, "deploy-helper")

	out := captureLogOutput(t, func() {
		if _, _, _, _, _, _, err := setupComponents(cfg); err != nil {
			t.Fatalf("setupComponents: %v", err)
		}
	})

	if !strings.Contains(out, "deploy-helper") {
		t.Errorf("the withheld skill was not named; the operator has no way to know which one:\n%s", out)
	}
	if !strings.Contains(out, "joshbot skills trust") {
		t.Errorf("the approval command was not given:\n%s", out)
	}
}

// A workspace with nothing pending must stay quiet. A warning that fires every
// start regardless of state is one the operator learns to ignore, which costs
// the real warning its only job.
func TestSetupComponentsSaysNothingWhenNoSkillIsWithheld(t *testing.T) {
	cfg := skillsConfig(t)

	out := captureLogOutput(t, func() {
		if _, _, _, _, _, _, err := setupComponents(cfg); err != nil {
			t.Fatalf("setupComponents: %v", err)
		}
	})
	if strings.Contains(out, "awaiting review") {
		t.Errorf("a workspace with no workspace skills at all still warned:\n%s", out)
	}
}
