package channels

import (
	"testing"

	"github.com/bigknoxy/joshbot/pkg/bus"
)

func TestAllowlistNormalization(t *testing.T) {
	mb := bus.NewMessageBus()

	// allowlist contains entries with and without '@' and mixed case
	bc := NewBaseChannel("telegram", mb, []string{"@Alice", "Bob"})

	tests := []struct {
		name string
		want bool
	}{
		{"Alice", true},
		{"@alice", true},
		{"ALICE", true},
		{"bob", true},
		{"@Bob", true},
		{"charlie", false},
	}

	for _, tt := range tests {
		if got := bc.IsAllowed(tt.name); got != tt.want {
			t.Fatalf("IsAllowed(%q) = %v; want %v", tt.name, got, tt.want)
		}
	}
}
