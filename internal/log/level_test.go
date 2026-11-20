package log

import "testing"

// A caller that changes the global level has to be able to put it back:
// the level is process-wide, and leaving it lowered changes the behaviour
// of everything that runs after.
func TestGetLevelReportsTheCurrentLevel(t *testing.T) {
	original := GetLevel()
	t.Cleanup(func() { SetLevel(original) })

	SetLevel(WarnLevel)
	if got := GetLevel(); got != WarnLevel {
		t.Errorf("GetLevel() = %v, want %v", got, WarnLevel)
	}

	SetLevel(DebugLevel)
	if got := GetLevel(); got != DebugLevel {
		t.Errorf("GetLevel() = %v, want %v", got, DebugLevel)
	}
}
