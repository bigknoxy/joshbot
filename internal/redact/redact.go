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
	`sk-ant-[A-Za-z0-9_-]{16,}`,     // Anthropic
	`sk-or-[A-Za-z0-9_-]{16,}`,      // OpenRouter
	`sk-[A-Za-z0-9_-]{20,}`,         // OpenAI and compatible
	`github_pat_[A-Za-z0-9_]{20,}`,  // GitHub fine-grained PAT
	`gh[pousr]_[A-Za-z0-9]{20,}`,    // GitHub classic/OAuth/app
	`xox[baprs]-[A-Za-z0-9-]{10,}`,  // Slack
	`AIza[0-9A-Za-z_-]{30,}`,        // Google
	`nvapi-[A-Za-z0-9_-]{20,}`,      // NVIDIA
	`gsk_[A-Za-z0-9]{20,}`,          // Groq
	`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`, // AWS access key IDs
}, "|"))

// keyShapeHints are the cheap substrings that gate the expensive scan. If none
// of them appear, no alternative above can match, so the regex is skipped.
var keyShapeHints = []string{"sk-", "github_pat_", "gh", "xox", "aiza", "nvapi-", "gsk_", "akia", "asia"}

// authHeader matches an Authorization header value of either common scheme.
var authHeader = regexp.MustCompile(`(?i)(authorization\s*:\s*)(bearer|basic)(\s+)(\S+)`)

// assignment matches `name = value`, `name: value` and `"name": "value"` where
// the name is credential-shaped.
//
// The value class stops at whitespace and structural punctuation so that a JSON
// or YAML document keeps its shape after redaction. Quotes are handled by the
// optional groups rather than by consuming them, so `"api_key": "x"` becomes
// `"api_key": "[REDACTED]"` and stays valid JSON.
var assignment = buildAssignmentRe()

func buildAssignmentRe() *regexp.Regexp {
	frags := make([]string, 0, len(SecretNameFragments))
	for _, f := range SecretNameFragments {
		// Field names use either underscores or none; accept both spellings
		// and an optional hyphen form.
		frags = append(frags, strings.ReplaceAll(regexp.QuoteMeta(f), "_", `[_-]?`))
	}
	// (?i)  case-insensitive
	// group 1: everything up to and including the separator and opening quote
	// group 2: the value
	return regexp.MustCompile(
		`(?i)([A-Za-z0-9_.-]*(?:` + strings.Join(frags, "|") + `)[A-Za-z0-9_.-]*"?\s*[:=]\s*"?)([^\s,;"'` + "`" + `}\]]+)`)
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
			if len(groups) != 5 {
				return m
			}
			if isPlaceholder(groups[4]) {
				return m
			}
			return groups[1] + groups[2] + groups[3] + Placeholder
		})
		lower = strings.ToLower(s)
	}

	// A field name is the strongest signal, so assignments are handled before
	// the shape rules: `api_key=sk-...` becomes one replacement, not two
	// overlapping ones. The scan is skipped entirely when no credential-shaped
	// name appears, which is the common case for log output.
	if containsAnyFold(lower, fragmentHints) {
		s = assignment.ReplaceAllStringFunc(s, func(m string) string {
			groups := assignment.FindStringSubmatch(m)
			if len(groups) != 3 {
				return m
			}
			if isPlaceholder(groups[2]) {
				return m // already redacted; keep idempotent
			}
			// "Authorization" is credential-shaped by name, so this rule also
			// matches an auth header — with the scheme as the "value". The
			// header rule above has already redacted the token that follows;
			// blanking the scheme as well would destroy readable structure
			// while protecting nothing.
			if isAuthScheme(groups[2]) {
				return m
			}
			return groups[1] + Placeholder
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
var fragmentHints = []string{
	"key", "token", "secret", "password", "passwd",
	"credential", "webhook", "auth", "bearer", "passphrase",
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

// isAuthScheme reports whether a captured value is an HTTP auth scheme rather
// than a credential.
func isAuthScheme(v string) bool {
	return strings.EqualFold(v, "bearer") || strings.EqualFold(v, "basic")
}

// isPlaceholder reports whether a captured value is already redacted.
//
// The comparison is a prefix test because the value class stops at `]`, so a
// previously redacted value is captured as "[REDACTED" without its bracket.
func isPlaceholder(v string) bool {
	return strings.HasPrefix(v, strings.TrimSuffix(Placeholder, "]"))
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
