package skills

import (
	"context"
	"strings"
	"testing"
)

// The bundled set must load no matter where joshbot runs from.
//
// It used to be found via the relative path "skills", which resolves against
// the process working directory, so an installed binary — which has no files
// beside it at all — reported "No skills found". Only a run from a checkout of
// joshbot's own source tree ever saw them.
func TestBundledSkillsLoadFromAnyWorkingDirectory(t *testing.T) {
	// t.Chdir restores the previous directory, so this cannot leak into the
	// other tests in the package.
	t.Chdir(t.TempDir())

	loader, err := NewLoader(t.TempDir())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if err := loader.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	names := map[string]bool{}
	for _, sk := range loader.List() {
		if sk.Bundled {
			names[sk.Name] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("no bundled skills discovered from an unrelated working directory")
	}
	for _, want := range []string{"cron", "github", "memory", "skill-creator"} {
		if !names[want] {
			t.Errorf("bundled skill %q missing; got %v", want, names)
		}
	}
}

// A bundled skill's body has to be readable without touching the filesystem.
func TestBundledSkillContentComesFromTheBinary(t *testing.T) {
	loader, err := NewLoader(t.TempDir())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if err := loader.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	sk := loader.GetSkill("cron")
	if sk == nil {
		t.Fatal("bundled skill \"cron\" not discovered")
	}
	if !sk.Trusted {
		t.Error("a bundled skill must be trusted; it arrives with the binary")
	}
	if body := sk.GetContent(); strings.TrimSpace(body) == "" {
		t.Error("bundled skill content is empty, so nothing was embedded")
	}

	summary, err := loader.LoadSummary(context.Background())
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if !strings.Contains(summary, "cron") {
		t.Errorf("bundled skill missing from the prompt summary: %s", summary)
	}

	full, err := loader.LoadFullSkillContent(context.Background(), "cron")
	if err != nil {
		t.Fatalf("LoadFullSkillContent: %v", err)
	}
	if strings.TrimSpace(full) == "" {
		t.Error("progressive loading returned nothing for a bundled skill")
	}
}

// Deleting a bundled skill has nothing to remove, and its path is an embed
// path rather than a filesystem one.
func TestBundledSkillCannotBeDeleted(t *testing.T) {
	loader, err := NewLoader(t.TempDir())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if err := loader.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := loader.Delete("cron"); err == nil {
		t.Error("Delete accepted a bundled skill")
	}
}
