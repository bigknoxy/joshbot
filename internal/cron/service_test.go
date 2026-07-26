package cron

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

func newTestService(t *testing.T) (*Service, *bus.MessageBus) {
	t.Helper()
	b := bus.NewMessageBus()
	return NewService(b, t.TempDir()), b
}

func TestListJobs_ReturnsAddedJobs(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)

	if got := s.ListJobs(); len(got) != 0 {
		t.Fatalf("new service should have no jobs, got %d", len(got))
	}

	_ = s.AddJob(Job{ID: "a", Schedule: "every:1h", Channel: "cli", Content: "one"})
	_ = s.AddJob(Job{ID: "b", Schedule: "delay:1h", Channel: "cli", Content: "two"})

	got := s.ListJobs()
	if len(got) != 2 {
		t.Fatalf("ListJobs() returned %d jobs, want 2", len(got))
	}
}

// ListJobs must return a copy; mutating the result must not corrupt the service.
func TestListJobs_ReturnsCopy(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	_ = s.AddJob(Job{ID: "a", Schedule: "every:1h", Channel: "cli", Content: "original"})

	got := s.ListJobs()
	got[0].Content = "mutated"

	if again := s.ListJobs(); again[0].Content != "original" {
		t.Errorf("ListJobs() exposed internal state: content became %q", again[0].Content)
	}
}

func TestListJobs_IsDeterministicallyOrdered(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	for _, id := range []string{"c", "a", "b"} {
		_ = s.AddJob(Job{ID: id, Schedule: "every:1h", Channel: "cli", Content: id})
	}

	// Go map iteration is randomised; without explicit sorting this flakes.
	for i := 0; i < 20; i++ {
		got := s.ListJobs()
		if got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
			t.Fatalf("ListJobs() not sorted by ID: %v, %v, %v", got[0].ID, got[1].ID, got[2].ID)
		}
	}
}

func TestDeleteJob_RemovesAndPersists(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	_ = s.AddJob(Job{ID: "gone", Schedule: "every:1h", Channel: "cli", Content: "x"})

	if err := s.DeleteJob("gone"); err != nil {
		t.Fatalf("DeleteJob error: %v", err)
	}
	if len(s.ListJobs()) != 0 {
		t.Error("job still present after DeleteJob")
	}

	// The deletion must survive a reload, not just live in memory.
	s2 := NewService(b, s.workspace)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(s2.ListJobs()) != 0 {
		t.Error("deleted job came back after reload")
	}
}

func TestDeleteJob_UnknownIDErrors(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	if err := s.DeleteJob("nope"); err == nil {
		t.Error("DeleteJob on an unknown ID should error so the agent can report it")
	}
}

// A deleted job must stop firing, otherwise "delete" is cosmetic.
func TestDeleteJob_StopsARunningJob(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	b.Start()

	fired := make(chan string, 8)
	b.Subscribe("test", func(ctx context.Context, msg bus.InboundMessage) {
		fired <- msg.Content
	})

	s.Start()
	defer s.Stop()
	_ = s.AddJob(Job{ID: "tick", Schedule: "every:50ms", Channel: "test", Content: "x"})

	// Wait for it to prove it is running.
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("recurring job never fired")
	}

	if err := s.DeleteJob("tick"); err != nil {
		t.Fatalf("DeleteJob error: %v", err)
	}
	// Drain anything already in flight.
	time.Sleep(120 * time.Millisecond)
	for len(fired) > 0 {
		<-fired
	}

	select {
	case <-fired:
		t.Error("job kept firing after DeleteJob")
	case <-time.After(300 * time.Millisecond):
	}
}

// A one-shot job must not be replayed on every subsequent start.
func TestDelayJob_DoesNotRefireAfterRestart(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	b.Start()

	fired := make(chan string, 8)
	b.Subscribe("test", func(ctx context.Context, msg bus.InboundMessage) {
		fired <- msg.Content
	})

	s.Start()
	_ = s.AddJob(Job{ID: "once", Schedule: "delay:50ms", Channel: "test", Content: "boom"})

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("one-shot job never fired")
	}
	s.Stop()

	// It has fired, so it must no longer be persisted.
	s2 := NewService(b, s.workspace)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(s2.ListJobs()) != 0 {
		t.Fatalf("spent one-shot job survived: %+v", s2.ListJobs())
	}

	s2.Start()
	defer s2.Stop()
	select {
	case v := <-fired:
		t.Errorf("one-shot job fired again after restart: %q", v)
	case <-time.After(300 * time.Millisecond):
	}
}

// Stop closes stopCh; a subsequent Start must not panic or refuse to schedule.
func TestService_CanRestartAfterStop(t *testing.T) {
	t.Parallel()
	s, b := newTestService(t)
	b.Start()

	fired := make(chan string, 4)
	b.Subscribe("test", func(ctx context.Context, msg bus.InboundMessage) {
		fired <- msg.Content
	})

	s.Start()
	s.Stop()

	s.Start() // must not panic on a closed channel
	defer s.Stop()

	_ = s.AddJob(Job{ID: "after", Schedule: "delay:50ms", Channel: "test", Content: "restarted"})
	select {
	case v := <-fired:
		if v != "restarted" {
			t.Fatalf("unexpected content %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job added after restart never fired")
	}
}

// AddJob read s.running without holding the mutex. Run under -race.
func TestAddJob_NoRaceWithStartStop(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.AddJob(Job{
				ID:       string(rune('a' + i)),
				Schedule: "every:1h",
				Channel:  "test",
				Content:  "x",
			})
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Start()
	}()
	wg.Wait()
	s.Stop()
}
