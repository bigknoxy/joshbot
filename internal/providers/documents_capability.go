package providers

import (
	"fmt"
	"strings"
)

// documentModelSubstrings marks the model families joshbot knows accept a PDF
// as a file part on an OpenAI-compatible chat endpoint. Matching is on a
// lower-cased substring of the full model spec, for the same reason
// visionModelSubstrings is: the same model arrives bare (`gpt-4o`),
// routing-prefixed (`openai/gpt-4o`) and vendor-namespaced on OpenRouter
// (`openrouter/openai/gpt-4o`).
//
// This list is deliberately NOT the vision list. Reading an image and reading a
// PDF are different capabilities: llava, moondream and the Qwen-VL family all
// accept images and none of them accept a document part. Every entry below is
// cited from the vendor's own documentation:
//
//   - OpenAI, "PDF file inputs": PDF parsing "requires models with vision
//     capabilities, such as gpt-4o and later models".
//     https://developers.openai.com/api/docs/guides/pdf-files
//   - Anthropic, "PDF support": "All active models support PDF processing."
//     The active list is Claude 3.5 and later.
//     https://platform.claude.com/docs/en/build-with-claude/pdf-support
//
// An entry nobody can cite does not belong here. The cost of a wrong "yes" is a
// provider 400 that reads as a joshbot bug; the cost of a wrong "no" is an
// error naming exactly which model to change.
var documentModelSubstrings = []string{
	// OpenAI
	"gpt-4o", "gpt-4.1", "gpt-5",
	// Anthropic — every active Claude model, i.e. 3.5 and later. The retired
	// original Claude 3 models are deliberately absent, so a bare "claude-3"
	// substring is not used.
	"claude-3-5", "claude-3.5", "claude-3-7", "claude-3.7",
	"claude-sonnet-4", "claude-opus-4", "claude-haiku-4",
	"claude-sonnet-5", "claude-opus-5", "claude-fable-5",
}

// SupportsDocuments reports whether a model spec is known to accept a document
// (PDF) attachment.
//
// An unknown model is reported as not document-capable. Failing closed is the
// whole point, exactly as in SupportsVision: guessing "yes" turns a typo into a
// provider 400 mid-conversation, while guessing "no" produces an error naming
// the model at the moment the operator is holding the file.
func SupportsDocuments(modelSpec string) bool {
	spec := strings.ToLower(strings.TrimSpace(modelSpec))
	if spec == "" {
		return false
	}
	for _, s := range documentModelSubstrings {
		if strings.Contains(spec, s) {
			return true
		}
	}
	return false
}

// RequestHasDocuments reports whether any message in the request carries a
// document.
func RequestHasDocuments(req ChatRequest) bool {
	for _, m := range req.Messages {
		if len(m.Documents) > 0 {
			return true
		}
	}
	return false
}

// ErrDocumentsUnsupported is returned when a document is attached to a request
// no configured model can accept. Like ErrVisionUnsupported it is produced
// before any network call, and it names the models tried and the config key to
// change — "unsupported" without either is a dead end for the operator.
type ErrDocumentsUnsupported struct {
	Models []string
}

func (e *ErrDocumentsUnsupported) Error() string {
	models := "the configured model"
	if len(e.Models) > 0 {
		models = strings.Join(e.Models, ", ")
	}
	return fmt.Sprintf("this message has a PDF attached, but %s cannot read documents; "+
		"set agents.defaults.model (or a profile) to a document-capable model such as "+
		"openai/gpt-4o or anthropic/claude-sonnet-4-20250514", models)
}
