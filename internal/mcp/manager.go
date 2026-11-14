package mcp

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/charmbracelet/log"
)

// ServerStatus describes the observability and connection health of an MCP server.
type ServerStatus struct {
	Name            string        `json:"name"`
	Connected       bool          `json:"connected"`
	Reconnecting    bool          `json:"reconnecting"`
	Attempts        int           `json:"attempts"`
	LastAttempt     time.Time     `json:"last_attempt,omitempty"`
	LastError       string        `json:"last_error,omitempty"`
	NextRetry       time.Time     `json:"next_retry,omitempty"`
	BackoffDuration time.Duration `json:"backoff_duration,omitempty"`
}

// ReconnectConfig configures the exponential backoff parameters for reconnecting
// dead MCP child processes.
type ReconnectConfig struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	MaxAttempts     int
	JitterFraction  float64
}

// DefaultReconnectConfig returns sensible defaults for MCP server reconnect backoff.
func DefaultReconnectConfig() ReconnectConfig {
	return ReconnectConfig{
		InitialInterval: 500 * time.Millisecond,
		MaxInterval:     30 * time.Second,
		MaxAttempts:     10,
		JitterFraction:  0.25,
	}
}

// serverState tracks the runtime reconnect and health state for one managed client.
type serverState struct {
	client       *Client
	reconnecting bool
	attempts     int
	lastAttempt  time.Time
	lastError    error
	nextRetry    time.Time
	backoff      time.Duration
}

// Manager owns a set of MCP clients and provides automatic reconnection with
// exponential backoff, health observability, and a single Close that reaps them all.
type Manager struct {
	mu      sync.Mutex
	clients []*Client
	states  map[string]*serverState
	cfg     ReconnectConfig
	closed  bool
	stopCh  chan struct{}
	wg      sync.WaitGroup

	// now and sleepFn are injectable for fast, deterministic unit testing.
	now     func() time.Time
	sleepFn func(time.Duration) <-chan time.Time
	rnd     *rand.Rand
}

// NewManager builds a Manager with one client per server spec and default reconnect config.
// No processes are started until each client's Connect is called.
func NewManager(servers []Server) *Manager {
	return NewManagerWithConfig(servers, DefaultReconnectConfig())
}

// NewManagerWithConfig builds a Manager with explicit reconnect backoff configuration.
func NewManagerWithConfig(servers []Server, cfg ReconnectConfig) *Manager {
	m := &Manager{
		states:  make(map[string]*serverState),
		cfg:     cfg,
		stopCh:  make(chan struct{}),
		now:     time.Now,
		sleepFn: time.After,
		rnd:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	for _, s := range servers {
		c := NewClient(s)
		m.clients = append(m.clients, c)
		m.states[s.Name] = &serverState{
			client: c,
		}
	}
	return m
}

// Clients returns the managed clients.
func (m *Manager) Clients() []*Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Client, len(m.clients))
	copy(out, m.clients)
	return out
}

// Client returns the client for the named server, or nil if not managed.
func (m *Manager) Client(name string) *Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.states[name]; ok {
		return st.client
	}
	return nil
}

// Status returns a snapshot of every managed server's connection and reconnect state.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]ServerStatus, 0, len(m.clients))
	for _, c := range m.clients {
		st := m.states[c.Name()]
		connected := c.IsConnected()
		var lastErrStr string
		if st.lastError != nil {
			lastErrStr = st.lastError.Error()
		}
		out = append(out, ServerStatus{
			Name:            c.Name(),
			Connected:       connected,
			Reconnecting:    st.reconnecting,
			Attempts:        st.attempts,
			LastAttempt:     st.lastAttempt,
			LastError:       lastErrStr,
			NextRetry:       st.nextRetry,
			BackoffDuration: st.backoff,
		})
	}
	return out
}

// StartMonitor watches for process deaths and reconnects dead servers automatically
// with exponential backoff and jitter. It monitors each client's SessionDone channel.
func (m *Manager) StartMonitor(ctx context.Context) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	clients := make([]*Client, len(m.clients))
	copy(clients, m.clients)
	m.mu.Unlock()

	for _, c := range clients {
		m.wg.Add(1)
		go m.watchLoop(ctx, c)
	}
}

// watchLoop monitors a single client. When its active session exits unexpectedly,
// watchLoop coordinates backoff and attempts reconnection.
func (m *Manager) watchLoop(ctx context.Context, c *Client) {
	defer m.wg.Done()

	for {
		done := c.SessionDone()

		select {
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		case <-done:
			// Process died or was not yet connected.
			// If manager or client is closed, stop watching.
			if m.isClosed() || c.IsClosed() {
				return
			}

			// If the client is still considered connected (i.e. this done channel was
			// from a session that just terminated), trigger reconnect.
			m.handleServerDeath(ctx, c)
		}
	}
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// handleServerDeath runs the backoff retry loop for a killed MCP server until
// it reconnects, reaches max attempts, or is closed.
func (m *Manager) handleServerDeath(ctx context.Context, c *Client) {
	name := c.Name()

	m.mu.Lock()
	if m.closed || c.IsClosed() {
		m.mu.Unlock()
		return
	}
	st := m.states[name]
	if st.reconnecting {
		// Another goroutine is already reconnecting this server.
		m.mu.Unlock()
		return
	}
	st.reconnecting = true
	m.mu.Unlock()

	for {
		m.mu.Lock()
		if m.closed || c.IsClosed() {
			st.reconnecting = false
			m.mu.Unlock()
			return
		}

		if m.cfg.MaxAttempts > 0 && st.attempts >= m.cfg.MaxAttempts {
			// Permanently dead; stop spinning so agent is not blocked.
			st.reconnecting = false
			log.Warn("mcp: server permanently dead, reached max reconnect attempts",
				"server", name,
				"attempts", st.attempts,
				"last_error", st.lastError,
			)
			m.mu.Unlock()
			return
		}

		delay := m.calculateBackoff(st.attempts)
		st.backoff = delay
		st.nextRetry = m.now().Add(delay)
		sleepCh := m.sleepFn(delay)
		attempts := st.attempts + 1
		m.mu.Unlock()

		log.Info("mcp: server disconnected, scheduling reconnect",
			"server", name,
			"attempt", attempts,
			"delay", delay,
		)

		select {
		case <-m.stopCh:
			m.mu.Lock()
			st.reconnecting = false
			m.mu.Unlock()
			return
		case <-ctx.Done():
			m.mu.Lock()
			st.reconnecting = false
			m.mu.Unlock()
			return
		case <-sleepCh:
		}

		m.mu.Lock()
		if m.closed || c.IsClosed() {
			st.reconnecting = false
			m.mu.Unlock()
			return
		}
		st.attempts++
		st.lastAttempt = m.now()
		m.mu.Unlock()

		// Attempt reconnection with a bounded handshake context.
		connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := c.Connect(connectCtx)
		cancel()

		m.mu.Lock()
		if err == nil {
			// Successfully reconnected! Reset backoff state.
			st.reconnecting = false
			st.attempts = 0
			st.lastError = nil
			st.backoff = 0
			st.nextRetry = time.Time{}
			log.Info("mcp: server reconnected successfully", "server", name)
			m.mu.Unlock()
			return
		}

		st.lastError = err
		log.Warn("mcp: server reconnect attempt failed",
			"server", name,
			"attempt", st.attempts,
			"error", err,
		)
		m.mu.Unlock()
	}
}

// calculateBackoff computes exponential backoff with cap and jitter.
// Caller holds m.mu or passes parameters safely.
func (m *Manager) calculateBackoff(attempt int) time.Duration {
	base := m.cfg.InitialInterval
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	maxD := m.cfg.MaxInterval
	if maxD <= 0 {
		maxD = 30 * time.Second
	}

	// base * 2^attempt with overflow protection
	shift := attempt
	if shift > 30 {
		shift = 30
	}
	d := base << shift
	if d > maxD || d <= 0 {
		d = maxD
	}

	// Add jitter: up to JitterFraction * d
	fraction := m.cfg.JitterFraction
	if fraction > 0 {
		jitterMax := int64(float64(d) * fraction)
		if jitterMax > 0 {
			var jitter int64
			if m.rnd != nil {
				jitter = m.rnd.Int63n(jitterMax)
			} else {
				jitter = rand.Int63n(jitterMax)
			}
			d += time.Duration(jitter)
		}
	}
	if d > maxD {
		d = maxD
	}
	return d
}

// Close shuts down every managed server and stops the reconnect monitor.
// Errors are swallowed per-client since there is nothing actionable to do on shutdown;
// each client still reaps its process.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stopCh)
	clients := m.clients
	m.mu.Unlock()

	// Wait for any running watchLoop goroutines to exit.
	m.wg.Wait()

	for _, c := range clients {
		_ = c.Close()
	}
}
