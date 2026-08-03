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
	block := "2026-08-03 12:00:00 INFO tool result: the quick brown fox jumps over the lazy dog\n"
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
