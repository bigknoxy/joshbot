package redact

import (
	"strings"
	"testing"
	"time"
)

// pinHome fixes the home directory for a test so a parallel test cannot see a
// mutated process environment.
func pinHome(t *testing.T, dir string) {
	t.Helper()
	prev := homeDir
	homeDir = func() string { return dir }
	t.Cleanup(func() { homeDir = prev })
}

// Each pattern gets a positive case and a near-miss that must survive intact.
// The near-misses are the point: an over-eager redactor makes ordinary output
// unreadable, which is how redaction gets turned off.
func TestStringPatterns(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	cases := []struct {
		name    string
		in      string
		redacts string // substring that must be gone; "" means nothing is removed
		keeps   string // substring that must survive
	}{
		// --- vendor key shapes -------------------------------------------
		{"anthropic key", "key sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv done", "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv", "done"},
		{"openrouter key", "sk-or-v1-0123456789abcdef0123456789abcdef", "sk-or-v1-0123456789abcdef", ""},
		{"openai key", "sk-proj0123456789abcdefghij0123", "sk-proj0123456789abcdefghij0123", ""},
		{"github pat", "github_pat_11ABCDEFG0abcdefghijklmno", "github_pat_11ABCDEFG0abcdefghijklmno", ""},
		{"github oauth", "gho_0123456789abcdefghijklmnopqrstuv", "gho_0123456789abcdefghijklmnopqrstuv", ""},
		{"slack bot", "xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij", ""},
		{"google key", "AIzaSyA0123456789abcdefghijklmnopqrstu", "AIzaSyA0123456789abcdefghijklmnopqrstu", ""},
		{"nvidia key", "nvapi-0123456789abcdefghijklmno", "nvapi-0123456789abcdefghijklmno", ""},
		{"groq key", "gsk_0123456789abcdefghijklmnop", "gsk_0123456789abcdefghijklmnop", ""},
		{"aws access key", "AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE", ""},

		// near-misses for the shape rules
		{"short sk word", "the sk-1 flag", "", "sk-1"},
		{"prose mentioning sk", "use sk- prefixed keys", "", "sk-"},
		{"git sha untouched", "commit 675e820aa3b4c5d6e7f8091a2b3c4d5e6f708192", "", "675e820aa3b4c5d6e7f8091a2b3c4d5e6f708192"},
		{"uuid untouched", "id 550e8400-e29b-41d4-a716-446655440000", "", "550e8400-e29b-41d4-a716-446655440000"},
		{"aws-like word", "AKIASHORT", "", "AKIASHORT"},

		// --- credential-shaped assignments -------------------------------
		{"json api_key", `{"api_key": "hunter2secretvalue"}`, "hunter2secretvalue", "api_key"},
		{"yaml token", "token: abc123def456", "abc123def456", "token"},
		{"env assignment", "OPENAI_API_KEY=abc123def456", "abc123def456", "OPENAI_API_KEY"},
		{"password field", "password = correct-horse", "correct-horse", "password"},
		{"hyphen field", "api-key: abc123def456", "abc123def456", "api-key"},
		{"nested secret", `{"db": {"secret": "s3cr3tvalue"}}`, "s3cr3tvalue", "db"},

		// near-misses for the assignment rule
		{"prose token", "the token is stored securely", "", "the token is stored securely"},
		{"word key alone", "key: value", "", "value"},
		{"keyboard layout", "KEYBOARD_LAYOUT=us", "", "us"},

		// --- authorization headers ---------------------------------------
		{"bearer", "Authorization: Bearer abc123def456ghi", "abc123def456ghi", "Bearer"},
		{"basic", "Authorization: Basic dXNlcjpwYXNz", "dXNlcjpwYXNz", "Basic"},
		{"lowercase header", "authorization: bearer abc123def456ghi", "abc123def456ghi", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := String(tc.in)
			if tc.redacts != "" && strings.Contains(got, tc.redacts) {
				t.Errorf("secret survived redaction:\n in:  %q\n out: %q\n want %q gone", tc.in, got, tc.redacts)
			}
			if tc.redacts == "" && got != tc.in {
				t.Errorf("ordinary text was altered:\n in:  %q\n out: %q", tc.in, got)
			}
			if tc.keeps != "" && !strings.Contains(got, tc.keeps) {
				t.Errorf("redaction removed too much:\n in:  %q\n out: %q\n want %q kept", tc.in, got, tc.keeps)
			}
		})
	}
}

// Running redaction twice must not change the result, or repeated logging
// would accumulate placeholders.
func TestIdempotent(t *testing.T) {
	pinHome(t, "/home/tester")

	inputs := []string{
		`{"api_key": "hunter2secretvalue"}`,
		"token: abc123def456",
		"Authorization: Bearer abc123def456ghi",
		"sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv",
		"OPENAI_API_KEY=abc123def456",
		"/home/tester/.joshbot/config.json",
		"nothing to see here",
		"",
	}
	for _, in := range inputs {
		once := String(in)
		twice := String(once)
		if once != twice {
			t.Errorf("not idempotent:\n in:    %q\n once:  %q\n twice: %q", in, once, twice)
		}
	}
}

// Ordinary prose and code must pass through byte-identical.
func TestOrdinaryTextUnchanged(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	corpus := []string{
		"func main() { fmt.Println(\"hello\") }",
		"The quick brown fox jumps over the lazy dog.",
		"go test -race ./... # runs everything",
		"error: connection refused (dial tcp 127.0.0.1:8099)",
		"| column | value |\n|---|---|\n| a | 1 |",
		"session key is channel:senderID",
		// Words that merely contain a credential fragment are not assignments.
		// Each of these was rewritten before the name and value classes were
		// tightened, and all are routine tool output.
		"Author: Josh Knox <j@example.com>",
		`{"author": "josh", "version": "1.0"}`,
		"unauthorized: request failed",
		"authenticated=true",
		"secretariat: horse",
		"tokens=[1,2,3]",
		"authors = [alice, bob]",
		"we walked through the bright highlights of the night",
	}
	for _, s := range corpus {
		if got := String(s); got != s {
			t.Errorf("ordinary text altered:\n in:  %q\n out: %q", s, got)
		}
	}
}

// A redacted JSON document must still parse — redaction that breaks structure
// is redaction that gets bypassed.
func TestJSONStaysWellFormed(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	in := `{"provider":"openai","api_key":"sk-proj0123456789abcdefghij0123","model":"gpt-4o"}`
	got := String(in)
	if strings.Contains(got, "sk-proj0123456789abcdefghij0123") {
		t.Fatalf("key survived: %s", got)
	}
	for _, want := range []string{`"provider":"openai"`, `"model":"gpt-4o"`, `"api_key":"` + Placeholder + `"`} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in %q", want, got)
		}
	}
}

func TestHomePathReplaced(t *testing.T) {
	pinHome(t, "/home/alice")

	got := String("config at /home/alice/.joshbot/config.json")
	if strings.Contains(got, "/home/alice") {
		t.Errorf("home path survived: %q", got)
	}
	if !strings.Contains(got, "~/.joshbot/config.json") {
		t.Errorf("expected a ~-rooted path, got %q", got)
	}
}

// An empty or root home must not turn every "/" into "~".
func TestDegenerateHomeIgnored(t *testing.T) {
	for _, home := range []string{"", "/"} {
		pinHome(t, home)
		in := "/usr/local/bin/joshbot"
		if got := String(in); got != in {
			t.Errorf("home=%q rewrote an unrelated path: %q -> %q", home, in, got)
		}
	}
}

func TestBytes(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	if got := Bytes(nil); got != nil {
		t.Errorf("Bytes(nil) = %v, want nil", got)
	}
	got := Bytes([]byte("token: abc123def456"))
	if strings.Contains(string(got), "abc123def456") {
		t.Errorf("secret survived: %s", got)
	}
}

// The log path runs redaction on every line, so it must not be a bottleneck.
func TestPerformanceOnLargeInput(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	// 1 MB of realistic mixed content.
	//
	// The corpus must contain the words that fire the cheap gates, or the
	// benchmark measures the one input that skips every regex. An earlier
	// version used prose with no "auth" and no GitHub prefix in it and so
	// asserted the budget against the fastest possible case.
	block := "2026-08-03 12:00:00 INFO tool result: the author walked through the " +
		"bright highlights; unauthorized request to github failed, token missing\n"
	var sb strings.Builder
	for sb.Len() < 1<<20 {
		sb.WriteString(block)
	}
	input := sb.String()

	start := time.Now()
	String(input)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("redacting 1MB took %v, want under 100ms", elapsed)
	}
}

// The fragment list is shared with internal/tools; an empty one would silently
// disable both.
func TestSecretNameFragmentsNonEmpty(t *testing.T) {
	if len(SecretNameFragments) == 0 {
		t.Fatal("SecretNameFragments is empty; environment screening and redaction both depend on it")
	}
	for _, f := range SecretNameFragments {
		if f != strings.ToUpper(f) {
			t.Errorf("fragment %q must be upper-case; matching upper-cases the input", f)
		}
	}
}

// Every credential must be removed regardless of the auth scheme, the
// separator, or how Go rendered the surrounding structure.
//
// The rule these pin down is that the scheme is preserved and the token is not.
// An allowlist of bearer|basic sent every other scheme to the assignment rule,
// which blanked the scheme and published the token after it.
func TestCredentialIsRemovedNotTheSchemeInFrontOfIt(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	const secret = "abc123secretvalue"
	inputs := []string{
		"Authorization: Bearer " + secret,
		"Authorization: Token " + secret,                          // GitHub's scheme
		"Authorization: ApiKey " + secret,                         // Azure-style
		"Authorization=Bearer " + secret,                          // = rather than :
		"authorization: " + secret,                                // no scheme at all
		"map[Authorization:[Bearer " + secret + "] Accept:[*/*]]", // %v of an http.Header
		"AUTH_TOKEN=Bearer " + secret,
		"api-key: " + secret,
		`{"api_key": "` + secret + `"}`,
	}
	for _, in := range inputs {
		got := String(in)
		if strings.Contains(got, secret) {
			t.Errorf("credential survived redaction:\n in:  %q\n out: %q", in, got)
		}
	}
}

// joshbot's own Telegram bot token appears in transport error lines as part of
// the api.telegram.org URL, and it is the credential most likely to be logged.
func TestTelegramBotTokenIsRemoved(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	in := "post https://api.telegram.org/bot123456789:AAEhBOweik6ad9r_QwertyuiopASDFGHJKLzx/sendMessage: timeout"
	got := String(in)
	if strings.Contains(got, "AAEhBOweik6ad9r_QwertyuiopASDFGHJKLzx") {
		t.Errorf("bot token survived redaction: %q", got)
	}
}

// A numeric setting is not a credential.
//
// `joshbot status` prints "Max tokens:     8192". TOKEN plus the plural "s"
// makes that label look like a credential-shaped assignment, so the value was
// replaced with [REDACTED] and the operator could not read their own config.
func TestNumericSettingsAreNotRedacted(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	for _, s := range []string{
		"Max tokens:     8192",
		"max_tokens: 8192",
		"session_key_rotation_secs = 3600",
		"tokens: 1_000",
		"secret_count: -1",
	} {
		if got := String(s); got != s {
			t.Errorf("numeric setting was redacted:\n in:  %q\n out: %q", s, got)
		}
	}
}

// The numeric exemption must not become a way to smuggle a real credential.
func TestNonNumericValuesAreStillRedacted(t *testing.T) {
	pinHome(t, "/home/nobody-unlikely-path")

	for _, s := range []string{
		"api_key: sk-or-v1-abcdefghijklmnopqrst",
		"password: hunter2secret",
		"token: 8192abc",
		"secret: 123-abc",
	} {
		if got := String(s); got == s {
			t.Errorf("credential survived redaction: %q", got)
		}
	}
}
