package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// Schedule prefixes. A job runs once after a delay, or repeatedly on an interval.
const (
	KindDelay = "delay"
	KindEvery = "every"
)

// Job represents a scheduled job.
type Job struct {
	ID string `json:"id"`
	// Schedule format: "delay:1s" (run once after), "every:1s" (recurring)
	Schedule string `json:"schedule"`
	Channel  string `json:"channel"`
	Content  string `json:"content"`
	// DueAt is the absolute moment a one-shot ("delay:") job is due. It is set
	// when the job is added and persisted, so a restart resumes the original
	// countdown instead of starting a new one. Recurring ("every:") jobs leave
	// it zero.
	DueAt time.Time `json:"due_at,omitempty"`
}

// Service schedules jobs and publishes InboundMessage to the bus when triggered.
type Service struct {
	bus       *bus.MessageBus
	workspace string
	jobsPath  string

	mu      sync.Mutex
	jobs    map[string]Job
	running bool
	// cancels holds a per-job stop channel, so an individual job can be stopped
	// without stopping the whole service.
	cancels map[string]chan struct{}
	// stopCh is recreated on every Start so the service can be restarted.
	stopCh chan struct{}

	wg sync.WaitGroup
}

// NewService constructs a new cron service storing jobs under workspace/cron/jobs.json
func NewService(b *bus.MessageBus, workspace string) *Service {
	jobsDir := filepath.Join(workspace, "cron")
	_ = os.MkdirAll(jobsDir, 0o755)
	return &Service{
		bus:       b,
		workspace: workspace,
		jobsPath:  filepath.Join(jobsDir, "jobs.json"),
		jobs:      map[string]Job{},
		cancels:   map[string]chan struct{}{},
	}
}

// Load loads persisted jobs (if any).
func (s *Service) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Service) loadLocked() error {
	data, err := os.ReadFile(s.jobsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return err
	}
	backfilled := false
	for _, j := range jobs {
		// Jobs written before due_at existed carry no due moment. Treat them as
		// due one duration from load — the historical behaviour — rather than
		// firing every one of them at once on the next start. Persist the
		// backfill so the deadline does not slide forward on every reload.
		if j.DueAt.IsZero() {
			if kind, d, err := ParseSchedule(j.Schedule); err == nil && kind == KindDelay {
				j.DueAt = time.Now().Add(d)
				backfilled = true
			}
		}
		s.jobs[j.ID] = j
	}
	if backfilled {
		_ = s.saveLocked()
	}
	return nil
}

// Save persists current jobs.
func (s *Service) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked writes the job file. The caller must hold s.mu.
func (s *Service) saveLocked() error {
	jobs := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].ID < jobs[k].ID })

	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.jobsPath, data, 0o644)
}

// AddJob adds and schedules a job. Adding a job with an existing ID replaces it.
func (s *Service) AddJob(j Job) error {
	if j.ID == "" {
		return fmt.Errorf("job ID must not be empty")
	}
	kind, d, err := ParseSchedule(j.Schedule)
	if err != nil {
		return err
	}
	// Record when a one-shot job is actually due, so a restart resumes the
	// original countdown. A caller that supplied its own DueAt keeps it.
	if kind == KindDelay && j.DueAt.IsZero() {
		j.DueAt = time.Now().Add(d)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Replacing a job must not leave the previous timer running.
	s.stopJobLocked(j.ID)
	s.jobs[j.ID] = j
	err = s.saveLocked()
	if s.running {
		s.scheduleJobLocked(j)
	}
	return err
}

// DeleteJob removes a job and stops it if scheduled.
func (s *Service) DeleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("no job with ID %q", id)
	}
	s.stopJobLocked(id)
	delete(s.jobs, id)
	return s.saveLocked()
}

// ListJobs returns a copy of the current jobs, ordered by ID.
func (s *Service) ListJobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].ID < jobs[k].ID })
	return jobs
}

// Start begins scheduling jobs. It is safe to call after Stop.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return
	}
	s.running = true
	// Fresh stop channel: the previous one was closed by Stop.
	s.stopCh = make(chan struct{})

	_ = s.loadLocked()
	for _, j := range s.jobs {
		s.scheduleJobLocked(j)
	}
}

// Stop stops all scheduled jobs.
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	stopCh := s.stopCh
	s.cancels = map[string]chan struct{}{}
	s.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	s.wg.Wait()
}

// ParseSchedule splits a schedule into its kind and duration.
func ParseSchedule(schedule string) (kind string, d time.Duration, err error) {
	parts := strings.SplitN(schedule, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid schedule %q: want \"delay:<duration>\" or \"every:<duration>\"", schedule)
	}
	kind = parts[0]
	if kind != KindDelay && kind != KindEvery {
		return "", 0, fmt.Errorf("invalid schedule kind %q: want %q or %q", kind, KindDelay, KindEvery)
	}
	d, err = ParseDuration(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}
	if d <= 0 {
		return "", 0, fmt.Errorf("invalid schedule %q: duration must be positive", schedule)
	}
	return kind, d, nil
}

// ParseDuration parses a Go duration, additionally accepting a "d" (days) suffix
// which time.ParseDuration rejects but which people reach for when scheduling.
func ParseDuration(spec string) (time.Duration, error) {
	spec = strings.TrimSpace(spec)
	if rest, ok := strings.CutSuffix(spec, "d"); ok && rest != "" {
		// Only a bare number of days, e.g. "2d". time.ParseDuration validates
		// the numeric part for us by way of the hour suffix.
		if hours, err := time.ParseDuration(rest + "h"); err == nil {
			return hours * 24, nil
		}
	}
	return time.ParseDuration(spec)
}

// overdueGrace is how long a one-shot job that came due while the process was
// stopped waits before firing. It fires promptly, but not inside Start itself,
// so the bus and channels are up before the message lands.
const overdueGrace = 50 * time.Millisecond

// delayUntilDue returns how long a one-shot job should wait before firing.
// A job with a recorded due moment waits until that moment, however many
// restarts happen in between; one already past waits only overdueGrace. A job
// with no due moment (defensive: Load backfills these) falls back to its
// duration, which is the pre-due_at behaviour.
func delayUntilDue(j Job, d time.Duration, now time.Time) time.Duration {
	if j.DueAt.IsZero() {
		return d
	}
	if wait := j.DueAt.Sub(now); wait > 0 {
		return wait
	}
	return overdueGrace
}

// stopJobLocked cancels a single job's timer. The caller must hold s.mu.
func (s *Service) stopJobLocked(id string) {
	if ch, ok := s.cancels[id]; ok {
		close(ch)
		delete(s.cancels, id)
	}
}

// scheduleJobLocked starts the timer goroutine for a job. Caller holds s.mu.
func (s *Service) scheduleJobLocked(j Job) {
	kind, d, err := ParseSchedule(j.Schedule)
	if err != nil {
		return
	}

	cancel := make(chan struct{})
	s.cancels[j.ID] = cancel
	stopCh := s.stopCh

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		switch kind {
		case KindDelay:
			select {
			case <-time.After(delayUntilDue(j, d, time.Now())):
				s.publishJob(j)
				// A one-shot job is spent once it fires; drop it so it is not
				// replayed on the next start.
				s.retireJob(j.ID)
			case <-cancel:
			case <-stopCh:
			}
		case KindEvery:
			ticker := time.NewTicker(d)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					s.publishJob(j)
				case <-cancel:
					return
				case <-stopCh:
					return
				}
			}
		}
	}()
}

// retireJob removes a spent one-shot job from memory and disk.
func (s *Service) retireJob(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return
	}
	delete(s.jobs, id)
	delete(s.cancels, id)
	_ = s.saveLocked()
}

func (s *Service) publishJob(j Job) {
	inbound := bus.InboundMessage{
		SenderID:  "cron",
		Content:   j.Content,
		Channel:   j.Channel,
		Timestamp: time.Now(),
		Metadata:  map[string]any{"job_id": j.ID},
	}
	_ = s.bus.Send(inbound)
}
