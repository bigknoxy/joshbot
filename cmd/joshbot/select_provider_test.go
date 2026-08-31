package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// selectProvider prints a numbered menu from one slice and maps the answer back
// with a separate hand-written switch. Nothing keeps the two in step: inserting
// or reordering a provider in the printed list without editing the switch
// onboards a *different* provider than the one the operator picked, with no
// error anywhere — the wizard then asks for that provider's key and writes a
// working-looking config that dials the wrong service.
//
// These tests read the menu joshbot actually printed and check that every
// number resolves to the provider shown beside it.

// menuChoices extracts "<n> -> <display name>" from the printed menu.
func menuChoices(t *testing.T, out string) map[string]string {
	t.Helper()

	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "✅"))
		var n int
		var rest string
		if _, err := fmt.Sscanf(line, "%d. %s", &n, &rest); err != nil {
			continue
		}
		// The name runs to the " (" that opens the description, if any.
		name := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(line, fmt.Sprintf("%d.", n)), "(", 2)[0])
		got[fmt.Sprint(n)] = name
	}
	if len(got) == 0 {
		t.Fatalf("no numbered options were parsed out of the menu:\n%s", out)
	}
	return got
}

func TestSelectProviderEveryMenuNumberReturnsTheProviderItNames(t *testing.T) {
	// One pass to learn the menu, so the test follows the real list rather
	// than a copy of it that would drift alongside the bug.
	withStdinInput(t, "\n")
	var menu map[string]string
	out := captureStdout(t, func() { selectProvider(nil) })
	menu = menuChoices(t, out)

	for choice, displayName := range menu {
		t.Run("choice_"+choice, func(t *testing.T) {
			withStdinInput(t, choice+"\n")
			var got string
			captureStdout(t, func() { got = selectProvider(nil) })

			if got == "" {
				t.Fatalf("choice %q resolved to no provider", choice)
			}
			if want := providers.GetProviderDisplayName(got); want != displayName {
				t.Errorf("choice %q shows %q in the menu but selects %q (%q) — "+
					"the printed list and the switch disagree",
					choice, displayName, got, want)
			}
			// Every provider reachable from the menu must have something to
			// say at the model prompt; a blank line there reads as a bug.
			if modelHelp(got) == "" {
				t.Errorf("provider %q has no modelHelp text", got)
			}
			// Providers that take an API key must tell the operator where to
			// get one. ollama is local and github-copilot uses OAuth.
			if got != "ollama" && got != "github-copilot" && providerKeyURL(got) == "" {
				t.Errorf("provider %q needs an API key but offers no key URL", got)
			}
		})
	}
}

// Garbled input (a typo, a stray paste) fell through to the default with no
// feedback — the operator had no way to know their choice wasn't the one
// written to config.json. A blank line (pressing Enter) is a deliberate
// "use the default" and must stay silent.
func TestSelectProviderNamesTheDefaultOnUnparseableInput(t *testing.T) {
	withStdinInput(t, "xyz\n")
	out := captureStdout(t, func() { selectProvider(nil) })
	if !strings.Contains(out, `Didn't recognize "xyz"`) {
		t.Errorf("no notice for unparseable input:\n%s", out)
	}

	withStdinInput(t, "\n")
	out = captureStdout(t, func() { selectProvider(nil) })
	if strings.Contains(out, "Didn't recognize") {
		t.Errorf("a blank line (deliberate default) printed a notice:\n%s", out)
	}
}

// The current default provider is shown so the operator can see what they are
// about to change. Losing it makes re-running onboarding a guess.
func TestSelectProviderShowsTheConfiguredDefault(t *testing.T) {
	withStdinInput(t, "\n")

	cfg := config.Defaults()
	cfg.ProviderDefaults.Default = "groq"

	out := captureStdout(t, func() { selectProvider(cfg) })
	if !strings.Contains(out, providers.GetProviderDisplayName("groq")) {
		t.Errorf("the configured default provider was not shown:\n%s", out)
	}
}
