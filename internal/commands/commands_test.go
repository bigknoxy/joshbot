package commands

import (
	"strings"
	"testing"
)

func TestHelpListsEveryCommandOnce(t *testing.T) {
	help := HelpText()
	seen := map[string]bool{}
	for _, c := range All {
		if c.Description == "" {
			t.Errorf("/%s has no description", c.Name)
		}
		if seen[c.Name] {
			t.Errorf("/%s listed twice", c.Name)
		}
		seen[c.Name] = true
		if n := strings.Count(help, "\n/"+c.Name+" "); n != 1 {
			t.Errorf("/%s appears %d times in help, want 1:\n%s", c.Name, n, help)
		}
		if !Is(c.Name) {
			t.Errorf("Is(%q) = false", c.Name)
		}
	}
	if Is("bogus") {
		t.Error("Is(bogus) = true")
	}
	if got := UnknownText("bogus"); !strings.HasPrefix(got, "Unknown command: /bogus") || !strings.Contains(got, "/help - ") {
		t.Errorf("UnknownText = %q", got)
	}
	if len(Names()) != len(All) || Names()[0] != "start" {
		t.Errorf("Names() = %v", Names())
	}
}
