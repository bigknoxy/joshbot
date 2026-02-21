package cron

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

func TestCronService_DelayJobPublishes(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	b := bus.NewMessageBus()
	b.Start()

	s := NewService(b, tmp)

	// subscribe to the test channel
	got := make(chan string, 1)
	b.Subscribe("test", func(ctx context.Context, msg bus.InboundMessage) {
		got <- msg.Content
	})

	job := Job{ID: "j1", Schedule: "delay:100ms", Channel: "test", Content: "hello"}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob error: %v", err)
	}

	s.Start()
	defer s.Stop()

	select {
	case v := <-got:
		if v != "hello" {
			t.Fatalf("unexpected content: %s", v)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for job publish")
	}
}

func TestCronService_SaveLoad(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	b := bus.NewMessageBus()
	s := NewService(b, tmp)

	job := Job{ID: "j2", Schedule: "delay:1s", Channel: "test", Content: "x"}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob error: %v", err)
	}

	// ensure file exists
	jobsFile := filepath.Join(tmp, "cron", "jobs.json")
	if _, err := os.Stat(jobsFile); err != nil {
		t.Fatalf("jobs file missing: %v", err)
	}

	// create new service and load
	s2 := NewService(b, tmp)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if _, ok := s2.jobs["j2"]; !ok {
		t.Fatalf("job not persisted")
	}
}
