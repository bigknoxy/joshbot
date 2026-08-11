package mcp

import "sync"

// Manager owns a set of MCP clients and provides a single Close that reaps them
// all. It is the lifecycle handle the caller keeps for the process's lifetime.
type Manager struct {
	mu      sync.Mutex
	clients []*Client
}

// NewManager builds a Manager with one client per server spec. No processes are
// started until each client's Connect is called (Clients + Connect).
func NewManager(servers []Server) *Manager {
	m := &Manager{}
	for _, s := range servers {
		m.clients = append(m.clients, NewClient(s))
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

// Close shuts down every managed server. Errors are swallowed per-client since
// there is nothing actionable to do on shutdown; each client still reaps its
// process.
func (m *Manager) Close() {
	m.mu.Lock()
	clients := m.clients
	m.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
}
