package cron

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// Job represents a scheduled job.
type Job struct {
	ID string `json:"id"`
	// Schedule format: "delay:1s" (run once after), "every:1s" (recurring)
	Schedule string `json:"schedule"`
	Channel  string `json:"channel"`
	Content  string `json:"content"`
}

// Service schedules jobs and publishes InboundMessage to the bus when triggered.
type Service struct {
	bus       *bus.MessageBus
	workspace string
	jobsPath  string
	jobs      map[string]Job
	mu        sync.Mutex
	running   bool
	wg        sync.WaitGroup
	stopCh    chan struct{}
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
		stopCh:    make(chan struct{}),
	}
}

// Load loads persisted jobs (if any).
func (s *Service) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	for _, j := range jobs {
		s.jobs[j.ID] = j
	}
	return nil
}

// Save persists current jobs.
func (s *Service) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var jobs []Job
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.jobsPath, data, 0o644)
}

// AddJob adds and schedules a job.
func (s *Service) AddJob(j Job) error {
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.mu.Unlock()
	_ = s.Save()
	if s.running {
		s.scheduleJob(j)
	}
	return nil
}

// Start begins scheduling jobs.
func (s *Service) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	// load persisted jobs
	_ = s.Load()

	s.mu.Lock()
	for _, j := range s.jobs {
		s.scheduleJob(j)
	}
	s.mu.Unlock()
}

// Stop stops all scheduled jobs.
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()
	close(s.stopCh)
	s.wg.Wait()
}

func (s *Service) scheduleJob(j Job) {
	// support 'delay:<duration>' and 'every:<duration>'
	parts := stringsSplitN(j.Schedule, ":", 2)
	if len(parts) != 2 {
		return
	}
	kind, spec := parts[0], parts[1]
	d, err := time.ParseDuration(spec)
	if err != nil {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		switch kind {
		case "delay":
			select {
			case <-time.After(d):
				s.publishJob(j)
			case <-s.stopCh:
				return
			}
		case "every":
			ticker := time.NewTicker(d)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					s.publishJob(j)
				case <-s.stopCh:
					return
				}
			}
		}
	}()
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

// Helper: strings.SplitN wrapper (avoid importing strings repeatedly)
func stringsSplitN(s, sep string, n int) []string {
	// lightweight wrapper
	return strings.SplitN(s, sep, n)
}
