// Package redact removes credentials and host-identifying paths from text
// joshbot is about to display, log or export.
//
// The threat it addresses is not an attacker reading files — file permissions
// cover that. It is the ordinary act of a user copying a log or a status dump
// into a bug report. A shell tool result can contain a credential without
// anyone intending it: the model runs `cat config.yml`, the output carries an
// API key, and it lands verbatim in the debug log.
//
// # Scope
//
// Redaction applies to outputs and displays. It deliberately does NOT rewrite
// session files on disk. Rewriting on save would mangle legitimate content (a
// user genuinely discussing a token format) and would add a second way for a
// session file to be corrupted, which is the failure this codebase has already
// been bitten by. Session files stay verbatim at 0600; anything that leaves the
// machine or is presented for copying goes through here.
//
// # What is deliberately not detected
//
// Bare high-entropy strings. Hashes, base64 payloads, UUIDs and git SHAs are
// indistinguishable from secrets by entropy alone, and redacting them would
// make ordinary output unreadable. Only recognisable key shapes, credential
// -shaped assignments and Authorization headers are matched.
package redact

import (
	"io"
	"os"
	"regexp"
	"strings"
)

// Placeholder replaces every redacted value.
//
// It is a fixed string rather than a length-preserving mask on purpose: a mask
// leaks the length of the secret, which narrows a guess.
const Placeholder = "[REDACTED]"

// SecretNameFragments mark an identifier as credential-shaped. Matched
// case-insensitively as substrings.
//
// These are deliberately specific: a bare "KEY" would match KEYBOARD_LAYOUT and
// similar. This is the single source of truth for the notion — internal/tools
// screens spawned-command environment variables with the same list, and two
// copies would drift.
var SecretNameFragments = []string{
	"API_KEY", "APIKEY", "ACCESS_KEY", "SECRET_KEY", "PRIVATE_KEY",
	"SESSION_KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD",
	"CREDENTIAL", "WEBHOOK", "AUTH", "BEARER", "PASSPHRASE",
}

// keyShape matches credentials recognisable on their own, without a nearby
// field name. Each alternative is anchored on a vendor prefix so ordinary prose
// cannot trip it, and the minimum lengths matter: without them `sk-` in a
// sentence would be redacted.
//
// It is one expression rather than a slice of them so the input is scanned
// once. Ten separate passes over a megabyte of log output measured 5x slower.
var keyShape = regexp.MustCompile(strings.Join([]string{
	`sk-ant-[A-Za-z0-9_-]{16,}`,       // Anthropic
	`sk-or-[A-Za-z0-9_-]{16,}`,        // OpenRouter
	`sk-[A-Za-z0-9_-]{20,}`,           // OpenAI and compatible
	`github_pat_[A-Za-z0-9_]{20,}`,    // GitHub fine-grained PAT
	`gh[pousr]_[A-Za-z0-9]{20,}`,      // GitHub classic/OAuth/app
	`xox[baprs]-[A-Za-z0-9-]{10,}`,    // Slack
	`AIza[0-9A-Za-z_-]{30,}`,          // Google
	`nvapi-[A-Za-z0-9_-]{20,}`,        // NVIDIA
	`gsk_[A-Za-z0-9]{20,}`,            // Groq
	`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`,   // AWS access key IDs
	`bot[0-9]{6,}:[A-Za-z0-9_-]{30,}`, // Telegram bot token, as it appears in an api.telegram.org URL
}, "|"))

// keyShapeHints are the cheap substrings that gate the expensive scan. If none
// of them appear, no alternative above can match, so the regex is skipped.
//
// They must be as narrow as the alternatives they gate. A bare "gh" occurs in
// ordinary English ("through", "right", "highlight"), so it fired the full scan
// on most prose; the five real GitHub prefixes cost nothing extra to list.
var keyShapeHints = []string{
	"sk-", "github_pat_", "ghp_", "gho_", "ghu_", "ghs_", "ghr_",
	"xox", "aiza", "nvapi-", "gsk_", "akia", "asia", "bot",
}

// authScheme optionally precedes the credential in an Authorization header or a
// credential-shaped assignment. It is preserved rather than redacted: the scheme
// is not a secret and keeping it makes the redacted line readable.
//
// It is deliberately any bare word, not an allowlist of `bearer|basic`. GitHub's
// own scheme is `Token`, Azure's is `SharedKey`, and an allowlist meant those
// fell through to the assignment rule, which blanked the *scheme* and published
// the credential after it — exactly the failure the ordering is meant to prevent.
const authScheme = `(?:[A-Za-z][A-Za-z0-9_.-]*[ \t]+)?`

// credValue is the value class shared by the rules below.
//
// It stops at whitespace, structural punctuation and both bracket kinds, so a
// JSON or YAML document keeps its shape: without `[` and `{` excluded,
// `tokens=[1,2,3]` was rewritten to `tokens=[REDACTED],2,3]`.
var credValue = `(?:` + regexp.QuoteMeta(Placeholder) + `|[^\s,;"'` + "`" + `{}\[\]]+)`

// The already-redacted placeholder is an explicit alternative so that a second
// pass matches it and can bail out. Without it the value class stops at the
// leading bracket, the rules re-match what is left, and String stops being
// idempotent: "[REDACTED]" became "[[REDACTED]]".

// authHeader matches an Authorization header in either `:` or `=` form, with or
// without a scheme, and whether or not Go's `%v` of an http.Header has wrapped
// the value in brackets.
var authHeader = regexp.MustCompile(`(?i)(authorization"?\s*[:=]\s*"?\[?)(` + authScheme + `)(` + credValue + `)`)

// assignment matches `name = value`, `name: value` and `"name": "value"` where
// the name is credential-shaped.
//
// The value class stops at whitespace and structural punctuation so that a JSON
// or YAML document keeps its shape after redaction. Quotes are handled by the
// optional groups rather than by consuming them, so `"api_key": "x"` becomes
// `"api_key": "[REDACTED]"` and stays valid JSON.
var assignment = buildAssignmentRe()

// assignmentFragments are the name fragments the assignment rule uses.
//
// It is SecretNameFragments minus AUTH. AUTH is a real signal for screening a
// spawned process's environment, where names are whole identifiers, but as a
// substring in free text it is destructive: it rewrote `Author: Josh Knox`,
// `{"author": "josh"}` and `unauthorized: request failed`, all of which are
// routine tool output. Authorization headers are covered by authHeader above,
// and `auth_token=` / `auth_key=` still match through TOKEN and SECRET_KEY.
func assignmentFragments() []string {
	out := make([]string, 0, len(SecretNameFragments))
	for _, f := range SecretNameFragments {
		if strings.EqualFold(f, "AUTH") {
			continue
		}
		out = append(out, f)
	}
	return out
}

func buildAssignmentRe() *regexp.Regexp {
	src := assignmentFragments()
	frags := make([]string, 0, len(src))
	for _, f := range src {
		// Field names use either underscores or none; accept both spellings
		// and an optional hyphen form.
		frags = append(frags, strings.ReplaceAll(regexp.QuoteMeta(f), "_", `[_-]?`))
	}
	// (?i)  case-insensitive
	// group 1: everything up to and including the separator and opening quote
	// group 2: an optional auth scheme, preserved
	// group 3: the value
	// There is no leading identifier class. Anything before the fragment stays
	// in the text untouched either way, since only the matched region is
	// rewritten, and an unanchored leading `[A-Za-z0-9_.-]*` made the scan
	// roughly 4x slower on log-shaped prose for no behavioural gain.
	//
	// The fragment must end the identifier, apart from a plural "s" and further
	// separator-led segments (SECRET_KEY_ID). Allowing arbitrary trailing
	// letters made the rule fire on any word merely containing a fragment:
	// "secretariat: horse" was rewritten to "secretariat: [REDACTED]".
	//
	// No optional "[" is consumed here, unlike authHeader. Swallowing it made
	// the rule reach inside a list and corrupt it — "tokens=[1,2,3]" became
	// "tokens=[[REDACTED],2,3]" — and a bracketed value is a list, not a
	// credential.
	return regexp.MustCompile(
		`(?i)((?:` + strings.Join(frags, "|") + `)s?(?:[_.-][A-Za-z0-9]+)*"?\s*[:=]\s*"?)(` +
			authScheme + `)(` + credValue + `)`)
}

// String returns s with credentials and the host home directory removed.
//
// It is idempotent: String(String(s)) == String(s) for every input.
func String(s string) string {
	if s == "" {
		return s
	}

	lower := strings.ToLower(s)

	// Authorization headers must be handled before the assignment rule.
	// "Authorization" contains the AUTH fragment, so the assignment rule would
	// otherwise treat the *scheme* as the secret and produce
	// "Authorization: [REDACTED] <the actual token>" — redacting the harmless
	// word and publishing the credential.
	if strings.Contains(lower, "authorization") {
		s = authHeader.ReplaceAllStringFunc(s, func(m string) string {
			groups := authHeader.FindStringSubmatch(m)
			if len(groups) != 4 {
				return m
			}
			if isPlaceholder(groups[3]) {
				return m
			}
			// groups[2] is the scheme, kept; groups[3] is the credential.
			return groups[1] + groups[2] + Placeholder
		})
		lower = strings.ToLower(s)
	}

	// A field name is the strongest signal, so assignments are handled before
	// the shape rules: `api_key=sk-...` becomes one replacement, not two
	// overlapping ones. The scan is skipped entirely when no credential-shaped
	// name appears, which is the common case for log output.
	if hasAssignmentCandidate(lower) {
		s = assignment.ReplaceAllStringFunc(s, func(m string) string {
			groups := assignment.FindStringSubmatch(m)
			if len(groups) != 4 {
				return m
			}
			if isPlaceholder(groups[3]) {
				return m // already redacted; keep idempotent
			}
			// groups[2] is an optional scheme word. Capturing it as part of the
			// match rather than stopping at it is what keeps `AUTH_TOKEN=Bearer
			// <secret>` safe: treating "Bearer" as the value would redact the
			// scheme and leave the credential in the clear.
			return groups[1] + groups[2] + Placeholder
		})
		lower = strings.ToLower(s)
	}

	if containsAnyFold(lower, keyShapeHints) {
		s = keyShape.ReplaceAllString(s, Placeholder)
	}

	return HomePath(s)
}

// fragmentHints gate the assignment scan.
//
// They are the separator-free stems of SecretNameFragments, not the fragments
// themselves: the assignment regex accepts "api-key" as well as "api_key", so
// gating on the underscored spelling would skip the hyphenated one and silently
// let `api-key: <secret>` through. Every entry of SecretNameFragments contains
// at least one of these stems, so the gate can never be narrower than the regex.
// "auth" is deliberately absent: AUTH is not an assignment fragment (see
// assignmentFragments), and it is the single most common English stem in this
// list, so gating on it ran the assignment scan over almost every line of prose.
var fragmentHints = []string{
	"key", "token", "secret", "password", "passwd",
	"credential", "webhook", "bearer", "passphrase",
}

// hasAssignmentCandidate reports whether the assignment rule could possibly
// match, using a cheap forward walk instead of the regex.
//
// A bare substring gate was not enough. Go's regexp is slow on a
// case-insensitive alternation of literals — the name part alone measured
// ~650ms per megabyte — and "token", "key" and "secret" appear constantly in
// ordinary log prose without being assignments, so the full scan ran on
// almost every large write.
//
// This walk mirrors the name part of the regex exactly: fragment, optional
// plural, separator-led segments, optional quote, whitespace, then ':' or '='.
// It must never be narrower than the regex, so every relaxation the regex
// allows is allowed here too.
func hasAssignmentCandidate(lower string) bool {
	for _, hint := range fragmentHints {
		from := 0
		for {
			i := strings.Index(lower[from:], hint)
			if i < 0 {
				break
			}
			j := from + i + len(hint)
			from = from + i + 1
			if assignmentFollows(lower, j) {
				return true
			}
		}
	}
	return false
}

// assignmentFollows reports whether an assignment separator follows the
// identifier that ends at j.
func assignmentFollows(lower string, j int) bool {
	n := len(lower)
	if j < n && lower[j] == 's' { // plural: api_keys=
		j++
	}
	for j < n && (lower[j] == '_' || lower[j] == '-' || lower[j] == '.') {
		k := j + 1
		for k < n && isAlnum(lower[k]) {
			k++
		}
		if k == j+1 {
			break // a lone separator is not a segment
		}
		j = k
	}
	if j < n && lower[j] == '"' {
		j++
	}
	for j < n && isSpace(lower[j]) {
		j++
	}
	return j < n && (lower[j] == ':' || lower[j] == '=')
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// containsAnyFold reports whether lowered contains any of the (already
// lower-case) needles.
func containsAnyFold(lowered string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(lowered, n) {
			return true
		}
	}
	return false
}

// isPlaceholder reports whether a captured value is already redacted.
//
// Brackets are trimmed first: depending on which rule matched, a previously
// redacted value arrives as "[REDACTED]", "[REDACTED" or bare "REDACTED",
// because the optional opening bracket in the name group may have consumed it.
func isPlaceholder(v string) bool {
	v = strings.TrimLeft(v, "[")
	return strings.HasPrefix(v, strings.Trim(Placeholder, "[]"))
}

// Bytes is String for byte slices.
func Bytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return []byte(String(string(b)))
}

// HomePath replaces the host home directory with "~".
//
// The home directory carries the account name, which identifies the machine's
// user in anything shared publicly.
func HomePath(s string) string {
	home := homeDir()
	if home == "" || home == "/" {
		return s
	}
	return strings.ReplaceAll(s, home, "~")
}

// homeDir is a variable so tests can pin it without touching the process
// environment of a parallel test.
var homeDir = func() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// Writer wraps w so that everything written through it is redacted first.
//
// Redaction is applied per Write call. The loggers this wraps emit one record
// per Write, so a secret is never split across calls in practice; a writer that
// streamed arbitrary chunks could split a credential across a boundary and miss
// it. That is a deliberate limit, not an oversight — buffering log output to
// scan across writes would reorder records and hold them in memory.
func Writer(w io.Writer) io.Writer { return &redactingWriter{w: w} }

type redactingWriter struct{ w io.Writer }

func (r *redactingWriter) Write(p []byte) (int, error) {
	if _, err := r.w.Write(Bytes(p)); err != nil {
		return 0, err
	}
	// Report the caller's length: redaction changes the byte count, and a short
	// write would be treated as an error by callers that check it.
	return len(p), nil
}
