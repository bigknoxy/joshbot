package cron

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// firedChan subscribes to a channel and reports content as it arrives.
func firedChan(t *testing.T, b *bus.MessageBus, channel string) chan string {
	t.Helper()
	fired := make(chan string, 8)
	b.Subscribe(channel, func(ctx context.Context, msg bus.InboundMessage) {
		fired <- msg.Content
	})
	return fired
}

// writeJobsFile writes a raw jobs.json into a workspace, as an older joshbot would have.
func writeJobsFile(t *testing.T, workspace string, raw string) {
	t.Helper()
	dir := filepath.Join(workspace, "cron")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// AddJob must record an absolute due moment for one-shot jobs, and persist it.
func TestAddJob_RecordsAbsoluteDueTime(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)

	before := time.Now()
	if err := s.AddJob(Job{ID: "once", Schedule: "delay:1h", Channel: "cli", Content: "x"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	after := time.Now()

	got := s.ListJobs()[0]
	if got.DueAt.IsZero() {
		t.Fatal("delay job has no DueAt; the due moment is not recorded")
	}
	if got.DueAt.Before(before.Add(time.Hour)) || got.DueAt.After(after.Add(time.Hour)) {
		t.Errorf("DueAt = %v, want ~now+1h (now was %v)", got.DueAt, before)
	}

	// It must survive on disk, otherwise a restart cannot use it.
	data, err := os.ReadFile(s.jobsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var persisted []Job
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(persisted) != 1 || persisted[0].DueAt.IsZero() {
		t.Fatalf("DueAt not persisted: %s", data)
	}
}

// A recurring job has no single due moment; it must not get one.
func TestAddJob_EveryJobHasNoDueTime(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	_ = s.AddJob(Job{ID: "tick", Schedule: "every:1h", Channel: "cli", Content: "x"})
	if got := s.ListJobs()[0]; !got.DueAt.IsZero() {
		t.Errorf("every: job got DueAt %v, want zero", got.DueAt)
	}
}

// The defect: a restart must not restart the countdown. A job whose due moment
// is near must fire near it, not a full duration later.
func TestDelayJob_FiresAtOriginalDueTimeAfterRestart(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	b.Start()
	fired := firedChan(t, b, "test")

	s.Start()
	// Long schedule, so "now + duration" would be far beyond the test timeout.
	if err := s.AddJob(Job{ID: "once", Schedule: "delay:10s", Channel: "test", Content: "boom"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	s.Stop()

	// Rewrite the persisted job so its due moment is imminent, simulating a job
	// scheduled long ago that is nearly due when the process comes back.
	jobs := s.ListJobs()
	jobs[0].DueAt = time.Now().Add(150 * time.Millisecond)
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(s.jobsPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s2 := NewService(b, s.workspace)
	s2.Start()
	defer s2.Stop()

	select {
	case v := <-fired:
		if v != "boom" {
			t.Fatalf("unexpected content %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job did not fire at its original due moment; countdown restarted")
	}
}

// A job that came due while the process was stopped must fire promptly, not be
// dropped and not wait out another full duration.
func TestDelayJob_OverdueJobFiresPromptly(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	b.Start()
	fired := firedChan(t, b, "test")

	_ = s.AddJob(Job{ID: "late", Schedule: "delay:10s", Channel: "test", Content: "late"})
	jobs := s.ListJobs()
	jobs[0].DueAt = time.Now().Add(-time.Hour)
	data, _ := json.MarshalIndent(jobs, "", "  ")
	if err := os.WriteFile(s.jobsPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s2 := NewService(b, s.workspace)
	s2.Start()
	defer s2.Stop()

	select {
	case v := <-fired:
		if v != "late" {
			t.Fatalf("unexpected content %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overdue job never fired")
	}

	// And it must retire, like any spent one-shot job.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(s2.ListJobs()) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("overdue one-shot job was not retired: %+v", s2.ListJobs())
}

// Backward compatibility: a jobs.json written before due_at existed must load,
// and its delay job must be treated as due one duration from load — the old
// behaviour — rather than firing instantly in a storm.
func TestLegacyJobWithoutDueAt_IsDueOneDurationFromLoad(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	b.Start()
	fired := firedChan(t, b, "test")

	writeJobsFile(t, s.workspace, `[
  {"id":"legacy","schedule":"delay:10s","channel":"test","content":"old"}
]`)

	if err := s.Load(); err != nil {
		t.Fatalf("Load on a legacy file must not error: %v", err)
	}
	loadedAt := time.Now()

	jobs := s.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("legacy job did not load: %+v", jobs)
	}
	if jobs[0].DueAt.IsZero() {
		t.Fatal("legacy job was not given a due moment on load")
	}
	if jobs[0].DueAt.Before(loadedAt.Add(9 * time.Second)) {
		t.Errorf("legacy job due at %v, want ~load+10s (load was %v) — it would fire too early", jobs[0].DueAt, loadedAt)
	}

	// Concretely: it must not fire immediately.
	s.Start()
	defer s.Stop()
	select {
	case v := <-fired:
		t.Fatalf("legacy job fired immediately: %q", v)
	case <-time.After(400 * time.Millisecond):
	}
}

// The backfilled due moment must be persisted, so a second restart does not
// slide the deadline forward again.
func TestLegacyJobWithoutDueAt_BackfillIsPersisted(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	writeJobsFile(t, s.workspace, `[
  {"id":"legacy","schedule":"delay:10s","channel":"test","content":"old"}
]`)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := s.ListJobs()[0].DueAt

	time.Sleep(30 * time.Millisecond)

	s2 := NewService(b, s.workspace)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	second := s2.ListJobs()[0].DueAt
	if !second.Equal(first) {
		t.Errorf("due moment moved across reloads: %v then %v", first, second)
	}
}

// A legacy recurring job must load unchanged and keep its semantics.
func TestLegacyEveryJobWithoutDueAt_Loads(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	b.Start()
	fired := firedChan(t, b, "test")

	writeJobsFile(t, s.workspace, `[
  {"id":"tick","schedule":"every:50ms","channel":"test","content":"t"}
]`)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.ListJobs()[0]; !got.DueAt.IsZero() {
		t.Errorf("recurring job gained a due moment: %v", got.DueAt)
	}

	s.Start()
	defer s.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-fired:
		case <-time.After(3 * time.Second):
			t.Fatal("legacy recurring job did not keep firing")
		}
	}
}

// The on-disk shape is user-visible (workspace/cron/jobs.json) and is what a
// legacy-compatibility decision keys off, so pin it. Note `omitempty` does NOT
// drop a zero time.Time — encoding/json never considers a struct empty — so a
// recurring job carries an explicit zero due_at rather than omitting the field.
func TestJobsFileShape(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	s := NewService(bus.NewMessageBus(), ws)

	if err := s.AddJob(Job{ID: "once", Schedule: "delay:30m", Channel: "cli", Content: "stretch"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if err := s.AddJob(Job{ID: "rep", Schedule: "every:24h", Channel: "cli", Content: "standup"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(ws, "cron", "jobs.json"))
	if err != nil {
		t.Fatalf("read jobs.json: %v", err)
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatalf("jobs.json is not valid JSON: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs on disk, got %d", len(jobs))
	}

	byID := map[string]Job{}
	for _, j := range jobs {
		byID[j.ID] = j
	}
	if byID["once"].DueAt.IsZero() {
		t.Error("one-shot job persisted without a due moment")
	}
	if !byID["rep"].DueAt.IsZero() {
		t.Errorf("recurring job persisted a due moment: %v", byID["rep"].DueAt)
	}
	// Round-trip: what is written must read back with the same meaning.
	if !strings.Contains(string(data), `"due_at"`) {
		t.Error(`jobs.json is missing the "due_at" field`)
	}
}
