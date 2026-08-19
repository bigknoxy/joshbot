package channels

import (
	"errors"
	"strings"
	"testing"

	telebot "gopkg.in/telebot.v3"
)

// The whole point of moving off Markdown is that ordinary prose stops being a
// minefield. Each of these strings is a real 400 "can't parse entities" under
// Telegram's legacy Markdown parser.
func TestMarkdownToHTMLLeavesOrdinaryProseAlone(t *testing.T) {
	cases := []string{
		"snake_case_name and another_one here",
		"see internal/tools/shell.go:152 (line 152) for the check",
		"cost was 1.5 - 2.0 per call",
		"use * as a wildcard",
		"a `dangling backtick",
		"**unterminated bold keeps going",
	}
	for _, in := range cases {
		got := MarkdownToHTML(in)
		if got != in {
			t.Errorf("MarkdownToHTML(%q) = %q, want it unchanged", in, got)
		}
	}
}

// An unterminated marker that swallowed the remainder would silently truncate
// or reformat the rest of a reply, which is worse than the failure it replaces.
func TestMarkdownToHTMLUnterminatedMarkerDoesNotSwallowRest(t *testing.T) {
	got := MarkdownToHTML("start **bold never closes and then more text")
	if strings.Contains(got, "<b>") {
		t.Fatalf("unterminated ** opened a tag: %q", got)
	}
	if !strings.HasSuffix(got, "more text") {
		t.Fatalf("remainder lost: %q", got)
	}
}

func TestMarkdownToHTMLEscapesLiteralMarkup(t *testing.T) {
	got := MarkdownToHTML(`if a < b && c > d then <script>alert(1)</script>`)
	if strings.Contains(got, "<script") {
		t.Fatalf("raw tag survived: %q", got)
	}
	want := `if a &lt; b &amp;&amp; c &gt; d then &lt;script&gt;alert(1)&lt;/script&gt;`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// Code content must never be Markdown-parsed: a shell one-liner is mostly
// metacharacters, and emphasis inside it would corrupt the command shown.
func TestMarkdownToHTMLCodeContentIsNotParsed(t *testing.T) {
	got := MarkdownToHTML("```sh\nfind . -name '*_test*' -exec rm {} +\n```")
	if !strings.Contains(got, `<pre><code class="language-sh">`) {
		t.Fatalf("missing language-tagged pre: %q", got)
	}
	if strings.Contains(got, "<i>") || strings.Contains(got, "<b>") {
		t.Fatalf("emphasis parsed inside code: %q", got)
	}
	if !strings.Contains(got, "-name '*_test*'") {
		t.Fatalf("code body mangled: %q", got)
	}
}

func TestMarkdownToHTMLInlineCodeEscapesAndDoesNotNest(t *testing.T) {
	got := MarkdownToHTML("run `go test ./... && echo <ok>` now")
	want := "run <code>go test ./... &amp;&amp; echo &lt;ok&gt;</code> now"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMarkdownToHTMLRendersSupportedEmphasis(t *testing.T) {
	cases := map[string]string{
		"**bold**":             "<b>bold</b>",
		"*italic*":             "<i>italic</i>",
		"_italic_":             "<i>italic</i>",
		"__underline__":        "<u>underline</u>",
		"~~struck~~":           "<s>struck</s>",
		"**bold _inner_ end**": "<b>bold <i>inner</i> end</b>",
	}
	for in, want := range cases {
		if got := MarkdownToHTML(in); got != want {
			t.Errorf("MarkdownToHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

// A javascript: or data: href in model output has no legitimate use and would
// render as a tappable link, so it stays literal text.
func TestMarkdownToHTMLLinkSchemeIsAllowlisted(t *testing.T) {
	if got := MarkdownToHTML("[docs](https://example.com/a?b=1&c=2)"); got != `<a href="https://example.com/a?b=1&amp;c=2">docs</a>` {
		t.Fatalf("https link: %q", got)
	}
	for _, bad := range []string{"[x](javascript:alert(1))", "[x](data:text/html,<b>)", "[x](JaVaScRiPt:alert(1))"} {
		got := MarkdownToHTML(bad)
		if strings.Contains(got, "<a ") {
			t.Errorf("MarkdownToHTML(%q) produced a link: %q", bad, got)
		}
	}
}

// Telegram counts message length "after entities parsing", so escaping cannot
// push a part over the limit -- but tag text can. Splitting the Markdown first
// and converting each part is what keeps the guarantee: conversion only ever
// replaces markers with tags whose rendered length is zero.
func TestMarkdownToHTMLPartsStayUnderTelegramLimit(t *testing.T) {
	long := strings.Repeat("some prose with _underscores_ and a `code span` here. ", 200)
	if len(long) <= TelegramMaxMessageLen {
		t.Fatalf("fixture too short to exercise splitting: %d", len(long))
	}
	for i, part := range splitMessage(long, TelegramMaxMessageLen) {
		html := MarkdownToHTML(part)
		if n := renderedLen(html); n > TelegramMaxMessageLen {
			t.Errorf("part %d renders to %d chars, over the %d limit", i, n, TelegramMaxMessageLen)
		}
	}
}

// renderedLen approximates what Telegram counts: tags contribute nothing and
// each entity reference collapses back to one character.
func renderedLen(html string) int {
	var out strings.Builder
	depth := 0
	for _, r := range html {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			out.WriteRune(r)
		}
	}
	s := out.String()
	s = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(s)
	return len([]rune(s))
}

func TestEscapeHTMLEscapesExactlyThreeCharacters(t *testing.T) {
	if got := EscapeHTML(`a&b<c>d`); got != "a&amp;b&lt;c&gt;d" {
		t.Fatalf("got %q", got)
	}
	// MarkdownV2's other fifteen metacharacters must pass through untouched --
	// escaping them would publish visible backslashes in ordinary prose.
	const others = `_*[]()~` + "`" + `>#+-=|{}.!`
	got := EscapeHTML(others)
	if got != strings.ReplaceAll(others, ">", "&gt;") {
		t.Fatalf("over-escaped: %q", got)
	}
}

// telebot v3 defines two not-modified errors and only ErrSameMessageContent
// matches what the API actually returns, so isNotModifiedError matches on the
// shared text rather than errors.Is against either symbol.
func TestIsNotModifiedErrorCoversBothTelebotSymbols(t *testing.T) {
	for _, err := range []error{telebot.ErrSameMessageContent, telebot.ErrMessageNotModified} {
		if !isNotModifiedError(err) {
			t.Errorf("isNotModifiedError(%v) = false", err)
		}
	}
	if errors.Is(telebot.ErrSameMessageContent, telebot.ErrMessageNotModified) {
		t.Fatal("the two symbols now unify; errors.Is would be safe and the string match can go")
	}
	if isNotModifiedError(errors.New("chat not found")) {
		t.Fatal("unrelated 400 matched")
	}
}
