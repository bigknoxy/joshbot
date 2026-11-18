package agent

import (
	"context"
	"strings"
	"testing"
)

// smartStubMemory returns a fixed memory document.
type smartStubMemory struct {
	doc string
}

func (m smartStubMemory) LoadMemory(context.Context) (string, error) { return m.doc, nil }
func (m smartStubMemory) LoadHistory(context.Context, string) (string, error) {
	return "", nil
}

// smartStubSkills returns a fixed skills summary.
type smartStubSkills struct {
	summary string
}

func (s smartStubSkills) LoadSummary(context.Context) (string, error) { return s.summary, nil }

const smartMemoryDoc = `# Long-Term Memory

## system
- [2026-01-01] The deployment pipeline runs on GitHub Actions.
- [2026-01-02] The user prefers dark roast coffee in the morning.
- [2026-01-03] The database is PostgreSQL 16 on the staging host.
- [2026-01-04] The team stand-up is at 09:30 every weekday.
- [2026-01-05] The office wifi password rotates every Monday.
- [2026-01-06] The production database is PostgreSQL 16.
- [2026-01-07] The release branch is named release/YYYY-MM-DD.
`

// TestRelevantMemoryFactsKeepsWhatTheQueryIsAbout is the point of the
// filter: a fact that bears on the question stays, a fact that does not goes.
func TestRelevantMemoryFactsKeepsWhatTheQueryIsAbout(t *testing.T) {
	got := relevantMemoryFacts(smartMemoryDoc, "which postgres version is production?")

	if !strings.Contains(got, "production database is PostgreSQL 16") {
		t.Errorf("the fact the query is about must survive, got:\n%s", got)
	}
	if strings.Contains(got, "dark roast coffee") {
		t.Errorf("an unrelated fact must not crowd the prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "## system") {
		t.Errorf("the category heading must survive, got:\n%s", got)
	}
}

// TestRelevantMemoryFactsAlwaysKeepsTheMostRecentFacts pins the recency rule:
// the newest facts stay even when the query mentions none of them, because
// recency is relevance the query cannot express.
func TestRelevantMemoryFactsAlwaysKeepsTheMostRecentFacts(t *testing.T) {
	got := relevantMemoryFacts(smartMemoryDoc, "what is the capital of France?")

	if !strings.Contains(got, "release branch is named release/YYYY-MM-DD") {
		t.Errorf("the most recent fact must survive, got:\n%s", got)
	}
	if !strings.Contains(got, "production database is PostgreSQL 16") {
		t.Errorf("the five most recent facts must survive, got:\n%s", got)
	}
	if strings.Contains(got, "deployment pipeline runs on GitHub Actions") {
		t.Errorf("an old, unrelated fact must be dropped, got:\n%s", got)
	}
}

// TestRelevantMemoryFactsKeepsTheOperatorsProse guards the hand-written part
// of the document: it is not a fact, and the document may have no facts in it.
func TestRelevantMemoryFactsKeepsTheOperatorsProse(t *testing.T) {
	doc := "# Long-Term Memory\n\nRemember to call the dentist.\n"
	got := relevantMemoryFacts(doc, "something entirely unrelated")

	if !strings.Contains(got, "Remember to call the dentist.") {
		t.Errorf("the operator's own prose must survive, got:\n%s", got)
	}
}

// TestRelevantMemoryFactsWithNoUsableQueryKeepsEverything: a query of only
// stop words says nothing about relevance, so nothing is dropped.
func TestRelevantMemoryFactsWithNoUsableQueryKeepsEverything(t *testing.T) {
	got := relevantMemoryFacts(smartMemoryDoc, "what is it?")
	if !strings.Contains(got, "dark roast coffee") {
		t.Errorf("a query with no usable keyword must not drop anything, got:\n%s", got)
	}
}

const smartSkillsSummary = `Available skills (use skill_registry with action="get" and the skill's name to load its full content when needed):
  <skill name="deploy" available="true">Deploy the application to production</skill>
  <skill name="coffee" available="true">Order a coffee</skill>
  <skill name="standup" available="true">Summarise the team stand-up</skill>
`

// TestRelevantSkillsKeepsWhatTheQueryIsAbout pins that a skill the query
// names survives.
func TestRelevantSkillsKeepsWhatTheQueryIsAbout(t *testing.T) {
	got := relevantSkills(smartSkillsSummary, "please order a coffee")

	if !strings.Contains(got, `name="coffee"`) {
		t.Errorf("the skill the query is about must survive, got:\n%s", got)
	}
	if strings.Contains(got, `name="standup"`) {
		t.Errorf("an unrelated skill must not crowd the prompt, got:\n%s", got)
	}
}

// TestRelevantSkillsKeepsEverythingWhenNothingMatches is the safety rule: a
// skill the model cannot see is a skill it cannot use.
func TestRelevantSkillsKeepsEverythingWhenNothingMatches(t *testing.T) {
	got := relevantSkills(smartSkillsSummary, "what is the capital of France?")

	for _, name := range []string{"deploy", "coffee", "standup"} {
		if !strings.Contains(got, `name="`+name+`"`) {
			t.Errorf("skill %q must survive when nothing matches, got:\n%s", name, got)
		}
	}
}

// TestBuildSmartPromptFiltersTheMemoryAndKeepsTheRest checks the whole
// prompt: the memory is filtered, and everything else BuildPrompt produces is
// still there.
func TestBuildSmartPromptFiltersTheMemoryAndKeepsTheRest(t *testing.T) {
	workspace := t.TempDir()
	mem := smartStubMemory{doc: smartMemoryDoc}
	sk := smartStubSkills{summary: smartSkillsSummary}

	prompt := BuildSmartPrompt(workspace, sk, mem, "Robin", "which postgres version is production?")

	if !strings.Contains(prompt, "production database is PostgreSQL 16") {
		t.Errorf("the relevant memory fact must be in the prompt, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "dark roast coffee") {
		t.Errorf("an unrelated memory fact must not be in the prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "The user's name is Robin.") {
		t.Errorf("the prompt must still name the user, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<current_time>") {
		t.Errorf("the prompt must still carry the current time, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<skills>") {
		t.Errorf("the prompt must still carry the skills, got:\n%s", prompt)
	}
}

// TestBuildSmartPromptWithNoQueryKeepsEverything: with nothing to filter by,
// the smart prompt is the plain prompt.
func TestBuildSmartPromptWithNoQueryKeepsEverything(t *testing.T) {
	workspace := t.TempDir()
	mem := smartStubMemory{doc: smartMemoryDoc}
	sk := smartStubSkills{summary: smartSkillsSummary}

	prompt := BuildSmartPrompt(workspace, sk, mem, "Robin", "")

	if !strings.Contains(prompt, "dark roast coffee") {
		t.Errorf("with no query every fact must survive, got:\n%s", prompt)
	}
}

// TestQueryKeywordsDropsTheWordsThatSayNothing keeps the stop-word list
// honest: a keyword that appears in almost every message is not evidence of
// relevance.
func TestQueryKeywordsDropsTheWordsThatSayNothing(t *testing.T) {
	got := queryKeywords("what is the deployment pipeline for the user?")

	for _, w := range got {
		if w == "the" || w == "is" || w == "what" || w == "for" {
			t.Errorf("stop word %q must be dropped, got %v", w, got)
		}
	}
	if len(got) == 0 {
		t.Error("a content word must survive")
	}
}
