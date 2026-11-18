package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/skills"
)

// writeSkill creates a workspace skill whose frontmatter name is name. The
// name is what the notice shows, so it is passed separately to cover the case
// where the directory and the frontmatter disagree.
func writeSkill(t *testing.T, workspace, dir, name string) {
	t.Helper()
	dirPath := filepath.Join(workspace, "skills", dir)
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", dirPath, err)
	}
	content := "---\nname: " + name + "\ndescription: a skill for the test\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dirPath, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}
}

// pendingSkillLoader returns a loader over workspace with no skill approved.
// No trust store means nothing from the workspace is approved, which is the
// fail-closed default the notice has to work with.
func pendingSkillLoader(t *testing.T, workspace string) *skills.Loader {
	t.Helper()
	loader, err := skills.NewLoader(workspace)
	if err != nil {
		t.Fatalf("skills.NewLoader() error = %v", err)
	}
	if err := loader.Discover(); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	return loader
}

func TestPendingSkillsNoticeNamesPendingSkillsAndDoesNotRepeat(t *testing.T) {
	workspace := t.TempDir()
	writeSkill(t, workspace, "alpha", "alpha")
	writeSkill(t, workspace, "beta", "beta")

	loader := pendingSkillLoader(t, workspace)
	a := &Agent{skillLoader: loader}
	sess := session.NewSession("cli:test")

	notice := a.pendingSkillsNotice(sess)
	if notice == "" {
		t.Fatal("pendingSkillsNotice() = \"\", want a notice naming the pending skills")
	}
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice must name the pending skill %q, got: %s", want, notice)
		}
	}
	if !strings.Contains(notice, "joshbot skills trust") {
		t.Errorf("notice must say what to do about it, got: %s", notice)
	}
	if !strings.Contains(notice, "2 workspace skills are waiting") {
		t.Errorf("notice must count the pending skills, got: %s", notice)
	}

	// It is shown once, not on every single message.
	if again := a.pendingSkillsNotice(sess); again != "" {
		t.Errorf("pendingSkillsNotice() on the second call = %q, want \"\" (the notice must not repeat)", again)
	}
}

func TestPendingSkillsNoticeDisappearsOnceTheSkillIsTrusted(t *testing.T) {
	workspace := t.TempDir()
	writeSkill(t, workspace, "alpha", "alpha")

	loader, err := skills.NewLoader(workspace)
	if err != nil {
		t.Fatalf("skills.NewLoader() error = %v", err)
	}
	store, err := skills.LoadTrustStore(filepath.Join(workspace, "skills.trust"))
	if err != nil {
		t.Fatalf("LoadTrustStore() error = %v", err)
	}
	loader.SetTrustStore(store)
	if err := loader.Discover(); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	a := &Agent{skillLoader: loader}
	sess := session.NewSession("cli:test")

	if notice := a.pendingSkillsNotice(sess); notice == "" {
		t.Fatal("pendingSkillsNotice() = \"\", want a notice for the untrusted skill")
	}

	if err := loader.Trust("alpha"); err != nil {
		t.Fatalf("Trust(alpha) error = %v", err)
	}
	if notice := a.pendingSkillsNotice(sess); notice != "" {
		t.Errorf("pendingSkillsNotice() after the skill was trusted = %q, want \"\"", notice)
	}
	if len(sess.PendingSkillsNoticed) != 0 {
		t.Errorf("the remembered set must be cleared once nothing is pending, got %v", sess.PendingSkillsNoticed)
	}
}

func TestPendingSkillsNoticeCatchesASkillAddedLater(t *testing.T) {
	workspace := t.TempDir()
	writeSkill(t, workspace, "alpha", "alpha")

	loader := pendingSkillLoader(t, workspace)
	a := &Agent{skillLoader: loader}
	sess := session.NewSession("cli:test")

	if notice := a.pendingSkillsNotice(sess); notice == "" {
		t.Fatal("pendingSkillsNotice() = \"\", want a notice for the untrusted skill")
	}
	if again := a.pendingSkillsNotice(sess); again != "" {
		t.Errorf("pendingSkillsNotice() on the second call = %q, want \"\"", again)
	}

	// A skill added later is a new one, so it has to be announced - and the
	// earlier skill must not be announced a second time on its own.
	writeSkill(t, workspace, "gamma", "gamma")
	loader.Invalidate()

	notice := a.pendingSkillsNotice(sess)
	if notice == "" {
		t.Fatal("pendingSkillsNotice() = \"\", want a notice for the newly added skill")
	}
	if !strings.Contains(notice, "gamma") {
		t.Errorf("notice must name the newly added skill, got: %s", notice)
	}
	if !strings.Contains(notice, "alpha") {
		t.Errorf("notice should still list what is pending, got: %s", notice)
	}
}

func TestPendingSkillsNoticeIsEmptyWithoutAPendingSkill(t *testing.T) {
	workspace := t.TempDir()
	loader := pendingSkillLoader(t, workspace)
	a := &Agent{skillLoader: loader}
	sess := session.NewSession("cli:test")

	if notice := a.pendingSkillsNotice(sess); notice != "" {
		t.Errorf("pendingSkillsNotice() = %q, want \"\" when nothing is pending", notice)
	}
}

func TestPendingSkillsNoticeWithNoLoader(t *testing.T) {
	a := &Agent{}
	sess := session.NewSession("cli:test")
	if notice := a.pendingSkillsNotice(sess); notice != "" {
		t.Errorf("pendingSkillsNotice() = %q, want \"\" when there is no skill loader", notice)
	}
}

func TestSanitizeSkillNameDropsControlCharacters(t *testing.T) {
	got := sanitizeSkillName("red\x1b[31m\x07name")
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("sanitizeSkillName() = %q, want no control characters", got)
	}
	if !strings.Contains(got, "red") {
		t.Errorf("sanitizeSkillName() = %q, want the readable text kept", got)
	}
}

func TestSanitizeSkillNameBoundsTheLength(t *testing.T) {
	got := sanitizeSkillName(strings.Repeat("x", 200))
	if len(got) > 80 {
		t.Errorf("sanitizeSkillName() length = %d, want it bounded", len(got))
	}
}

func TestSanitizeSkillNameHandlesAnEmptyName(t *testing.T) {
	if got := sanitizeSkillName("   "); got == "" {
		t.Error("sanitizeSkillName() = \"\", want a placeholder")
	}
}
