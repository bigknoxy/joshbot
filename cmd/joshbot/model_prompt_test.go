package main

import (
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// The model picker is the last step of onboarding, and every branch of it
// chooses what the agent will dial for the rest of its life. Pressing Enter
// must yield the *default*, not model 1; a number must index the currently
// filtered list, not the unfiltered one; and a filter that matches nothing must
// fall back to the full list rather than leaving an empty list a later number
// would index out of range.
func TestPromptModelSelection(t *testing.T) {
	models := []string{
		"anthropic/claude-sonnet-4",
		"openai/gpt-4o",
		"openai/gpt-4o-mini",
		"meta/llama-3-70b",
	}
	const def = "openai/gpt-4o"

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"enter takes the default, not the first entry", "\n", def},
		{"whitespace-only is still the default", "   \n", def},
		{"a number selects that model", "1\n", "anthropic/claude-sonnet-4"},
		{"out-of-range number is treated as a filter, not an index", "9\nllama\n1\n", "meta/llama-3-70b"},
		{"filter then number indexes the filtered list", "gpt\n2\n", "openai/gpt-4o-mini"},
		{"a filter matching exactly one, repeated, selects it", "llama\nllama\n", "meta/llama-3-70b"},
		{"no match falls back to the whole list", "zzz\n1\n", "anthropic/claude-sonnet-4"},
		{"filter then enter still gives the default", "gpt\n\n", def},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStdinInput(t, tt.input)
			var got string
			captureStdout(t, func() { got = promptModelSelection(models, def) })
			if got != tt.want {
				t.Errorf("promptModelSelection(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// With nothing to choose from the picker must return the default rather than
// blocking on a prompt listing zero models — onboarding against a provider
// whose /models call came back empty would otherwise hang.
func TestPromptModelSelection_EmptyListReturnsDefaultWithoutReading(t *testing.T) {
	withStdinInput(t, "") // an immediate EOF: reading at all would return ""
	var got string
	out := captureStdout(t, func() { got = promptModelSelection(nil, "fallback/model") })
	if got != "fallback/model" {
		t.Errorf("promptModelSelection(nil) = %q, want the default", got)
	}
	if strings.Contains(out, "Available models") {
		t.Errorf("an empty list still printed a chooser:\n%s", out)
	}
}

// The Ollama picker accepts a raw model name as well as a number, because a
// locally pulled model may not be in the listing yet. Returning "" for a typed
// name (or indexing with it) would write an empty model into the config.
func TestPromptOllamaModelSelection(t *testing.T) {
	models := []providers.ModelInfo{
		{Name: "llama3:8b", Size: 4 * 1024 * 1024 * 1024},
		{Name: "qwen2:7b"},
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"number selects", "2\n", "qwen2:7b"},
		{"raw name passes through", "mistral:latest\n", "mistral:latest"},
		{"out-of-range number is taken as a name", "7\n", "7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStdinInput(t, tt.input)
			var got string
			out := captureStdout(t, func() { got = promptOllamaModelSelection(models) })
			if got != tt.want {
				t.Errorf("promptOllamaModelSelection(%q) = %q, want %q", tt.input, got, tt.want)
			}
			// The size is the only way to tell a 4GB model from a 40GB one
			// before pulling it into memory.
			if !strings.Contains(out, "4.0 GB") {
				t.Errorf("listing omitted the model size:\n%s", out)
			}
		})
	}
}

// An empty listing means Ollama is running but has no models pulled. Returning
// "" lets the caller say so; prompting over an empty list would read a name the
// server cannot serve.
func TestPromptOllamaModelSelection_EmptyListReturnsEmpty(t *testing.T) {
	withStdinInput(t, "llama3\n")
	var got string
	captureStdout(t, func() { got = promptOllamaModelSelection(nil) })
	if got != "" {
		t.Errorf("promptOllamaModelSelection(nil) = %q, want \"\"", got)
	}
}
