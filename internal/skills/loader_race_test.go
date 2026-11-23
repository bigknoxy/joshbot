package skills

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// The discovery cache is read on the turn's goroutine and replaced from a
// background one: afterReActDetection creates the skill the turn produced
// and invalidates the cache while the next turn's notice is being built.
// Before #497 neither side took a lock, and the race detector caught the
// concurrent map access in CI. The loader must be safe to use from both.
func TestLoaderSurvivesAConcurrentCreateAndRead(t *testing.T) {
	workspace := t.TempDir()
	l, err := NewLoader(workspace)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	const turns = 40
	for i := 0; i < turns; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			content := fmt.Sprintf("---\nname: skill-%d\ndescription: turn %d wrote this\n---\n\nBody.\n", i, i)
			if err := l.Create(fmt.Sprintf("skill-%d", i), content); err != nil {
				t.Errorf("create skill-%d: %v", i, err)
			}
		}(i)
		go func() {
			defer wg.Done()
			_ = l.Untrusted()
			_ = l.List()
			_ = l.GetSkill("skill-0")
		}()
	}
	wg.Wait()

	// After the storm the cache is coherent: every skill written is
	// discoverable, untrusted, and listed exactly once.
	l.Invalidate()
	got := l.Untrusted()
	if len(got) != turns {
		t.Errorf("Untrusted() = %d skills, want %d", len(got), turns)
	}
	if sk := l.GetSkill("skill-7"); sk == nil {
		t.Errorf("skill-7 missing after the storm")
	} else if sk.Path != filepath.Join(workspace, "skills", "skill-7", "SKILL.md") && sk.Name != "skill-7" {
		t.Errorf("skill-7 resolved oddly: %+v", sk)
	}
}
