package heartbeat

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// Contract is appended to every published heartbeat task. It tells the agent
// the message is an automated background check and that it must reply with
// exactly HEARTBEAT_OK when nothing needs the user's attention, so the gateway
// can suppress a noise reply instead of delivering it.
const Contract = "\n\n[heartbeat] This is an automated background check, not a message from the user. " +
	"Do the task only if it genuinely needs the user's attention right now. " +
	"If nothing needs their attention, reply with exactly HEARTBEAT_OK and nothing else."

// Service watches HEARTBEAT.md for actionable checkbox tasks and publishes them to the bus.
type Service struct {
	bus       *bus.MessageBus
	workspace string
	path      string
	interval  time.Duration
	// channel is where heartbeat tasks are published. Empty means "all".
	channel string
	// resolveChatID looks up the stored chat ID for a channel. When set, a task
	// is only published if a chat ID is known, so its result has a real
	// recipient. nil means publish unconditionally (used by unit tests).
	resolveChatID func(channel string) (string, bool)
	ticker        *time.Ticker
	stopCh        chan struct{}
	wg            sync.WaitGroup
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

// SetChannel sets the channel heartbeat tasks are published to. An empty value
// leaves the default ("all"). Must be called before Start().
func (s *Service) SetChannel(channel string) {
	if channel != "" {
		s.channel = channel
	}
}

// SetChatIDResolver installs a lookup for a channel's stored chat ID. When set,
// a task is only published once a chat ID is known for its channel, so its
// result is deliverable. Must be called before Start().
func (s *Service) SetChatIDResolver(fn func(channel string) (string, bool)) {
	s.resolveChatID = fn
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

// checkboxRE matches an unchecked task line, capturing the task text.
var checkboxRE = regexp.MustCompile(`(?m)^(?:\s*[-*]\s*)\[ \]\s*(.+)$`)

// uncheckedRE matches an unchecked task line, capturing the bullet prefix and
// the remainder so the box can be flipped to [x] in place, preserving indent,
// bullet style and task text.
var uncheckedRE = regexp.MustCompile(`(?m)^(\s*[-*]\s+)\[ \](\s+\S.*)$`)

func (s *Service) scanAndPublish() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	matches := checkboxRE.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		return
	}

	channel := s.channel
	if channel == "" {
		channel = "all"
	}

	// Resolve the recipient once. Without a known chat ID the agent's result
	// would be undeliverable ("no valid recipient"), so skip this tick entirely
	// and leave the boxes unchecked to retry once a chat becomes known.
	var chatID string
	if s.resolveChatID != nil {
		id, ok := s.resolveChatID(channel)
		if !ok || id == "" {
			return
		}
		chatID = id
	}

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		task := strings.TrimSpace(m[1])
		meta := map[string]any{"source": "heartbeat"}
		if chatID != "" {
			meta["chat_id"] = chatID
		}
		inbound := bus.InboundMessage{
			SenderID:  "heartbeat",
			Content:   task + Contract,
			Channel:   channel,
			Timestamp: time.Now(),
			Metadata:  meta,
		}
		_ = s.bus.Send(inbound)
	}

	// One-shot per task: check off every task just published so it does not
	// re-fire on the next tick. This is what makes the heartbeat stop burning
	// tokens on the same tasks forever.
	newData := uncheckedRE.ReplaceAll(data, []byte("${1}[x]${2}"))
	if !bytes.Equal(newData, data) {
		_ = os.WriteFile(s.path, newData, 0o644)
	}
}
