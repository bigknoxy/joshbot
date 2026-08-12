package subagent

import (
	"reflect"
	"testing"

	"github.com/bigknoxy/joshbot/internal/tools"
)

// TestFieldParity ensures the subagent mirror types stay in sync with the
// canonical types in the tools package. If tools.ToolResult or
// tools.AsyncResult gains a new field, the mirror type must be updated
// and the adapter in cmd/joshbot/main.go must map it.
//
// This test lives in the subagent package (not cmd/joshbot) because the
// subagent package defines the mirror types. The adapter is verified by
// ensuring all fields from the canonical types are present in the mirrors.
func TestFieldParity(t *testing.T) {
	t.Run("ToolResult", func(t *testing.T) {
		canonical := reflect.TypeOf(tools.ToolResult{})
		mirror := reflect.TypeOf(ToolResult{})

		for i := 0; i < canonical.NumField(); i++ {
			cf := canonical.Field(i)
			mf, ok := mirror.FieldByName(cf.Name)
			if !ok {
				t.Errorf("tools.ToolResult.%s missing from subagent.ToolResult — add it and update toolExecutorAdapter", cf.Name)
				continue
			}
			if mf.Type != cf.Type {
				t.Errorf("subagent.ToolResult.%s type mismatch: got %s, want %s", cf.Name, mf.Type, cf.Type)
			}
		}

		// Check mirror doesn't have extra fields not in canonical
		for i := 0; i < mirror.NumField(); i++ {
			mf := mirror.Field(i)
			_, ok := canonical.FieldByName(mf.Name)
			if !ok {
				t.Errorf("subagent.ToolResult.%s exists in mirror but not in tools.ToolResult — remove it", mf.Name)
			}
		}
	})

	t.Run("AsyncResult", func(t *testing.T) {
		canonical := reflect.TypeOf(tools.AsyncResult{})
		mirror := reflect.TypeOf(AsyncResult{})

		for i := 0; i < canonical.NumField(); i++ {
			cf := canonical.Field(i)
			mf, ok := mirror.FieldByName(cf.Name)
			if !ok {
				t.Errorf("tools.AsyncResult.%s missing from subagent.AsyncResult — add it and update toolExecutorAdapter", cf.Name)
				continue
			}
			if mf.Type != cf.Type {
				t.Errorf("subagent.AsyncResult.%s type mismatch: got %s, want %s", cf.Name, mf.Type, cf.Type)
			}
		}

		for i := 0; i < mirror.NumField(); i++ {
			mf := mirror.Field(i)
			_, ok := canonical.FieldByName(mf.Name)
			if !ok {
				t.Errorf("subagent.AsyncResult.%s exists in mirror but not in tools.AsyncResult — remove it", mf.Name)
			}
		}
	})
}

// TestFirstNonZeroHelpers verifies the default-override helpers used in RunWithCallback.
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
