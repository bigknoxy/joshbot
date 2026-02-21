package heartbeat

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// Service watches HEARTBEAT.md for actionable checkbox tasks and publishes them to the bus.
type Service struct {
	bus       *bus.MessageBus
	workspace string
	path      string
	interval  time.Duration
	ticker    *time.Ticker
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewService creates a heartbeat service. interval defaults to 30m if zero.
func NewService(b *bus.MessageBus, workspace string) *Service {
	p := filepath.Join(workspace, "HEARTBEAT.md")
	return &Service{bus: b, workspace: workspace, path: p, interval: 30 * time.Minute, stopCh: make(chan struct{})}
}

// SetInterval overrides the polling interval. Must be called before Start().
func (s *Service) SetInterval(d time.Duration) {
	if d > 0 {
		s.interval = d
	}
}

// Start begins polling HEARTBEAT.md and publishing tasks.
func (s *Service) Start() {
	if s.interval <= 0 {
		s.interval = 30 * time.Minute
	}
	s.ticker = time.NewTicker(s.interval)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Run an immediate scan first
		s.scanAndPublish()
		for {
			select {
			case <-s.ticker.C:
				s.scanAndPublish()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop stops the service.
func (s *Service) Stop() {
	if s.ticker != nil {
		s.ticker.Stop()
	}
	close(s.stopCh)
	s.wg.Wait()
}

var checkboxRE = regexp.MustCompile(`(?m)^\s*[-*]\s*\[ \]\s*(.+)$`)

func (s *Service) scanAndPublish() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	matches := checkboxRE.FindAllStringSubmatch(string(data), -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		task := m[1]
		inbound := bus.InboundMessage{
			SenderID:  "heartbeat",
			Content:   task,
			Channel:   "all",
			Timestamp: time.Now(),
			Metadata:  map[string]any{"source": "heartbeat"},
		}
		_ = s.bus.Send(inbound)
	}
}
