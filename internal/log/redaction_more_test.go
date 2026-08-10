package log

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Redaction has to sit on the writer, below the formatter, or it only covers
// whichever formatter it was tested with. JSON mode is what a deployed gateway
// runs, and it escapes and re-quotes every value — a redaction applied at the
// key/value stage instead would leave the JSON path untouched and leak a
// credential into exactly the logs that get shipped somewhere.
//
// The output must also still parse: redaction that corrupts the JSON turns a
// log pipeline into a stream of parse errors, which is how it ends up disabled.
func TestJSONLogOutputIsRedactedAndStillParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "json.log")
	logger, err := NewLogger(Config{
		Level:     DebugLevel,
		JSON:      true,
		File:      path,
		Prefix:    "test",
		Timestamp: false,
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	const secret = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"
	logger.Debug("tool result", "output", "cat config.yml gave "+secret)
	logger.Info("assignment", "line", "OPENAI_API_KEY=abc123def456ghi")
	for _, h := range logger.handlers {
		_ = h.Close()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, leaked := range []string{secret, "abc123def456ghi"} {
		if strings.Contains(content, leaked) {
			t.Errorf("secret %q reached the JSON log:\n%s", leaked, content)
		}
	}

	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("redaction produced unparseable JSON: %v\nline: %s", err, line)
		}
		if rec["msg"] == nil {
			t.Errorf("redaction removed the record's message: %s", line)
		}
		lines++
	}
	if lines != 2 {
		t.Errorf("expected 2 JSON records, got %d:\n%s", lines, content)
	}
}

// The formatted variants build their message with Sprintf before handing it to
// the logger. That is exactly the shape that tempts someone to write straight
// to os.Stderr and bypass the redacting writer — and the formatted calls are
// the ones used for error paths, which is where a provider response carrying a
// key gets logged.
func TestFormattedLogVariantsAreRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fmt.log")
	logger, err := NewLogger(Config{
		Level:     DebugLevel,
		File:      path,
		Prefix:    "test",
		Timestamp: false,
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	const secret = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"
	logger.Warnf("provider rejected the key %s", secret)
	logger.Errorf("request failed with header Authorization: Bearer %s", "abc123def456ghi")
	// A child logger must not escape the redacting writer either.
	logger.With("component", "provider").Errorf("retrying with %s", secret)

	for _, h := range logger.handlers {
		_ = h.Close()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, leaked := range []string{secret, "abc123def456ghi"} {
		if strings.Contains(content, leaked) {
			t.Errorf("secret %q reached the log via a formatted call:\n%s", leaked, content)
		}
	}
	if !strings.Contains(content, "provider rejected the key") {
		t.Errorf("the formatted records did not reach the log at all:\n%s", content)
	}
	if strings.Count(content, "[REDACTED]") < 3 {
		t.Errorf("expected every formatted record to be redacted, got:\n%s", content)
	}
}

// The scheme is kept and the token redacted, never the reverse. An earlier
// version recognised only Bearer|Basic, so GitHub's own `Authorization: Token
// <secret>` fell through to the assignment rule, which blanked the word "Token"
// and published the credential after it. joshbot's own log is where that lands.
func TestAuthorizationSchemeIsKeptAndOnlyTheTokenRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.log")
	logger, err := NewLogger(Config{Level: DebugLevel, File: path, Prefix: "test", Timestamp: false})
	if err != nil {
		t.Fatal(err)
	}

	logger.Info("gh call", "header", "Authorization: Token ghp_AbCdEfGhIjKlMnOpQrSt")
	for _, h := range logger.handlers {
		_ = h.Close()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "ghp_AbCdEfGhIjKlMnOpQrSt") {
		t.Fatalf("a non-Bearer Authorization credential was published verbatim:\n%s", content)
	}
	if !strings.Contains(content, "Token") {
		t.Errorf("the scheme word was redacted instead of the credential, which is backwards:\n%s", content)
	}
}
