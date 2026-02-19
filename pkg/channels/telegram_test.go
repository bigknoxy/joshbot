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
