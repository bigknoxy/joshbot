package main

import (
	"fmt"
	"strings"
	"testing"
)

// A real provider list is hundreds of models long, so the picker's long-list
// path is the one operators actually see and the short one is the exception.
// Two things go wrong silently there: the list is capped at 15 with no sign
// that anything was dropped, so the operator concludes their model is not
// offered; and a number is still expected to index the whole filtered list, not
// only the visible window.

func manyModels(n int) []string {
	models := make([]string, n)
	for i := range models {
		models[i] = fmt.Sprintf("vendor/model-%02d", i)
	}
	return models
}

// Over the cap the display is truncated, and the truncation has to be
// announced. Silently showing 15 of 40 is how an operator decides the model
// they want is unavailable and picks the wrong one.
func TestPromptModelSelectionAnnouncesTheModelsItDidNotShow(t *testing.T) {
	models := manyModels(40)
	withStdinInput(t, "\n")

	var got string
	out := captureStdout(t, func() { got = promptModelSelection(models, models[0]) })

	if got != models[0] {
		t.Errorf("selection = %q, want the default", got)
	}
	if !strings.Contains(out, "and 25 more") {
		t.Errorf("the 25 hidden models were not announced:\n%s", out)
	}
	if !strings.Contains(out, "vendor/model-14") {
		t.Errorf("the 15th model was not listed:\n%s", out)
	}
	if strings.Contains(out, "vendor/model-15") {
		t.Errorf("model 16 was listed; the display cap is 15:\n%s", out)
	}
}

// The cap is a display cap, not a selection cap. A number past the visible
// window still has to index the filtered list — refusing it would leave every
// model after the 15th unreachable except by typing a filter.
func TestPromptModelSelectionSelectsBeyondTheVisibleWindow(t *testing.T) {
	models := manyModels(40)
	withStdinInput(t, "31\n")

	var got string
	captureStdout(t, func() { got = promptModelSelection(models, models[0]) })

	if got != "vendor/model-30" {
		t.Errorf("selection = %q, want vendor/model-30 (entry 31, past the 15 shown)", got)
	}
}

// Retyping the same filter is what an operator does when nothing appeared to
// happen. With one match it selects; with several it must say what to do next
// and keep the loop alive. Falling through to the filter code would recompute
// the identical list forever with no guidance on screen.
func TestPromptModelSelectionRepeatedFilterWithSeveralMatchesGuidesInstead(t *testing.T) {
	models := []string{"openai/gpt-4o", "openai/gpt-4o-mini", "meta/llama-3-70b"}
	withStdinInput(t, "gpt\ngpt\n2\n")

	var got string
	out := captureStdout(t, func() { got = promptModelSelection(models, models[2]) })

	if !strings.Contains(out, "Press Enter for default or type a number") {
		t.Errorf("a repeated filter with 2 matches gave no guidance:\n%s", out)
	}
	if got != "openai/gpt-4o-mini" {
		t.Errorf("selection = %q, want the second match; the filter must survive the repeat", got)
	}
}
