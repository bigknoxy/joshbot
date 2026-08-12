package subagent_test

import (
	"reflect"
	"testing"

	"github.com/bigknoxy/joshbot/internal/subagent"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// TestFieldParity ensures the subagent mirror types stay in sync with the
// canonical types in the tools package. If tools.ToolResult or
// tools.AsyncResult gains a new field, the mirror type must be updated
// and the adapter in cmd/joshbot/main.go must map it.
//
// This test lives in an external test package (subagent_test) so it can import
// both subagent and tools without a cycle: tools imports subagent (for the
// delegate tool), so a test inside package subagent that imports tools would
// form a cycle.
func TestFieldParity(t *testing.T) {
	t.Run("ToolResult", func(t *testing.T) {
		canonical := reflect.TypeOf(tools.ToolResult{})
		mirror := reflect.TypeOf(subagent.ToolResult{})

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
		mirror := reflect.TypeOf(subagent.AsyncResult{})

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
