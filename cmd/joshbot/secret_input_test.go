package main

import (
	"strings"
	"testing"
)

// A secret piped into the wizard must still be accepted. The wizard is
// scriptable and the existing tests drive it with piped input, so masking
// the prompt must not make the prompt unusable without a terminal.
func TestReadSecretAcceptsAPipedValue(t *testing.T) {
	withStdinInput(t, "sk-piped-secret\n")
	got, err := readSecret()
	if err != nil {
		t.Fatalf("readSecret: %v", err)
	}
	if got != "sk-piped-secret" {
		t.Errorf("readSecret = %q, want the piped value", got)
	}
}

// A value pasted with a trailing newline must not carry the newline into the
// next prompt. The newline is the terminator of the secret, not part of it.
func TestReadSecretDropsTheTerminatingNewline(t *testing.T) {
	withStdinInput(t, "sk-pasted\n")
	got, err := readSecret()
	if err != nil {
		t.Fatalf("readSecret: %v", err)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("readSecret = %q, want the newline stripped", got)
	}
}

// An empty answer is an answer: it means "leave it alone", and it must come
// back as an empty string rather than as an error, or the wizard cannot ask
// "press Enter to keep the current key".
func TestReadSecretOnEmptyInput(t *testing.T) {
	withStdinInput(t, "\n")
	got, err := readSecret()
	if err != nil {
		t.Fatalf("readSecret on empty input: %v", err)
	}
	if got != "" {
		t.Errorf("readSecret = %q, want empty", got)
	}
}
