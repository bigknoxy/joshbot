package agent

import (
	"strings"
)

// smartMemoryKeep is how many of the most recent memory facts survive the
// relevance filter even when the query does not mention them. Recency is
// relevance the query cannot express, and dropping the newest facts is how a
// long-term memory turns into a stale one.
const smartMemoryKeep = 5

// queryStopWords are the words too common to say anything about relevance.
var queryStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "is": true, "it": true, "for": true, "on": true,
	"with": true, "that": true, "this": true, "my": true, "i": true,
	"you": true, "what": true, "how": true, "do": true, "can": true, "me": true,
	"your": true, "our": true, "but": true, "not": true, "all": true, "any": true,
	"there": true, "here": true, "was": true, "are": true, "have": true, "has": true,
}

// queryKeywords returns the words of a query that could bear on relevance:
// lowercased, stripped of punctuation, at least three characters, and not one
// of the words that appear in almost every message.
func queryKeywords(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	out := make([]string, 0, len(fields))
	for _, w := range fields {
		w = strings.Trim(w, ".,!?;:\"'()[]{}<>*_`")
		if len(w) < 3 || queryStopWords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// lineMatches reports whether a line shares a keyword with the query.
func lineMatches(line string, keywords []string) bool {
	lower := strings.ToLower(line)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// relevantMemoryFacts keeps the memory lines that bear on the query, plus the
// most recent ones.
//
// The document is a list of bullets under category headings. Headings are
// kept with the bullets under them, and a bullet is kept when it shares a
// word with the query. A line that is neither a heading nor a bullet is the
// operator's own prose and is always kept: it is not a fact, and the
// document may have no facts in it at all.
func relevantMemoryFacts(document, query string) string {
	keywords := queryKeywords(query)
	if len(keywords) == 0 {
		return document
	}
	lines := strings.Split(document, "\n")

	// The document records facts in the order they were learned, so the last
	// bullets are the newest.
	var bullets []int
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "- ") {
			bullets = append(bullets, i)
		}
	}
	keepRecent := make(map[int]bool, smartMemoryKeep)
	for i := len(bullets) - smartMemoryKeep; i < len(bullets); i++ {
		if i >= 0 {
			keepRecent[bullets[i]] = true
		}
	}

	var b strings.Builder
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "- ") {
			if keepRecent[i] || lineMatches(l, keywords) {
				b.WriteString(l + "\n")
			}
			continue
		}
		b.WriteString(l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// relevantSkills keeps the skill lines that bear on the query.
//
// The summary is a heading followed by one line per skill. The skills are the
// agent's capabilities, and a skill the model cannot see is a skill it cannot
// use, so when the query matches none of them every skill is kept.
func relevantSkills(summary, query string) string {
	keywords := queryKeywords(query)
	if len(keywords) == 0 {
		return summary
	}
	lines := strings.Split(summary, "\n")
	kept := 0
	var b strings.Builder
	for _, l := range lines {
		if strings.Contains(l, "<skill ") {
			if !lineMatches(l, keywords) {
				continue
			}
			kept++
		}
		b.WriteString(l + "\n")
	}
	if kept == 0 {
		return summary
	}
	return strings.TrimRight(b.String(), "\n")
}
