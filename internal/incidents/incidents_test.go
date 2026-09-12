package incidents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestLog(t *testing.T) *Log {
	t.Helper()
	path := filepath.Join(t.TempDir(), "incidents.jsonl")
	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	return l
}

func TestRecordPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incidents.jsonl")
	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	l.now = func() time.Time { return time.Unix(1700000000, 0) }

	l.Record(Incident{Type: TurnTimeout, Session: "telegram:bob", Channel: "telegram",
		Model: "nvidia/deepseek", ElapsedNanos: 120_000_000_000, Iterations: 3, ToolCalls: 5,
		Detail: "context deadline exceeded"})

	reloaded, err := NewLog(path)
	if err != nil {
		t.Fatalf("reload NewLog: %v", err)
	}
	got := reloaded.Recent(10, "")
	if len(got) != 1 {
		t.Fatalf("Recent(10, \"\") len = %d, want 1", len(got))
	}
	if got[0].Type != TurnTimeout || got[0].Session != "telegram:bob" || got[0].Iterations != 3 {
		t.Fatalf("round-trip mismatch: %+v", got[0])
	}
}

func TestRecordStampsZeroTime(t *testing.T) {
	l := newTestLog(t)
	l.now = func() time.Time { return time.Unix(1700000000, 0) }
	l.Record(Incident{Type: LLMFailure, Detail: "boom"})
	if got := l.Recent(1, ""); len(got) != 1 || got[0].Time.IsZero() {
		t.Fatalf("zero time not stamped: %+v", got)
	}
}

func TestDetailIsRedacted(t *testing.T) {
	l := newTestLog(t)
	secret := "failed: Authorization: Bearer sk-supersecret-value-123456"
	l.Record(Incident{Type: LLMFailure, Detail: secret})
	got := l.Recent(1, "")[0].Detail
	if got == secret {
		t.Fatalf("detail not redacted: %q", got)
	}
}

func TestRecentNewestFirstAndTypeFilter(t *testing.T) {
	l := newTestLog(t)
	for i, typ := range []string{TurnTimeout, LLMFailure, TurnTimeout} {
		l.now = func() time.Time { return time.Unix(1700000000+int64(i), 0) }
		l.Record(Incident{Type: typ})
	}
	got := l.Recent(10, TurnTimeout)
	if len(got) != 2 || got[0].Type != TurnTimeout || got[1].Type != TurnTimeout {
		t.Fatalf("type filter wrong: %+v", got)
	}
	if got := l.Recent(2, ""); len(got) != 2 || got[0].Type != TurnTimeout || got[1].Type != LLMFailure {
		t.Fatalf("newest-first wrong: %+v", got)
	}
}

func TestInMemoryCapPrunesOldest(t *testing.T) {
	l := newTestLog(t)
	for i := 0; i < maxIncidents+5; i++ {
		l.now = func() time.Time { return time.Unix(1700000000+int64(i), 0) }
		l.Record(Incident{Type: TurnTimeout})
	}
	if got := l.CountSince(24*time.Hour, ""); got != maxIncidents {
		t.Fatalf("in-memory cap: CountSince = %d, want %d", got, maxIncidents)
	}
}

func TestCountSinceWindowAndFilter(t *testing.T) {
	l := newTestLog(t)
	l.now = func() time.Time { return time.Unix(1700000000, 0) }
	old := l.now().Add(-25 * time.Hour)
	l.Record(Incident{Time: old, Type: TurnTimeout})
	l.Record(Incident{Type: TurnTimeout})
	l.Record(Incident{Type: LLMFailure})

	if got := l.CountSince(24*time.Hour, TurnTimeout); got != 1 {
		t.Fatalf("window filter: got %d, want 1", got)
	}
	if got := l.CountSince(48*time.Hour, ""); got != 3 {
		t.Fatalf("no filter: got %d, want 3", got)
	}
}

func TestTimeoutBumpArithmetic(t *testing.T) {
	l := newTestLog(t)
	if got := l.TimeoutBump(); got != 0 {
		t.Fatalf("empty log bump = %v, want 0", got)
	}
	l.now = func() time.Time { return time.Unix(1700000000, 0) }
	for i := 0; i < 3; i++ {
		l.Record(Incident{Type: TurnTimeout})
	}
	if got := l.TimeoutBump(); got != 90*time.Second {
		t.Fatalf("3 timeouts bump = %v, want 90s", got)
	}
	for i := 0; i < 10; i++ {
		l.Record(Incident{Type: TurnTimeout})
	}
	if got := l.TimeoutBump(); got != HealMaxBump {
		t.Fatalf("13 timeouts bump = %v, want cap %v", got, HealMaxBump)
	}
	// Old timeouts do not count.
	cutoffOld := l.now().Add(-HealLookbackWindow - time.Minute)
	l.Record(Incident{Time: cutoffOld, Type: TurnTimeout})
	if got := l.TimeoutBump(); got != HealMaxBump {
		t.Fatalf("stale timeout changed bump: %v", got)
	}
}

func TestLLMFailureDoesNotDriveTimeoutBump(t *testing.T) {
	l := newTestLog(t)
	l.Record(Incident{Type: LLMFailure})
	l.Record(Incident{Type: StreamDied})
	if got := l.TimeoutBump(); got != 0 {
		t.Fatalf("non-timeout bump = %v, want 0", got)
	}
}

func TestPruneDropsOldAndKeepsNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incidents.jsonl")
	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	now := time.Now()
	l.now = func() time.Time { return now }
	l.Record(Incident{Time: now.Add(-48 * time.Hour), Type: TurnTimeout})
	l.Record(Incident{Type: LLMFailure})

	removed, err := l.Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if got := l.CountSince(0, ""); got != 1 {
		t.Fatalf("after prune count = %d, want 1", got)
	}

	reloaded, err := NewLog(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.CountSince(time.Minute, ""); got != 1 {
		t.Fatalf("file not rewritten: %d", got)
	}
	if got := reloaded.Recent(1, ""); got[0].Type != LLMFailure {
		t.Fatalf("wrong survivor: %+v", got[0])
	}
}

func TestPruneFailureKeepsOriginalFile(t *testing.T) {
	l := newTestLog(t)
	l.now = func() time.Time { return time.Unix(1700000000, 0) }
	l.Record(Incident{Time: l.now().Add(-48 * time.Hour), Type: TurnTimeout})
	l.Record(Incident{Type: LLMFailure})
	// Force rewrite failure: a directory component that cannot hold the
	// temp file (the leaf path is a directory, so the rename target is
	// invalid and CreateTemp's rename fails).
	if err := os.Remove(l.path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Mkdir(l.path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := l.Prune(24 * time.Hour); err == nil {
		t.Fatal("Prune should fail when the target is a directory")
	}
	if got := l.CountSince(0, ""); got != 2 {
		t.Fatalf("failed prune destroyed in-memory record: %d kept", got)
	}
	if got := l.Recent(1, ""); got[0].Type != LLMFailure {
		t.Fatalf("failed prune dropped survivor: %+v", got[0])
	}
}

func TestClearRemovesFileAndMemory(t *testing.T) {
	l := newTestLog(t)
	l.Record(Incident{Type: TurnTimeout})
	if err := l.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got := l.CountSince(time.Minute, ""); got != 0 {
		t.Fatalf("after clear count = %d", got)
	}
	if _, err := os.Stat(l.path); !os.IsNotExist(err) {
		t.Fatalf("file still present after Clear: %v", err)
	}
	if err := l.Clear(); err != nil { // missing file is not an error
		t.Fatalf("second Clear: %v", err)
	}
}

func TestLoadSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incidents.jsonl")
	good := `{"time":"` + time.Now().Format(time.RFC3339Nano) + `","type":"turn_timeout"}`
	content := "not json\n\n" + good + "\n{\"type\":123}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog: %v", err)
	}
	if got := l.CountSince(time.Minute, TurnTimeout); got != 1 {
		t.Fatalf("lenient load got %d, want 1", got)
	}
}

func TestFileModeIsOwnerOnly(t *testing.T) {
	l := newTestLog(t)
	l.Record(Incident{Type: TurnTimeout})
	info, err := os.Stat(l.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}
}

func TestJSONWellFormedLines(t *testing.T) {
	l := newTestLog(t)
	l.Record(Incident{Type: TurnTimeout, Detail: `quote " and \ backslash`})
	data, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var inc Incident
	if err := json.Unmarshal(data[:len(data)-1], &inc); err != nil {
		t.Fatalf("stored line not valid JSON: %v (%s)", err, data)
	}
}
