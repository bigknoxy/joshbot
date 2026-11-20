package demo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// providerRequest is the smallest request the scripted provider accepts.
func providerRequest() providers.ChatRequest {
	return providers.ChatRequest{Model: "demo"}
}

// The demo's whole promise is that it needs nothing: no key, no network, no
// account. If it ever grows a dependency on one of those, this fails.
func TestRunNeedsNoProviderKeyOrNetwork(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer

	answer, err := Run(context.Background(), workspace, &out)
	if err != nil {
		t.Fatalf("the demo failed: %v", err)
	}
	if answer == "" {
		t.Error("the demo produced no answer")
	}
}

// The tool call has to be the real one: the file must exist, with the
// content the script wrote, and the read must have returned it.
func TestRunReallyWritesAndReadsTheFile(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer

	if _, err := Run(context.Background(), workspace, &out); err != nil {
		t.Fatalf("the demo failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "hello.txt"))
	if err != nil {
		t.Fatalf("the demo did not write hello.txt: %v", err)
	}
	if !strings.Contains(string(data), "Hello from joshbot!") {
		t.Errorf("hello.txt does not hold the greeting: %q", data)
	}
}

// The transcript is the demo's product: it has to show the turn, the tool
// calls, the tool's real output and the answer.
func TestRunNarratesTheTurn(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer

	if _, err := Run(context.Background(), workspace, &out); err != nil {
		t.Fatalf("the demo failed: %v", err)
	}
	transcript := out.String()

	for _, want := range []string{
		"you",
		"tool call",
		"write_file",
		"read_file",
		"Hello from joshbot!",
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("the transcript does not mention %q:\n%s", want, transcript)
		}
	}
}

// The scripted provider must not run out of script during the demo: a
// provider that answers every turn with the same thing would be a fake.
func TestScriptedProviderReplaysEveryStep(t *testing.T) {
	steps := Script()
	p := NewScriptedProvider(nil, steps...)

	for i := range steps {
		if _, err := p.Chat(context.Background(), providerRequest()); err != nil {
			t.Fatalf("step %d failed: %v", i, err)
		}
	}
	if _, err := p.Chat(context.Background(), providerRequest()); err == nil {
		t.Error("the provider should refuse to invent an answer past the end of the script")
	}
}
