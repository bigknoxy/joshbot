package tools

import (
	"context"
	"testing"
)

// TestProgressFromContextNilOnBareContext verifies that a context with no
// progress callback attached returns nil rather than panicking or requiring
// callers to special-case a missing sink — this is fire-and-forget cosmetic
// output, not a security gate, so unlike ApproverFromContext it does not fail
// closed to a non-nil default.
func TestProgressFromContextNilOnBareContext(t *testing.T) {
	if p := ProgressFromContext(context.Background()); p != nil {
		t.Errorf("expected nil ProgressFunc for a bare context, got %v", p)
	}
	if p := ProgressFromContext(nil); p != nil { //nolint:staticcheck // deliberately exercising a nil ctx
		t.Errorf("expected nil ProgressFunc for a nil context, got %v", p)
	}
}

// TestWithProgressRoundTrips verifies WithProgress attaches a callback that
// ProgressFromContext then returns, and that invoking it reaches the original
// function (funcs are not comparable, so this is proven via a side effect).
func TestWithProgressRoundTrips(t *testing.T) {
	var got []string
	ctx := WithProgress(context.Background(), func(note string) {
		got = append(got, note)
	})

	p := ProgressFromContext(ctx)
	if p == nil {
		t.Fatal("expected non-nil ProgressFunc after WithProgress")
	}
	p("trying exa-cli")
	p("falling back to Exa MCP")

	want := []string{"trying exa-cli", "falling back to Exa MCP"}
	if len(got) != len(want) {
		t.Fatalf("got %d notes, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("note %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWithProgressNilClears verifies that passing a nil ProgressFunc to
// WithProgress removes any previously attached callback, mirroring
// WithApprover's "passing nil disables" contract for the approval gate — here
// that just means ProgressFromContext goes back to returning nil.
func TestWithProgressNilClears(t *testing.T) {
	ctx := WithProgress(context.Background(), func(string) {})
	ctx = WithProgress(ctx, nil)
	if p := ProgressFromContext(ctx); p != nil {
		t.Errorf("expected nil ProgressFunc after WithProgress(ctx, nil), got %v", p)
	}
}
