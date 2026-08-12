package subagent

import "testing"

func TestFirstNonZeroHelpers(t *testing.T) {
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"all zero", firstNonZero(0, 0), 0},
		{"first wins", firstNonZero(5, 10), 5},
		{"second fallback", firstNonZero(0, 10), 10},
		{"third fallback", firstNonZero(0, 0, 20), 20},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

func TestFirstNonZeroFloat(t *testing.T) {
	tests := []struct {
		name string
		got  float64
		want float64
	}{
		{"all zero", firstNonZeroFloat(0, 0), 0},
		{"first wins", firstNonZeroFloat(0.5, 0.7), 0.5},
		{"second fallback", firstNonZeroFloat(0, 0.7), 0.7},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %f, want %f", tt.name, tt.got, tt.want)
		}
	}
}

func TestFirstNonZeroDur(t *testing.T) {
	d1 := firstNonZeroDur(0, 30e9)
	if d1 != 30e9 {
		t.Errorf("second fallback: got %v, want 30s", d1)
	}
	d2 := firstNonZeroDur(5e9, 30e9)
	if d2 != 5e9 {
		t.Errorf("first wins: got %v, want 5s", d2)
	}
}

func TestFirstNonZeroStr(t *testing.T) {
	s1 := firstNonZeroStr("", "fallback")
	if s1 != "fallback" {
		t.Errorf("second fallback: got %q, want %q", s1, "fallback")
	}
	s2 := firstNonZeroStr("primary", "fallback")
	if s2 != "primary" {
		t.Errorf("first wins: got %q, want %q", s2, "primary")
	}
}
