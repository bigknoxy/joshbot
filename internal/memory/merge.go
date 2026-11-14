package memory

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// renderFactsDocument builds a complete MEMORY.md document from a fact set.
//
// This is the shape WriteFacts has always produced: a title, then one
// section per category. It is only correct for a document that does not
// exist yet — merging into an existing one is mergeFactsDocument's job, and
// losing the distinction is how a user's own notes got erased.
func renderFactsDocument(facts []Fact) string {
	byCategory := map[FactCategory][]Fact{}
	for _, f := range facts {
		byCategory[f.Category] = append(byCategory[f.Category], f)
	}

	var b strings.Builder
	b.WriteString("# Long-Term Memory\n\n")

	categories := make([]FactCategory, 0, len(byCategory))
	for cat := range byCategory {
		categories = append(categories, cat)
	}
	sort.Slice(categories, func(i, j int) bool {
		return categories[i] < categories[j]
	})

	for _, cat := range categories {
		b.WriteString(fmt.Sprintf("## %s\n", string(cat)))
		for _, f := range byCategory[cat] {
			b.WriteString(FormatFactMarkdown(f))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}

// parseSystemFactLine parses a line in the exact shape FormatFactMarkdown
// writes, and reports false for anything else.
//
// The distinction matters because parseFactLine is deliberately permissive:
// it accepts any "- " bullet with text as a fact, so a line the user wrote by
// hand ("- Remember to call the dentist") parses as a fact too. Only lines
// carrying a date and a confidence — the shape joshbot itself writes — are
// system facts, and only system facts may be rewritten or evicted. Everything
// else is the user's content and is preserved byte for byte.
//
// The content extraction mirrors parseFactLine's (both take the *last*
// metadata group), so the two agree on a fact's ID for every line they both
// recognise.
func parseSystemFactLine(line string, category FactCategory) (Fact, bool) {
	rest := strings.TrimSpace(line)
	if !strings.HasPrefix(rest, "- [") {
		return Fact{}, false
	}
	end := strings.Index(rest, "]")
	if end < 0 {
		return Fact{}, false
	}
	created, err := time.Parse("2006-01-02", rest[3:end])
	if err != nil {
		return Fact{}, false
	}
	rest = strings.TrimSpace(rest[end+1:])
	if rest == "" {
		return Fact{}, false
	}

	metaStart := strings.LastIndex(rest, " (")
	if metaStart < 0 || !strings.HasSuffix(rest, ")") {
		return Fact{}, false
	}
	meta := rest[metaStart+2 : len(rest)-1]
	content := strings.TrimSpace(rest[:metaStart])
	if content == "" {
		return Fact{}, false
	}

	confIdx := strings.Index(meta, "confidence:")
	if confIdx < 0 {
		return Fact{}, false
	}
	confPart := meta[confIdx+len("confidence:"):]
	if comma := strings.Index(confPart, ","); comma >= 0 {
		confPart = confPart[:comma]
	}
	confidence, err := strconv.ParseFloat(strings.TrimSpace(confPart), 64)
	if err != nil {
		return Fact{}, false
	}

	var tags []string
	if tagsIdx := strings.Index(meta, "tags:"); tagsIdx >= 0 {
		for _, t := range strings.Split(meta[tagsIdx+len("tags:"):], ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
	}

	return Fact{
		ID:         FactID(category, content),
		Category:   category,
		Content:    content,
		Tags:       tags,
		Confidence: ClampConfidence(confidence),
		CreatedAt:  created,
		UpdatedAt:  created,
	}, true
}

// parseSystemFacts collects the facts in a document that joshbot itself
// wrote — the lines carrying a date and a confidence.
//
// parseFacts is deliberately permissive and accepts any "- " bullet with
// text, so a line the user wrote by hand ("- Remember to call the dentist")
// comes back as a fact. The consolidator must not treat those as facts: it
// would hand the user's prose a confidence of 1.0, rank it above everything
// the system learned, and spend one of the hundred fact slots on it.
func parseSystemFacts(content string) []Fact {
	var facts []Fact
	var section FactCategory
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			section = FactCategory(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			continue
		}
		if section == "" {
			continue
		}
		if f, ok := parseSystemFactLine(trimmed, section); ok {
			facts = append(facts, f)
		}
	}
	return facts
}

// mergeFactsDocument merges a fact set into an existing MEMORY.md document.
//
// It is a section-merge, not a rewrite:
//
//   - A system fact line whose fact is still live is updated in place.
//   - A system fact line whose fact was evicted by the fact cap is removed.
//   - Every other line — prose, HTML comments, headings the user added,
//     bullets written by hand — is preserved exactly.
//   - A fact that is not in the document yet is inserted into its category's
//     section, and the section is created if the document has none.
//
// The fact cap applies to facts only: evicting a fact never takes a line of
// the user's own writing with it.
func mergeFactsDocument(existing string, facts []Fact) string {
	if strings.TrimSpace(existing) == "" {
		return renderFactsDocument(facts)
	}

	incoming := make(map[string]Fact, len(facts))
	for _, f := range facts {
		incoming[f.ID] = f
	}

	lines := strings.Split(existing, "\n")
	out := make([]string, 0, len(lines)+len(facts))
	placed := make(map[string]bool, len(facts))

	// headingAt is where a category's section starts; factAt is the last
	// system fact line inside it, which is where new facts join the group.
	headingAt := map[FactCategory]int{}
	factAt := map[FactCategory]int{}

	var section FactCategory
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## ") {
			section = FactCategory(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			out = append(out, line)
			headingAt[section] = len(out) - 1
			factAt[section] = -1
			continue
		}

		if section != "" {
			if sys, ok := parseSystemFactLine(trimmed, section); ok {
				if merged, live := incoming[sys.ID]; live {
					out = append(out, FormatFactMarkdown(merged))
					placed[sys.ID] = true
					factAt[section] = len(out) - 1
					continue
				}
				// The fact was evicted by the cap. Drop the line joshbot
				// wrote; the rest of the section stays.
				continue
			}
			if loose := parseFactLine(trimmed, section); loose != nil {
				if _, live := incoming[loose.ID]; live {
					// The fact is already in the document, in a shape
					// joshbot did not write. Leave the line alone, but do
					// not add a second copy of the fact.
					placed[loose.ID] = true
				}
			}
		}

		out = append(out, line)
	}

	pending := map[FactCategory][]Fact{}
	for _, f := range facts {
		if placed[f.ID] {
			continue
		}
		pending[f.Category] = append(pending[f.Category], f)
	}
	if len(pending) == 0 {
		return strings.Join(out, "\n")
	}

	categories := make([]FactCategory, 0, len(pending))
	for cat := range pending {
		categories = append(categories, cat)
	}
	sort.Slice(categories, func(i, j int) bool {
		return categories[i] < categories[j]
	})

	type insertion struct {
		at    int
		lines []string
	}
	var inserts []insertion

	for _, cat := range categories {
		group := pending[cat]
		formatted := make([]string, 0, len(group))
		for _, f := range group {
			formatted = append(formatted, FormatFactMarkdown(f))
		}

		if at, ok := headingAt[cat]; ok {
			pos := at + 1
			if fa := factAt[cat]; fa >= 0 {
				pos = fa + 1
			}
			inserts = append(inserts, insertion{at: pos, lines: formatted})
			continue
		}

		// The document has no section for this category yet. Add one after
		// the last line that carries content, keeping the document's own
		// trailing blank lines at the end.
		block := []string{"", fmt.Sprintf("## %s", string(cat))}
		block = append(block, formatted...)
		at := len(out)
		for at > 0 && strings.TrimSpace(out[at-1]) == "" {
			at--
		}
		inserts = append(inserts, insertion{at: at, lines: block})
	}

	// Apply from the back so the earlier positions stay valid.
	sort.SliceStable(inserts, func(i, j int) bool {
		return inserts[i].at > inserts[j].at
	})
	for _, ins := range inserts {
		at := ins.at
		if at < 0 {
			at = 0
		}
		if at > len(out) {
			at = len(out)
		}
		out = append(out, make([]string, len(ins.lines))...)
		copy(out[at+len(ins.lines):], out[at:len(out)-len(ins.lines)])
		copy(out[at:], ins.lines)
	}

	return strings.Join(out, "\n")
}
