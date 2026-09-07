// Package gatewaystatus provides a file-based status bridge between a running
// gateway and the `joshbot status` command. The gateway writes;
// status reads. No shared memory, no sockets — just a JSON file that the
// gateway atomically replaces on each channel transition.
package gatewaystatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Path returns the canonical location of the gateway status file.
// joshbotHome is the joshbot home directory (typically ~/.joshbot).
func Path(joshbotHome string) string {
	return filepath.Join(joshbotHome, "gateway-status.json")
}

// ChannelState is the per-channel live state. The string values match
// channels.ConnectionState exactly so they can be compared without import.
type ChannelState struct {
	Name      string    `json:"name"`
	State     string    `json:"state"`     // connected, reconnecting, down, disabled
	UpdatedAt time.Time `json:"updated_at"`
}

// Document is the top-level structure written to the status file.
type Document struct {
	// PID is the gateway process ID, or 0 if no gateway is running.
	PID int `json:"pid"`
	// StartedAt is the time the gateway process entered its main loop, or zero
	// if no gateway is running.
	StartedAt time.Time `json:"started_at,omitempty"`
	// Channels holds the live state for each enabled channel.
	Channels []ChannelState `json:"channels"`
	// WrittenAt is the time this document was last written to disk.
	WrittenAt time.Time `json:"written_at"`
}

// Writer atomically writes gateway status documents to a JSON file.
type Writer struct {
	mu       sync.Mutex
	path     string
	document Document
}

// NewWriter creates a status writer targeting the given file path.
func NewWriter(path string) *Writer {
	return &Writer{path: path}
}

// SetPID records the gateway PID and start time.
func (w *Writer) SetPID(pid int, startedAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.document.PID = pid
	w.document.StartedAt = startedAt
}

// SetChannel updates a single channel's live state and writes the file.
func (w *Writer) SetChannel(name, state string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().UTC()
	for i := range w.document.Channels {
		if w.document.Channels[i].Name == name {
			w.document.Channels[i].State = state
			w.document.Channels[i].UpdatedAt = now
			w.write(now)
			return
		}
	}
	w.document.Channels = append(w.document.Channels, ChannelState{
		Name:      name,
		State:     state,
		UpdatedAt: now,
	})
	w.write(now)
}

// RemoveChannel removes a channel from the status document (used when a
// channel is disabled and should not appear in the status output at all).
func (w *Writer) RemoveChannel(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for i := range w.document.Channels {
		if w.document.Channels[i].Name == name {
			w.document.Channels = append(w.document.Channels[:i], w.document.Channels[i+1:]...)
			break
		}
	}
	w.write(time.Now().UTC())
}

// write serialises the document and atomically replaces the file.
func (w *Writer) write(now time.Time) {
	w.document.WrittenAt = now
	data, err := json.Marshal(w.document)
	if err != nil {
		return
	}
	// Write to a temp file in the same directory, then rename for atomicity.
	dir := filepath.Dir(w.path)
	tmp, err := os.CreateTemp(dir, "gateway-status-*.tmp")
	if err != nil {
		return
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return
	}
	tmp.Close()
	os.Rename(tmp.Name(), w.path)
}

// Delete removes the status file. Called on gateway shutdown so a stale
// file does not outlive the process.
func (w *Writer) Delete() {
	os.Remove(w.path)
}

// Read loads the gateway status file. Returns nil if the file does not
// exist or cannot be parsed — the caller treats this as "gateway not running".
func Read(path string) *Document {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return &doc
}
