package heartbeat

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
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

	// published remembers every task text this process has already put on the
	// bus. Checking the box off is the durable record, but it is written after
	// the sends and can fail (read-only workspace, full disk, lost
	// permissions) — without this set a persistent write failure re-publishes
	// every task on every tick forever, which is exactly the unbounded token
	// burn checking off was meant to end. Best-effort by design: it is
	// per-process, so a restart falls back to the file, which is correct
	// because a restart is also when the write may have started succeeding.
	publishedMu sync.Mutex
	published   map[string]struct{}
}

// NewService creates a heartbeat service. interval defaults to 30m if zero.
func NewService(b *bus.MessageBus, workspace string) *Service {
	p := filepath.Join(workspace, "HEARTBEAT.md")
	return &Service{
		bus:       b,
		workspace: workspace,
		path:      p,
		interval:  30 * time.Minute,
		stopCh:    make(chan struct{}),
		published: make(map[string]struct{}),
	}
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

// uncheckedRE matches a single unchecked task line. It is deliberately the ONLY
// pattern in this file: publishing and checking off must agree exactly, or a
// line that can be published but not checked off re-fires on every tick,
// forever. Two separate patterns used to disagree here — "-[ ] task" and
// "* [ ]task" published but never got checked off.
//
// Group 1 is the indent + bullet prefix, group 2 the whitespace between the box
// and the task text, group 3 the task text. Keeping 1 and 2 verbatim lets the
// box be flipped to [x] in place, preserving indent, bullet style and spacing.
var uncheckedRE = regexp.MustCompile(`^([ \t]*[-*+][ \t]*)\[ \]([ \t]*)(\S.*?)[ \t]*$`)

// parseTask reports whether line is an unchecked task, returning the task text
// and the line with its box flipped to [x].
func parseTask(line string) (task, checked string, ok bool) {
	// Preserve a trailing CR so CRLF files survive the rewrite unchanged.
	body, cr := line, ""
	if strings.HasSuffix(body, "\r") {
		body, cr = body[:len(body)-1], "\r"
	}
	m := uncheckedRE.FindStringSubmatch(body)
	if m == nil {
		return "", "", false
	}
	return m[3], m[1] + "[x]" + m[2] + m[3] + cr, true
}

func (s *Service) scanAndPublish() {
	data, err := os.ReadFile(s.path)
	if err != nil {
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

	lines := strings.Split(string(data), "\n")
	changed := false
	for i, line := range lines {
		task, checked, ok := parseTask(line)
		if !ok {
			continue
		}
		// Already published by this process: the box could not be persisted,
		// but the task has been dispatched and must not fire again.
		if s.alreadyPublished(task) {
			// Still flip the box in memory so a later successful write records
			// it, rather than leaving it to be rediscovered forever.
			lines[i] = checked
			changed = true
			continue
		}
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
		// Only check a task off once it is actually queued. A dropped Send with
		// the box flipped anyway loses the task silently and forever.
		if !s.bus.Send(inbound) {
			log.Warnf("heartbeat: bus queue full, task left unchecked: %s", task)
			continue
		}
		s.markPublished(task)
		// One-shot per task: check off every task just published so it does not
		// re-fire on the next tick. This is what makes the heartbeat stop
		// burning tokens on the same tasks forever.
		lines[i] = checked
		changed = true
	}
	if !changed {
		return
	}
	if err := writeFileAtomic(s.path, []byte(strings.Join(lines, "\n"))); err != nil {
		log.Warnf("heartbeat: failed to update %s: %v (tasks will not be re-published by this process)", s.path, err)
	}
}

// alreadyPublished reports whether this process has already put task on the bus.
func (s *Service) alreadyPublished(task string) bool {
	s.publishedMu.Lock()
	defer s.publishedMu.Unlock()
	_, ok := s.published[task]
	return ok
}

// markPublished records that task has been dispatched, so a failure to persist
// the checked box cannot make it fire again on the next tick.
func (s *Service) markPublished(task string) {
	s.publishedMu.Lock()
	defer s.publishedMu.Unlock()
	if s.published == nil {
		s.published = make(map[string]struct{})
	}
	s.published[task] = struct{}{}
}

// writeFileAtomic rewrites path via a uniquely named temp file in the same
// directory plus a rename, preserving the existing file's mode.
//
// The read-modify-write in scanAndPublish spans a tick, so a user editing
// HEARTBEAT.md in that window would otherwise see their edit half-clobbered by
// a partial write. The temp name must be unique per writer (os.CreateTemp), not
// a fixed "<path>.tmp": two writers sharing one temp file interleave and the
// surviving rename publishes a torn mix. This mirrors the identical helper in
// internal/session/manager.go; the two are kept separate rather than shared
// because session's is unexported and hoisting it into a common package for two
// call sites is not yet worth the dependency.
func writeFileAtomic(path string, data []byte) error {
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to set temporary file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("failed to rename temporary file: %w", err)
	}
	// Fsync the directory so the rename itself is durable: the file contents are
	// synced above, but a crash before the directory entry reaches disk can
	// still lose the new name. Best-effort — some filesystems refuse to sync a
	// directory, and the write itself has already succeeded.
	if dirF, err := os.Open(dir); err == nil {
		_ = dirF.Sync()
		_ = dirF.Close()
	}
	return nil
}
