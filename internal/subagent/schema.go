package subagent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OutputSchema constrains a subagent's final answer. It is deliberately a small
// subset of JSON Schema — object type, required keys, optional per-key types —
// because the point is to give the caller a parseable result, not to implement
// a validator. A run whose output does not satisfy it is an error, never a
// success carrying prose where the caller expects fields.
type OutputSchema struct {
	// Required names the keys that must be present and non-null.
	Required []string
	// Types optionally pins a key to "string", "number", "boolean", "array"
	// or "object". A key absent from this map may hold anything.
	Types map[string]string
}

// Describe renders the schema as an instruction appended to the subagent's
// prompt. Without it the model has no way to know what shape is expected and
// the first attempt fails by definition.
func (s *OutputSchema) Describe() string {
	if s == nil || len(s.Required) == 0 {
		return ""
	}
	keys := append([]string(nil), s.Required...)
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("\n\nRespond with a single JSON object and nothing else — no prose, no code fence. Required keys:\n")
	for _, k := range keys {
		if t, ok := s.Types[k]; ok {
			fmt.Fprintf(&b, "- %s (%s)\n", k, t)
			continue
		}
		fmt.Fprintf(&b, "- %s\n", k)
	}
	return b.String()
}

// Validate reports why output does not satisfy the schema, or nil if it does.
// The message is fed back to the model for one repair attempt, so it names the
// specific key rather than saying the output was invalid.
func (s *OutputSchema) Validate(output string) error {
	if s == nil || len(s.Required) == 0 {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stripFence(output)), &obj); err != nil {
		return fmt.Errorf("output is not a JSON object: %v", err)
	}
	keys := append([]string(nil), s.Required...)
	sort.Strings(keys)
	for _, k := range keys {
		v, ok := obj[k]
		if !ok || v == nil {
			return fmt.Errorf("missing required key %q", k)
		}
		want, pinned := s.Types[k]
		if !pinned {
			continue
		}
		if got := jsonTypeOf(v); got != want {
			return fmt.Errorf("key %q is %s, want %s", k, got, want)
		}
	}
	return nil
}

// stripFence removes a ```json fence, which models add even when told not to.
// Rejecting output for the fence alone would burn a repair iteration on
// formatting rather than on content.
func stripFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
}

func jsonTypeOf(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64, json.Number:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "null"
	}
}
