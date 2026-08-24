package contextpkg

import (
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// ComputeBudget subtracts the completion reservation and a safety margin from
// the model's window. When the caller asks for more completion tokens than the
// model has — a 4096-window model with max_tokens 8192, which is an ordinary
// misconfiguration — the arithmetic goes negative. A negative budget is fed
// straight into CompressMessages, where it is used both as a token comparison
// and as `budget*4` character slice bounds, so it turns a config mistake into
// an empty prompt or a panic. The floor is the thing that prevents that, and
// nothing exercised it.
func TestComputeBudgetNeverGoesNegative(t *testing.T) {
	b := NewBudgetManager(NewRegistry(), 100)

	for _, tc := range []struct {
		model         string
		maxCompletion int
	}{
		{"openai/gpt-4", 8192},   // exactly the window
		{"openai/gpt-4", 100000}, // far past it
		{"unknown-model", DefaultContextWindow},
	} {
		got := b.ComputeBudget(tc.model, tc.maxCompletion)
		if got < 256 {
			t.Errorf("ComputeBudget(%q, %d) = %d; a budget below the 256 floor is used as a slice bound in CompressMessages",
				tc.model, tc.maxCompletion, got)
		}
	}

	// A completion reservation that swallows the window is a misconfiguration,
	// and the answer to it is a quarter of the window, not the 256 floor: at
	// 256 tokens compaction fires at ~700 chars of history and summarizes the
	// conversation away on every tool call (#346).
	if got, want := b.ComputeBudget("openai/gpt-4", 8192), 8192/4; got != want {
		t.Errorf("ComputeBudget over-reserved = %d, want %d (a quarter of the window)", got, want)
	}

	// And the margin must actually be reserved, or the prompt is built to
	// exactly the window and the provider rejects the request.
	if got, want := b.ComputeBudget("openai/gpt-4", 1000), 8192-1000-100; got != want {
		t.Errorf("ComputeBudget = %d, want %d: the safety margin was not reserved", got, want)
	}
}

// A non-positive margin means "no reservation at all", which builds a prompt
// filling the entire context window and leaves nothing for the system message —
// the provider then rejects every request. The constructor substitutes a
// default; assert it, because the substitution is a silent one.
func TestNewBudgetManagerRejectsANonPositiveMargin(t *testing.T) {
	for _, margin := range []int{0, -5000} {
		b := NewBudgetManager(NewRegistry(), margin)
		got := b.ComputeBudget("openai/gpt-4", 1000)
		if want := 8192 - 1000 - 100; got != want {
			t.Errorf("margin %d: ComputeBudget = %d, want %d (the default 100-token margin)", margin, got, want)
		}
	}
}

// The size hints in a model name are checked in order, and "16k"/"32k" sit
// below the catch-all that maps anything containing "gpt" or "claude" to the
// 32768 default. A reordering would silently give a 16k model a 32k budget,
// which is not a test failure anywhere — it is a provider error at runtime,
// under load, for one model only.
func TestRegistryLookupSizeHintsBeatTheFamilyHeuristic(t *testing.T) {
	r := NewRegistry()
	for _, tc := range []struct {
		model string
		want  int
	}{
		{"vendor/gpt-tiny-16k", 16384},
		// llama alone maps to 8192, so this row fails if the 32k hint is lost.
		// ("claude-...-32k" would be a vacuous row: the claude family heuristic
		// also yields 32768.)
		{"vendor/llama-3-32k", 32768},
		{"vendor/gpt-huge-128k", 131072},
		{"vendor/llama-3-200k", 131072},
	} {
		if got := r.Lookup(tc.model).ContextWindow; got != tc.want {
			t.Errorf("Lookup(%q).ContextWindow = %d, want %d: the size hint in the name lost to a family heuristic",
				tc.model, got, tc.want)
		}
	}
}

// An override is stored case-folded and trimmed, because model names arrive
// from config files where "  OpenAI/GPT-4o  " is ordinary. Lookup folds its
// argument too; if only one side folded, a configured override would be
// silently ignored and the model would run on a guessed window.
func TestSetOverrideIsCaseAndWhitespaceInsensitive(t *testing.T) {
	r := NewRegistry()
	r.SetOverride("  Vendor/Custom-Model  ", 65536)

	if got := r.Lookup("vendor/custom-model").ContextWindow; got != 65536 {
		t.Errorf("configured override was not applied: got %d, want 65536", got)
	}
	if got := r.Lookup("VENDOR/CUSTOM-MODEL").ContextWindow; got != 65536 {
		t.Errorf("override lookup is case sensitive: got %d, want 65536", got)
	}

	// A non-positive window is a config error, not an instruction to make the
	// budget zero: it must be refused, leaving the previous value in place.
	r.SetOverride("vendor/custom-model", 0)
	if got := r.Lookup("vendor/custom-model").ContextWindow; got != 65536 {
		t.Errorf("a zero override replaced a valid window: got %d", got)
	}
	r.SetOverride("vendor/custom-model", -1)
	if got := r.Lookup("vendor/custom-model").ContextWindow; got != 65536 {
		t.Errorf("a negative override replaced a valid window: got %d", got)
	}
}

// CompressMessages must never return an empty context with a nil error. That
// is the silent-failure shape: the agent builds a prompt containing no
// conversation at all, answers confidently from nothing, and no caller has any
// signal that history was dropped. A budget of zero or less is reachable from
// any caller that does its own arithmetic (ComputeBudget floors at 256, but it
// is not the only source). A zero budget drove the truncation fallback to slice
// the content away to "" and report success; a negative one made the same slice
// run off the front of the string and panic, taking the process down. Both were
// live bugs found by this test.
func TestCompressMessagesNeverReturnsEmptyWithNilError(t *testing.T) {
	msgs := []providers.Message{
		{Role: providers.RoleUser, Content: "old"},
		{Role: providers.RoleAssistant, Content: ""},
		{Role: providers.RoleUser, Content: "the newest thing the user said"},
		{Role: providers.RoleAssistant, Content: ""},
	}

	for _, budget := range []int{-10, 0} {
		out, err := (&Compressor{}).CompressMessages(context.Background(), "unknown", msgs, budget)
		if err == nil && strings.TrimSpace(out) == "" {
			t.Errorf("budget %d: compression returned an empty context with a nil error; the agent would send a prompt with no conversation in it", budget)
		}
	}
}

// With a budget too small to admit the whole join, the result is the tail of
// the conversation, not the head: dropping the newest turn is what makes the
// agent answer the wrong question. Pin the direction of the truncation.
func TestCompressMessagesTruncationKeepsTheNewestContent(t *testing.T) {
	msgs := []providers.Message{
		{Role: providers.RoleUser, Content: "an old message nobody needs any more"},
		{Role: providers.RoleUser, Content: "the newest thing the user said"},
	}

	out, err := (&Compressor{}).CompressMessages(context.Background(), "unknown", msgs, 8)
	if err != nil {
		t.Fatalf("CompressMessages: %v", err)
	}
	if !strings.Contains(out, "user said") {
		t.Errorf("truncation dropped the newest content, got %q", out)
	}
	if strings.Contains(out, "an old message") {
		t.Errorf("truncation kept the oldest content over the newest, got %q", out)
	}
}
