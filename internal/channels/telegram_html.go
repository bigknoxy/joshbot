package channels

import "strings"

// Telegram's HTML parse mode needs exactly three characters escaped -- &, <
// and > -- against MarkdownV2's eighteen ordinary punctuation characters. That
// asymmetry is the whole reason this file exists: LLM prose is full of
// underscores, dots, parentheses and hyphens, every one of which is a
// MarkdownV2 metacharacter, so a Markdown send fails on entity parsing
// routinely and the recovery path is a retry with formatting dropped. Escaping
// at interpolation time and sending HTML means the formatting survives.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// EscapeHTML makes a literal string safe to interpolate into Telegram HTML.
// Only the three characters Telegram's parser treats as markup are escaped; a
// wider escape would publish visible entity references in ordinary prose.
func EscapeHTML(s string) string { return htmlEscaper.Replace(s) }

// Attribute values are interpolated between double quotes, so a `"` in a
// fence language tag or an href — legal in a URL — would otherwise close the
// attribute early and fail the whole message on entity parsing.
var htmlAttrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func escapeHTMLAttr(s string) string { return htmlAttrEscaper.Replace(s) }

// MarkdownToHTML renders the Markdown subset an LLM actually emits into the
// tag subset Telegram accepts, escaping every literal run on the way through.
//
// Three rules make this safe. Code spans and fenced blocks are emitted
// verbatim-escaped and are never scanned for emphasis, so a shell one-liner
// full of asterisks survives. An unterminated marker is literal text, not an
// open tag -- half a bold run must not swallow the remainder of the message.
// And the output is only ever generated from escaped input, so an LLM writing
// "<script>" ships as text rather than as a parse failure.
//
// Callers split first and convert per part: entity parsing only ever removes
// characters, so a part inside Telegram's 4096 limit as Markdown is still
// inside it once converted, and no tag can straddle a part boundary.
func MarkdownToHTML(md string) string {
	var out strings.Builder
	out.Grow(len(md) + len(md)/8)

	for i := 0; i < len(md); {
		// Fenced code block. The fence is only a fence at the start of a line;
		// mid-line backticks are an inline span.
		if strings.HasPrefix(md[i:], "```") && atLineStart(md, i) {
			if body, lang, next, ok := scanFence(md, i); ok {
				out.WriteString("<pre>")
				if lang != "" {
					out.WriteString(`<code class="language-` + escapeHTMLAttr(lang) + `">`)
				}
				out.WriteString(EscapeHTML(body))
				if lang != "" {
					out.WriteString("</code>")
				}
				out.WriteString("</pre>")
				i = next
				continue
			}
		}

		if md[i] == '`' {
			if body, next, ok := scanDelim(md, i, "`"); ok {
				out.WriteString("<code>" + EscapeHTML(body) + "</code>")
				i = next
				continue
			}
		}

		if body, next, tag, ok := scanEmphasis(md, i); ok {
			out.WriteString("<" + tag + ">" + MarkdownToHTML(body) + "</" + tag + ">")
			i = next
			continue
		}

		if md[i] == '[' {
			if text, href, next, ok := scanLink(md, i); ok {
				out.WriteString(`<a href="` + escapeHTMLAttr(href) + `">` + MarkdownToHTML(text) + "</a>")
				i = next
				continue
			}
		}

		out.WriteString(EscapeHTML(md[i : i+1]))
		i++
	}

	return out.String()
}

type emphasisRule struct {
	marker string
	tag    string
	// wordSafe marks a marker that may not open or close inside a word.
	// Underscores are the whole reason this file exists: snake_case_name is
	// ordinary prose in every joshbot reply, and treating it as emphasis is
	// exactly the corruption Markdown mode already inflicts.
	wordSafe bool
}

// Longest marker first: "**" must be tested before "*", or every bold run is
// read as an empty italic followed by literal text.
var emphasis = []emphasisRule{
	{marker: "**", tag: "b"},
	{marker: "__", tag: "u", wordSafe: true},
	{marker: "~~", tag: "s"},
	{marker: "*", tag: "i"},
	{marker: "_", tag: "i", wordSafe: true},
}

// scanEmphasis tries every marker in table order -- longest first, so "**" is
// tested before "*" and a bold run is not read as an empty italic.
func scanEmphasis(s string, i int) (body string, next int, tag string, ok bool) {
	for _, e := range emphasis {
		if !strings.HasPrefix(s[i:], e.marker) {
			continue
		}
		if e.wordSafe && i > 0 && isWordByte(s[i-1]) {
			return "", 0, "", false
		}
		if body, next, ok := scanDelim(s, i, e.marker); ok {
			if e.wordSafe && next < len(s) && isWordByte(s[next]) {
				return "", 0, "", false
			}
			return body, next, e.tag, true
		}
		// The marker matched but never closed: it is literal text, and no
		// shorter marker may claim it either ("**" must not fall back to "*").
		return "", 0, "", false
	}
	return "", 0, "", false
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b >= 0x80
}

func atLineStart(s string, i int) bool { return i == 0 || s[i-1] == '\n' }

// scanFence reads a ```lang\nbody``` block. It reports false for an
// unterminated fence so the backticks stay literal rather than eating the
// rest of the message.
func scanFence(s string, i int) (body, lang string, next int, ok bool) {
	rest := s[i+3:]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return "", "", 0, false
	}
	lang = strings.TrimSpace(rest[:nl])
	if strings.ContainsAny(lang, " \t") {
		// A first line with spaces is prose, not a language tag.
		lang = ""
		nl = -1
	}
	var start int
	if nl >= 0 {
		start = i + 3 + nl + 1
	} else {
		start = i + 3
	}
	end := strings.Index(s[start:], "```")
	if end < 0 {
		return "", "", 0, false
	}
	body = strings.TrimSuffix(s[start:start+end], "\n")
	return body, lang, start + end + 3, true
}

// scanDelim reads a marker-delimited run. An empty body or a missing closer
// reports false, which leaves the marker as literal text.
func scanDelim(s string, i int, marker string) (body string, next int, ok bool) {
	start := i + len(marker)
	if start >= len(s) {
		return "", 0, false
	}
	end := strings.Index(s[start:], marker)
	if end <= 0 {
		return "", 0, false
	}
	// A marker run may not span a blank line: an underscore in one paragraph
	// and another three paragraphs later is two literal underscores.
	if strings.Contains(s[start:start+end], "\n\n") {
		return "", 0, false
	}
	return s[start : start+end], start + end + len(marker), true
}

// scanLink reads [text](href). Only http, https and tg protocols are emitted
// as links -- a javascript: or data: href in model output has no legitimate
// use here, and Telegram would render it as a tappable link.
func scanLink(s string, i int) (text, href string, next int, ok bool) {
	close := strings.IndexByte(s[i:], ']')
	if close < 0 || i+close+1 >= len(s) || s[i+close+1] != '(' {
		return "", "", 0, false
	}
	text = s[i+1 : i+close]
	rest := s[i+close+2:]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return "", "", 0, false
	}
	href = rest[:end]
	if !safeHref(href) {
		return "", "", 0, false
	}
	return text, href, i + close + 2 + end + 1, true
}

func safeHref(href string) bool {
	l := strings.ToLower(href)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "tg://")
}
