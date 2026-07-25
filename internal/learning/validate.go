package learning

import "strings"

// maxFactContentLength bounds the length of a single consolidated fact. The
// consolidation prompt asks for short factual one-line statements; anything
// longer looks like unfiltered prose, not an extracted fact, and is rejected
// rather than truncated (see GH issue #73).
const maxFactContentLength = 300

// refusalPatterns are case-insensitive substrings that mark a line as a
// refusal or meta-commentary rather than an extracted fact. Matching is
// deliberately whole-phrase (not single words) to avoid rejecting legitimate
// facts that happen to share a word with one of these phrases.
var refusalPatterns = []string{
	"i don't have enough",
	"i do not have enough",
	"i don't have access",
	"i do not have access",
	"not enough context",
	"not enough information",
	"as an ai",
	"as a language model",
	"i cannot extract",
	"i can't extract",
	"unable to extract",
	"i'm sorry",
	"i am sorry",
	"i don't know",
	"i do not know",
	"no facts",
	"no factual",
	"i cannot determine",
	"i can't determine",
	"here are the facts",
	"here is a summary",
	"here's a summary",
	"i don't see",
	"i do not see",
}

// looksLikeRefusalOrMeta reports whether line reads like a refusal or
// meta-commentary rather than an extracted fact.
func looksLikeRefusalOrMeta(line string) bool {
	lower := strings.ToLower(line)
	for _, p := range refusalPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// stripListMarker removes a leading list marker ("- ", "* ", "• ", "1. ",
// "2) ") so downstream checks see the actual content, since models commonly
// format "a short list of one-line statements" as a bulleted or numbered
// list.
func stripListMarker(line string) string {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i < len(line) && (line[i] == '.' || line[i] == ')') {
		if rest := strings.TrimSpace(line[i+1:]); rest != "" {
			return rest
		}
	}
	return line
}

// extractValidFacts splits a raw model completion into candidate one-line
// facts and applies a deterministic content gate: each line must be
// non-empty after trimming a list marker, within maxFactLen characters, and
// must not look like a refusal or meta-commentary. At most maxFacts
// survivors are returned, in order.
//
// If no line survives, facts is nil and reason explains why, for logging.
// The caller must not persist anything in that case.
func extractValidFacts(summary string, maxFactLen, maxFacts int) (facts []string, reason string) {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return nil, "empty or whitespace-only completion"
	}

	var rejectedLong, rejectedRefusal int
	for _, raw := range strings.Split(trimmed, "\n") {
		line := stripListMarker(raw)
		if line == "" {
			continue
		}
		if len(line) > maxFactLen {
			rejectedLong++
			continue
		}
		if looksLikeRefusalOrMeta(line) {
			rejectedRefusal++
			continue
		}
		facts = append(facts, line)
		if len(facts) >= maxFacts {
			break
		}
	}

	if len(facts) == 0 {
		switch {
		case rejectedRefusal > 0 && rejectedLong == 0:
			reason = "completion looked like a refusal or meta-commentary"
		case rejectedLong > 0 && rejectedRefusal == 0:
			reason = "completion exceeded per-fact length bound"
		case rejectedLong > 0 || rejectedRefusal > 0:
			reason = "completion contained no usable facts (too long or refusal-like)"
		default:
			reason = "completion contained no usable facts"
		}
		return nil, reason
	}
	return facts, ""
}
