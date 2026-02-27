package channels

import (
	"bytes"
	"log"
	"regexp"
	"testing"
)

// Test that empty messages are not sent and instrumentation is logged
func TestSendTelegram_EmptyMessage(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	msg := &OutboundMessage{Text: ""}
	err := SendTelegram(msg)
	if err != nil {
		t.Fatalf("SendTelegram returned error: %v", err)
	}

	out := buf.String()
	// should contain a log line with length=0 and a trace_id
	lengthRe := regexp.MustCompile(`length=0`)
	traceRe := regexp.MustCompile(`trace_id=[0-9a-fA-F-]{36}`)

	if !lengthRe.MatchString(out) {
		t.Fatalf("expected log to contain length=0; got: %s", out)
	}
	if !traceRe.MatchString(out) {
		t.Fatalf("expected log to contain trace_id; got: %s", out)
	}
}

// Test that non-empty messages are marked sent and trace_id logged
func TestSendTelegram_NonEmptyMessage(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	msg := &OutboundMessage{Text: "hello"}
	err := SendTelegram(msg)
	if err != nil {
		t.Fatalf("SendTelegram returned error: %v", err)
	}

	out := buf.String()
	if msg.Metadata["sent"] != "true" {
		t.Fatalf("expected message to be marked sent in metadata")
	}
	// verify instrumentation log
	lengthRe := regexp.MustCompile(`length=5`)
	traceRe := regexp.MustCompile(`trace_id=[0-9a-fA-F-]{36}`)
	if !lengthRe.MatchString(out) || !traceRe.MatchString(out) {
		t.Fatalf("expected instrumentation logs present; got: %s", out)
	}
}

func TestSendTelegram_TraceIDUniqueness(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	// Send multiple messages - each should get unique trace ID
	msg1 := &OutboundMessage{Text: "hello"}
	msg2 := &OutboundMessage{Text: "world"}
	msg3 := &OutboundMessage{Text: "test"}

	SendTelegram(msg1)
	SendTelegram(msg2)
	SendTelegram(msg3)

	if msg1.Metadata["trace_id"] == "" {
		t.Error("msg1 should have trace_id")
	}
	if msg2.Metadata["trace_id"] == "" {
		t.Error("msg2 should have trace_id")
	}
	if msg3.Metadata["trace_id"] == "" {
		t.Error("msg3 should have trace_id")
	}

	// All should be unique
	if msg1.Metadata["trace_id"] == msg2.Metadata["trace_id"] {
		t.Error("trace IDs should be unique")
	}
	if msg2.Metadata["trace_id"] == msg3.Metadata["trace_id"] {
		t.Error("trace IDs should be unique")
	}
}

func TestSendTelegram_SHA256Digest(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	msg := &OutboundMessage{Text: "hello"}
	SendTelegram(msg)

	out := buf.String()
	// "hello" should produce a specific SHA256 - let's just verify the format
	sha256Re := regexp.MustCompile(`sha256=[0-9a-f]{64}`)
	if !sha256Re.MatchString(out) {
		t.Fatalf("expected log to contain sha256 digest; got: %s", out)
	}
}

func TestSendTelegram_MetadataInitialized(t *testing.T) {
	// Test that Metadata is initialized if nil
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	msg := &OutboundMessage{Text: "test"}
	err := SendTelegram(msg)
	if err != nil {
		t.Fatalf("SendTelegram returned error: %v", err)
	}

	if msg.Metadata == nil {
		t.Error("Metadata should be initialized")
	}
}

func TestSendTelegram_LongMessage(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	// Test with a long message
	longText := string(bytes.Repeat([]byte("a"), 10000))
	msg := &OutboundMessage{Text: longText}
	err := SendTelegram(msg)
	if err != nil {
		t.Fatalf("SendTelegram returned error: %v", err)
	}

	out := buf.String()
	lengthRe := regexp.MustCompile(`length=10000`)
	if !lengthRe.MatchString(out) {
		t.Fatalf("expected log to contain length=10000; got: %s", out)
	}
}

func TestSendTelegram_SpecialCharacters(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	// Test with special characters
	msg := &OutboundMessage{Text: "hello\n\r\t\"'\\世界🚀"}
	err := SendTelegram(msg)
	if err != nil {
		t.Fatalf("SendTelegram returned error: %v", err)
	}

	out := buf.String()
	lengthRe := regexp.MustCompile(`length=\d+`)
	if !lengthRe.MatchString(out) {
		t.Fatalf("expected log to contain length; got: %s", out)
	}
}

func TestSendTelegram_SkipEmptyLogsSkippingSend(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	msg := &OutboundMessage{Text: ""}
	err := SendTelegram(msg)
	if err != nil {
		t.Fatalf("SendTelegram returned error: %v", err)
	}

	out := buf.String()
	// Should have both instrumentation and skip logs
	skipRe := regexp.MustCompile(`skipping send because message is empty`)
	if !skipRe.MatchString(out) {
		t.Fatalf("expected log to contain skipping message; got: %s", out)
	}

	// Should NOT be marked sent
	if msg.Metadata["sent"] == "true" {
		t.Error("empty message should not be marked sent")
	}
}

func TestOutboundMessage(t *testing.T) {
	// Basic structural test
	msg := OutboundMessage{
		Text:     "test",
		Metadata: map[string]string{"key": "value"},
	}

	if msg.Text != "test" {
		t.Errorf("Text = %q, want %q", msg.Text, "test")
	}
	if msg.Metadata["key"] != "value" {
		t.Errorf("Metadata[key] = %q, want %q", msg.Metadata["key"], "value")
	}
}
