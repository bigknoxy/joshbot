package providers

import (
	"strings"
	"testing"
)

func TestSupportsDocumentsFailsClosed(t *testing.T) {
	capable := []string{
		"gpt-4o", "openai/gpt-4o-mini", "openrouter/openai/gpt-4.1", "gpt-5",
		"anthropic/claude-sonnet-4-20250514", "claude-3-5-sonnet-latest",
		"claude-opus-5", "claude-haiku-4",
	}
	for _, m := range capable {
		if !SupportsDocuments(m) {
			t.Errorf("SupportsDocuments(%q) = false, want true", m)
		}
	}

	// The document list is deliberately not the vision list: these all read
	// images and none of them accept a PDF part.
	incapable := []string{
		"", "ollama/llava", "ollama/qwen2.5vl:3b", "moondream", "gemini-2.5-pro",
		"pixtral-12b", "llama-4-scout", "z-ai/glm-5.2", "some-model-nobody-heard-of",
		"claude-3-opus-20240229",
	}
	for _, m := range incapable {
		if SupportsDocuments(m) {
			t.Errorf("SupportsDocuments(%q) = true, want false (fail closed)", m)
		}
	}
}

// TestErrDocumentsUnsupportedNamesTheModel is acceptance criterion 2: the
// refusal has to tell the operator which model to change.
func TestErrDocumentsUnsupportedNamesTheModel(t *testing.T) {
	err := &ErrDocumentsUnsupported{Models: []string{"ollama/llava", "z-ai/glm-5.2"}}
	got := err.Error()
	for _, want := range []string{"ollama/llava", "z-ai/glm-5.2", "agents.defaults.model"} {
		if !strings.Contains(got, want) {
			t.Errorf("error message must contain %q, got %q", want, got)
		}
	}

	if !strings.Contains((&ErrDocumentsUnsupported{}).Error(), "the configured model") {
		t.Error("an empty model list must still produce a usable message")
	}
}

func TestRequestHasDocuments(t *testing.T) {
	if RequestHasDocuments(ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}) {
		t.Error("a text-only request reports documents")
	}
	req := ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleUser, Documents: []Document{{MIME: MIMEPDF, Data: []byte("%PDF-")}}},
	}}
	if !RequestHasDocuments(req) {
		t.Error("a request carrying a document reports none")
	}
}
