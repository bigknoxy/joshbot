package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/cron"
)

// Bundled skills are injected into the agent's prompt. A skill that tells the
// agent to use a tool which is not registered produces a confident-sounding
// failure at runtime and nothing fails at build time — that is exactly how
// issue #90 (a cron skill with no cron tool) survived for months.
//
// The marker is a backticked name immediately followed by the word "tool", as
// in "the `cron` tool". "the `gh` CLI tool" does not match, which is correct:
// gh is an external binary, not a joshbot tool.
var skillToolRef = regexp.MustCompile("`([a-z][a-z0-9_]*)` tool")

// toolsAllowlist covers names that legitimately are not registry tools.
var toolsAllowlist = map[string]bool{}

func bundledSkillsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "skills")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the bundled skills directory")
		}
		dir = parent
	}
}

// fullRegistry builds a registry with every optional tool switched on, so the
// lint measures against the largest set of tools the agent can ever see.
func fullRegistry(t *testing.T) *Registry {
	t.Helper()
	ws := t.TempDir()
	svc := cron.NewService(bus.NewMessageBus(), ws)
	return RegistryWithDefaults(ws, true, 30, 30, nil, nil, nil, nil,
		WithCronService(svc, "cli"))
}

// nameMethodLiteral matches a tool's Name() method returning a string literal,
// on one line or two.
var nameMethodLiteral = regexp.MustCompile(`func \(\w+ \*\w+\) Name\(\) string \{\s*return "([a-z][a-z0-9_]*)"`)

// knownToolNames is every tool name joshbot can register. Some tools are
// registered in RegistryWithDefaults, others directly in main.go (memory_search,
// for one), so a constructed registry alone is not the full universe.
func knownToolNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}

	for _, n := range fullRegistry(t).List() {
		names[n] = true
	}

	// Harvest Name() literals straight from the package source.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range nameMethodLiteral.FindAllStringSubmatch(string(data), -1) {
			names[m[1]] = true
			found++
		}
	}
	if found == 0 {
		t.Fatal("no Name() literals found — the source scan is broken, so this lint would pass vacuously")
	}
	return names
}

func TestBundledSkillsOnlyReferenceRegisteredTools(t *testing.T) {
	registered := knownToolNames(t)

	skillsDir := bundledSkillsDir(t)
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("read skills dir: %v", err)
	}

	checked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue // not every directory has to hold a skill
		}
		checked++

		for _, m := range skillToolRef.FindAllStringSubmatch(string(data), -1) {
			name := m[1]
			if toolsAllowlist[name] {
				continue
			}
			if !registered[name] {
				t.Errorf("skills/%s/SKILL.md tells the agent to use the %q tool, "+
					"which does not exist as a joshbot tool.",
					e.Name(), name)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no bundled skills were checked — the lint is not actually reading anything")
	}
}

// The cron skill is the one that drifted. Assert its documented actions match
// the tool's enum, so rewording one without the other fails.
func TestCronSkillMatchesToolActions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(bundledSkillsDir(t), "cron", "SKILL.md"))
	if err != nil {
		t.Fatalf("read cron skill: %v", err)
	}
	skill := string(data)

	tool := NewCronTool(nil, "cli")
	var actions []string
	for _, p := range tool.Parameters() {
		if p.Name == "action" {
			actions = p.Enum
		}
	}
	if len(actions) == 0 {
		t.Fatal("cron tool advertises no actions")
	}
	for _, a := range actions {
		if !strings.Contains(skill, a) {
			t.Errorf("cron tool supports action %q but the skill never mentions it", a)
		}
	}

	// The old skill taught 5-field cron expressions, which the scheduler cannot
	// run. Make sure they do not creep back in as examples.
	for _, bad := range []string{"* * * * *", "0 9 * * *", "*/5 * * * *"} {
		if strings.Contains(skill, bad) {
			t.Errorf("cron skill still shows the cron expression %q, which the tool rejects", bad)
		}
	}
}
