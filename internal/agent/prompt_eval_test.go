package agent

import (
	"strings"
	"testing"
)

// promptEvalCase defines a single evaluation case for the core identity prompt.
type promptEvalCase struct {
	name       string
	checks     []string // substrings that MUST be present
	antiChecks []string // substrings that must NOT be present
}

// promptEvalRubric scores a prompt against a case.
func promptEvalRubric(prompt string, c promptEvalCase) (passed int, total int) {
	total = len(c.checks) + len(c.antiChecks)
	for _, check := range c.checks {
		if strings.Contains(prompt, check) {
			passed++
		}
	}
	for _, anti := range c.antiChecks {
		if !strings.Contains(prompt, anti) {
			passed++
		}
	}
	return passed, total
}

func TestCoreIdentityEval(t *testing.T) {
	prompt := buildCoreIdentity()

	cases := []promptEvalCase{
		{
			name: "identity and purpose",
			checks: []string{
				"joshbot",
				"personal AI assistant",
				"tools",
			},
		},
		{
			name: "tool use guidance",
			checks: []string{
				"web_search",
				"shell",
				"read_file",
				"parallel_subagent",
				"chain_execution",
			},
		},
		{
			name: "memory instructions",
			checks: []string{
				"MEMORY.md",
				"HISTORY.md",
				"Update MEMORY.md",
			},
		},
		{
			name: "conversation coherence",
			checks: []string{
				"pronouns",
				"Track the flow",
				"Stay on topic",
			},
		},
		{
			name: "work principles",
			checks: []string{
				"Read before write",
				"Batch operations",
				"Admit uncertainty",
			},
		},
		{
			name: "no verbosity drift",
			antiChecks: []string{
				"you must always",
				"it is imperative",
				"under no circumstances",
				"strictly prohibited",
			},
		},
	}

	totalPassed := 0
	totalChecks := 0
	for _, c := range cases {
		passed, total := promptEvalRubric(prompt, c)
		totalPassed += passed
		totalChecks += total
		t.Logf("  %s: %d/%d passed", c.name, passed, total)
	}

	score := float64(totalPassed) / float64(totalChecks) * 100
	t.Logf("\n  OVERALL SCORE: %.1f%% (%d/%d checks passed)", score, totalPassed, totalChecks)

	if totalPassed < totalChecks {
		t.Errorf("prompt scored %.1f%% - expected 100%%", score)
	}
}

func TestCoreIdentityConciseness(t *testing.T) {
	prompt := buildCoreIdentity()
	words := len(strings.Fields(prompt))
	t.Logf("  Word count: %d", words)
	if words > 500 {
		t.Errorf("prompt too verbose: %d words (target < 500)", words)
	}
}

func TestCoreIdentityNoContradictions(t *testing.T) {
	prompt := buildCoreIdentity()

	contradictions := []struct{ a, b string }{
		{"Do not use tools", "Use them"},
		{"never", "always"},
	}

	for _, c := range contradictions {
		hasA := strings.Contains(prompt, c.a)
		hasB := strings.Contains(prompt, c.b)
		if hasA && hasB {
			t.Logf("  Potential contradiction: '%s' and '%s' both present", c.a, c.b)
		}
	}
}

func TestCoreIdentityActionableInstructions(t *testing.T) {
	prompt := buildCoreIdentity()

	// Check for vague instructions that should be more specific
	vaguePatterns := []string{
		"be good",
		"do your best",
		"try to",
		"you should consider",
	}

	for _, v := range vaguePatterns {
		if strings.Contains(prompt, v) {
			t.Errorf("vague instruction found: '%s'", v)
		}
	}
}

// The announce-without-doing failure mode (issue #283): on several providers
// the model replied with an intention ("Let me dig into...", "I'd be happy to
// check...") and then stopped, having called no tools. The system prompt must
// carry an explicit act-before-narrating rule so the turn ends on a result
// (or a concrete blocker), never on an intention.
func TestCoreIdentityActDontAnnounce(t *testing.T) {
	prompt := buildCoreIdentity()

	for _, check := range []string{
		"Act before you narrate",
		"run the tools, then report",
		"Never reply with an intention",
	} {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt lacks the act-don't-announce rule containing %q", check)
		}
	}
}
