package agent

import (
	"strings"
	"testing"
)

type evalCase struct {
	name       string
	checks     []string
	antiChecks []string
	weight     int // 1-3, higher = more important
}

type evalResult struct {
	name        string
	passed      int
	total       int
	weighted    float64
	maxWeighted float64
}

func runEval(prompt string, cases []evalCase) []evalResult {
	var results []evalResult
	for _, c := range cases {
		passed := 0
		total := len(c.checks) + len(c.antiChecks)
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
		weight := c.weight
		if weight == 0 {
			weight = 1
		}
		results = append(results, evalResult{
			name:        c.name,
			passed:      passed,
			total:       total,
			weighted:    float64(passed*weight) / float64(total),
			maxWeighted: float64(weight),
		})
	}
	return results
}

func scoreEval(results []evalResult) (float64, float64) {
	var totalWeighted, maxWeighted float64
	for _, r := range results {
		totalWeighted += r.weighted
		maxWeighted += r.maxWeighted
	}
	return totalWeighted, maxWeighted
}

func TestPromptOptimizerEval(t *testing.T) {
	prompt := buildCoreIdentity()
	words := len(strings.Fields(prompt))
	t.Logf("=== Core Identity Prompt Eval ===")
	t.Logf("Word count: %d", words)

	cases := []evalCase{
		{
			name:   "identity",
			weight: 3,
			checks: []string{"joshbot", "personal AI assistant"},
		},
		{
			name:   "tool-use guidance",
			weight: 3,
			checks: []string{"web_search", "shell", "read_file", "parallel_subagent", "chain_execution"},
		},
		{
			name:   "memory rules",
			weight: 2,
			checks: []string{"MEMORY.md", "HISTORY.md"},
		},
		{
			name:   "conversation coherence",
			weight: 3,
			checks: []string{"pronouns", "Stay on topic"},
		},
		{
			name:   "work discipline",
			weight: 2,
			checks: []string{"Read before write", "Batch operations"},
		},
		{
			name:   "continuity rules",
			weight: 3,
			checks: []string{"conversation_context", "correction", "/new"},
		},
		{
			name:   "no verbosity drift",
			weight: 2,
			antiChecks: []string{
				"you must always", "it is imperative",
				"under no circumstances", "strictly prohibited",
			},
		},
		{
			name:   "no vague instructions",
			weight: 1,
			antiChecks: []string{
				"be good", "do your best", "try to", "you should consider",
			},
		},
		{
			name:   "tool selection clarity",
			weight: 3,
			checks: []string{"web_search", "shell", "read_file"},
		},
		{
			name:   "error recovery",
			weight: 2,
			checks: []string{"Admit uncertainty", "investigate", "corrections"},
		},
	}

	results := runEval(prompt, cases)
	totalWeighted, maxWeighted := scoreEval(results)

	for _, r := range results {
		status := "PASS"
		if r.passed < r.total {
			status = "FAIL"
		}
		t.Logf("  %s: %s (%d/%d, weight=%d)", r.name, status, r.passed, r.total, int(r.maxWeighted))
	}

	score := (totalWeighted / maxWeighted) * 100
	t.Logf("\n  WEIGHTED SCORE: %.1f%% (%.0f/%.0f)", score, totalWeighted, maxWeighted)

	if totalWeighted < maxWeighted {
		t.Errorf("prompt scored %.1f%% - expected 100%%", score)
	}
}

func TestPromptOptimizerConciseness(t *testing.T) {
	prompt := buildCoreIdentity()
	words := len(strings.Fields(prompt))
	t.Logf("Word count: %d", words)
	if words > 500 {
		t.Errorf("too verbose: %d words (target < 500)", words)
	}
}

func TestPromptOptimizerNoContradictions(t *testing.T) {
	prompt := buildCoreIdentity()
	if strings.Contains(prompt, "Do not use tools") && strings.Contains(prompt, "Use them") {
		t.Errorf("contradiction: 'Do not use tools' and 'Use them'")
	}
}

func TestPromptOptimizerActionable(t *testing.T) {
	prompt := buildCoreIdentity()
	vague := []string{"be good", "do your best", "try to", "you should consider"}
	for _, v := range vague {
		if strings.Contains(prompt, v) {
			t.Errorf("vague: '%s'", v)
		}
	}
}

func TestPromptOptimizerToolDescriptions(t *testing.T) {
	descs := map[string]string{
		"filesystem":        `filesystem: read, write, edit, list, search files and directories.`,
		"shell":             `Execute shell commands (builds, tests, git, scripts). Safety restrictions active. Output truncated to 4000 chars.`,
		"web_search":        `web_search: search the internet for current information, news, and web pages.`,
		"web_fetch":         `web_fetch: fetch the full content of a URL (articles, docs, web pages).`,
		"web_code":          `web_code: search for code examples, docs, and repositories.`,
		"web_company":       `web_company: research companies, products, funding, team info, and competitors.`,
		"web_research":      `web_research: deep research on a topic using multiple sources.`,
		"message":           `Send a message to a different channel (e.g., Telegram). Not for responding to current conversation - return response as text instead.`,
		"parallel_subagent": `parallel_subagent: run multiple subagent tasks in parallel for independent work (research, file analysis, etc.).`,
		"chain_execution":   `chain_execution: execute subagent steps sequentially, feeding each step's output as context to the next.`,
		"memory_search":     `memory_search: search long-term memory for user facts and past decisions.`,
		"skill_registry":    `skill_registry: list, create, or delete skills in the registry.`,
		"subagent_config":   `Manage subagent config profiles: list, get, save, discover.`,
		"joshbot_config":    `Read or modify joshbot config: list models, switch model, adjust settings (temperature, max_tokens).`,
	}

	t.Logf("=== Tool Description Quality Check ===")
	issues := 0
	for name, desc := range descs {
		words := len(strings.Fields(desc))
		if words < 4 {
			t.Errorf("  %s: too short (%d words): %q", name, words, desc)
			issues++
		}
		if words > 25 {
			t.Errorf("  %s: too long (%d words): %q", name, words, desc)
			issues++
		}
		if !strings.Contains(desc, name) && !strings.Contains(desc, strings.ReplaceAll(name, "_", " ")) {
			t.Logf("  %s: description doesn't mention tool name: %q", name, desc)
		}
	}
	if issues == 0 {
		t.Logf("  All %d tool descriptions OK", len(descs))
	}
}
