package tools

import "testing"

func TestIsWithinBase(t *testing.T) {
	base := "/tmp/work"
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "exact base", path: "/tmp/work", want: true},
		{name: "child", path: "/tmp/work/a/b", want: true},
		{name: "sibling prefix", path: "/tmp/workspace/abc", want: false},
		{name: "parent", path: "/tmp", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWithinBase(tt.path, base); got != tt.want {
				t.Fatalf("isWithinBase(%q, %q) = %v, want %v", tt.path, base, got, tt.want)
			}
		})
	}
}
