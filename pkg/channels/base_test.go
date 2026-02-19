package channels

import (
    "testing"

    "github.com/bigknoxy/joshbot/pkg/bus"
)

func TestNormalizeAllowListAndIsAllowed(t *testing.T) {
    mb := bus.NewMessageBus()

    // Create base channel with mixed-case and with/without @ entries
    bc := NewBaseChannel("telegram", mb, []string{"@Alice", "bob"})

    cases := []struct{
        input string
        want bool
    }{
        {"Alice", true},
        {"@alice", true},
        {"ALICE", true},
        {"bob", true},
        {"@Bob", true},
        {"charlie", false},
    }

    for _, c := range cases {
        if got := bc.IsAllowed(c.input); got != c.want {
            t.Fatalf("IsAllowed(%q) = %v; want %v", c.input, got, c.want)
        }
    }
}
