package providers

import (
	"fmt"
	"strings"
)

// visionModelSubstrings marks the model families joshbot knows accept images on
// an OpenAI-compatible chat endpoint. Matching is on a lower-cased substring of
// the full model spec because the same model arrives under several names — bare
// (`gpt-4o`), routing-prefixed (`openai/gpt-4o`) and vendor-namespaced on
// OpenRouter (`openrouter/openai/gpt-4o`).
//
// Each entry was checked against the vendor's published model documentation
// when it was added; the citations are in the PR that introduced this file. An
// entry nobody can cite does not belong here — the cost of a wrong "yes" is a
// provider 400 that reads as a joshbot bug, and the cost of a wrong "no" is an
// error message telling the operator exactly which model to change.
var visionModelSubstrings = []string{
	// OpenAI
	"gpt-4o", "gpt-4.1", "gpt-4-turbo", "gpt-5", "o3", "o4-mini",
	// Anthropic — every Claude 3 and later model accepts images
	"claude-3", "claude-sonnet-4", "claude-opus-4", "claude-haiku-4",
	"claude-sonnet-5", "claude-opus-5", "claude-fable-5",
	// Google
	"gemini",
	// Meta
	"llama-3.2-11b", "llama-3.2-90b", "llama-4",
	// Mistral
	"pixtral",
	// Qwen
	"qwen-vl", "qwen2-vl", "qwen2.5-vl", "qwen3-vl",
	// Ollama-hosted vision models. The unhyphenated spellings are not
	// duplicates: Ollama's own library tags are `llama3.2-vision` and
	// `qwen2.5vl`, neither of which contains the hyphenated vendor spelling
	// above, so without these an `ollama/qwen2.5vl:3b` was refused as text-only.
	// Model names have to be matched as the provider actually spells them, and
	// each entry here was checked against ollama.com/library rather than guessed
	// — a plausible-looking `qwen3vl` does not exist (the tag is `qwen3-vl`).
	"llava", "bakllava", "moondream", "minicpm-v",
	"llama3.2-vision", "qwen2.5vl", "granite3.2-vision",
}

// SupportsVision reports whether a model spec is known to accept images.
//
// An unknown model is reported as not vision-capable. Failing closed is the
// whole point: guessing "yes" turns an unremarkable typo into a provider 400
// twenty seconds into a conversation, while guessing "no" produces an error
// naming the model at the moment the operator is holding the image.
func SupportsVision(modelSpec string) bool {
	spec := strings.ToLower(strings.TrimSpace(modelSpec))
	if spec == "" {
		return false
	}
	for _, s := range visionModelSubstrings {
		if strings.Contains(spec, s) {
			return true
		}
	}
	return false
}

// RequestHasImages reports whether any message in the request carries an image.
func RequestHasImages(req ChatRequest) bool {
	for _, m := range req.Messages {
		if len(m.Images) > 0 {
			return true
		}
	}
	return false
}

// ErrVisionUnsupported is returned when an image is attached to a request no
// configured model can accept. It is deliberately produced before any network
// call, and it names the models tried and the config key to change, because
// "unsupported" without either is a dead end for the operator.
type ErrVisionUnsupported struct {
	Models []string
}

func (e *ErrVisionUnsupported) Error() string {
	models := "the configured model"
	if len(e.Models) > 0 {
		models = strings.Join(e.Models, ", ")
	}
	return fmt.Sprintf("this message has an image attached, but %s cannot accept images; "+
		"set agents.defaults.model (or a profile) to a vision-capable model such as "+
		"openai/gpt-4o, anthropic/claude-sonnet-4-20250514 or ollama/llava", models)
}
