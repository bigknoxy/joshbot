package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Debug logging includes tool results, and a tool result routinely carries a
// credential nobody meant to expose. The log file is also what people paste
// into bug reports, so this is the boundary that matters.
func TestLogFileIsRedacted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

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
	logger.Debug("tool result", "output", "cat config.yml gave "+secret)
	logger.Info("assignment", "line", "OPENAI_API_KEY=abc123def456ghi")
	logger.Warn("header", "h", "Authorization: Bearer abc123def456ghi")

	for _, h := range logger.handlers {
		_ = h.Close()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)

	for _, leaked := range []string{secret, "abc123def456ghi"} {
		if strings.Contains(content, leaked) {
			t.Errorf("secret %q reached the log file:\n%s", leaked, content)
		}
	}
	if !strings.Contains(content, "[REDACTED]") {
		t.Errorf("expected a redaction placeholder in the log, got:\n%s", content)
	}
	// The surrounding record must still be readable.
	if !strings.Contains(content, "tool result") {
		t.Errorf("redaction destroyed the log record:\n%s", content)
	}
}

// Ordinary log output must be unaffected, or redaction becomes the thing people
// turn off.
func TestOrdinaryLogOutputUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.log")

	logger, err := NewLogger(Config{Level: InfoLevel, File: path, Timestamp: false})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	logger.Info("processed message", "elapsed", 1.5, "response_len", 42)
	for _, h := range logger.handlers {
		_ = h.Close()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "[REDACTED]") {
		t.Errorf("ordinary log line was redacted:\n%s", content)
	}
	for _, want := range []string{"processed message", "response_len", "42"} {
		if !strings.Contains(content, want) {
			t.Errorf("expected %q in log output:\n%s", want, content)
		}
	}
}
