package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrEmptyContent is returned when a write is attempted with no content.
var ErrEmptyContent = errors.New("content cannot be empty")

// Option configures the Manager.
type Option func(*Manager)

// WithMaxSize sets the maximum size in bytes for MEMORY.md content.
// Content exceeding this limit will be trimmed (newest entries kept).
func WithMaxSize(size int) Option {
	return func(m *Manager) {
		if size > 0 {
			m.maxSize = size
		}
	}
}

// Manager provides thread-safe access to the MEMORY.md and HISTORY.md files.
type Manager struct {
	workspace string
	memoryDir string
	now       func() time.Time
	mu        sync.RWMutex
	maxSize   int
	dream     *DreamManager // nil when Dream consolidation is disabled
}

// New returns a Manager rooted inside the provided workspace directory.
// Options can be provided to configure behavior (e.g., WithMaxSize).
func New(workspace string, opts ...Option) (*Manager, error) {
	memoryDir := filepath.Join(workspace, "memory")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}

	m := &Manager{
		workspace: workspace,
		memoryDir: memoryDir,
		now:       time.Now,
		maxSize:   4096, // default max memory size
	}

	for _, opt := range opts {
		opt(m)
	}

	return m, nil
}

// MemoryPath returns the location of MEMORY.md.
func (m *Manager) MemoryPath() string {
	return filepath.Join(m.memoryDir, "MEMORY.md")
}

// HistoryPath returns the location of HISTORY.md.
func (m *Manager) HistoryPath() string {
	return filepath.Join(m.memoryDir, "HISTORY.md")
}

// LoadMemory reads MEMORY.md for inclusion in the system prompt.
// If the content exceeds the configured maxSize, it is trimmed to fit
// by keeping the header and the newest entries, and the trimmed result
// is written back to disk.
func (m *Manager) LoadMemory(ctx context.Context) (string, error) {
	path := m.MemoryPath()

	m.mu.RLock()
	data, err := os.ReadFile(path)
	m.mu.RUnlock()
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read memory: %w", err)
	}

	content := string(data)
	if len(content) > m.maxSize {
		content = trimMemoryContent(content, m.maxSize)
		// Write trimmed version back to disk under write lock
		m.mu.Lock()
		_ = os.WriteFile(path, []byte(content), 0o600)
		m.mu.Unlock()
	}

	return content, nil
}

// trimMemoryContent trims content to fit within maxSize bytes.
// It splits on "---" separators, keeps the header (before first "---"),
// and retains the newest entries (from the end) until the limit is reached.
// If there are no "---" separators, it truncates to maxSize.
func trimMemoryContent(content string, maxSize int) string {
	if len(content) <= maxSize {
		return content
	}

	// Split on --- separators
	separator := "\n---\n"
	parts := strings.Split(content, separator)

	if len(parts) <= 1 {
		// No separators, just truncate to maxSize
		return content[:maxSize]
	}

	// First part is the header (title, metadata), always keep it
	header := parts[0]
	entries := parts[1:]

	// If even the header is over maxSize, truncate it
	if len(header) > maxSize {
		return header[:maxSize]
	}

	// Build result: header + as many newest entries as fit
	var result strings.Builder
	result.WriteString(header)

	// Add entries from the end (newest) until we'd exceed maxSize
	for i := len(entries) - 1; i >= 0; i-- {
		candidate := result.String() + separator + entries[i]
		if len(candidate) <= maxSize {
			result.Reset()
			result.WriteString(candidate)
		} else {
			break
		}
	}

	return result.String()
}

// LoadHistory returns the entire HISTORY.md file. The query is currently ignored.
func (m *Manager) LoadHistory(ctx context.Context, _ string) (string, error) {
	path := m.HistoryPath()

	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read history: %w", err)
	}

	return string(data), nil
}

// WriteMemory overwrites MEMORY.md with the provided content.
func (m *Manager) WriteMemory(ctx context.Context, content string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if content == "" {
		return ErrEmptyContent
	}

	if content[len(content)-1] != '\n' {
		content += "\n"
	}

	path := m.MemoryPath()
	tmp := path + ".tmp"

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write temp memory: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename memory: %w", err)
	}

	return nil
}

// AppendHistory appends a timestamped entry to HISTORY.md.
func (m *Manager) AppendHistory(ctx context.Context, entry string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if entry == "" {
		return ErrEmptyContent
	}

	timestamp := m.now().UTC().Format("[2006-01-02 15:04]")
	formatted := fmt.Sprintf("\n%s %s\n", timestamp, entry)

	m.mu.Lock()
	defer m.mu.Unlock()

	f, err := os.OpenFile(m.HistoryPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(formatted); err != nil {
		return fmt.Errorf("append history: %w", err)
	}

	// Stage 1 of Dream consolidation rides on the history append rather than on
	// a separate hook in the ReAct loop: the agent already records one entry per
	// turn here, and a second recording point would drift out of step with it.
	// A failed record is not a failed append — the history file is the durable
	// record and the raw dream log is derived from it.
	if m.dream != nil {
		_ = m.dream.Record(ctx, DreamRecord{Type: DreamThought, Content: entry})
	}

	return nil
}

// Initialize ensures both memory files exist with default templates.
func (m *Manager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureFile(m.MemoryPath(), defaultMemoryTemplate); err != nil {
		return err
	}

	if err := m.ensureFile(m.HistoryPath(), defaultHistoryTemplate); err != nil {
		return err
	}

	return nil
}

func (m *Manager) ensureFile(path, template string) error {
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(template), 0o600); err != nil {
			return fmt.Errorf("write template %s: %w", path, err)
		}
	}
	return nil
}

// WriteFacts replaces the fact sections of MEMORY.md with the given facts.
//
// This is the destructive primitive: it rewrites the whole document from the
// fact set, so any content that is not a fact is discarded — including the
// default template's own sections. ReconcileFacts is the merging path and is
// what the consolidation flow uses; prefer it unless replacing the document
// is exactly what you want.
func (m *Manager) WriteFacts(ctx context.Context, facts []Fact) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.writeDocument(renderFactsDocument(facts))
}

// writeDocument writes content to MEMORY.md atomically. The caller holds the
// write lock.
func (m *Manager) writeDocument(content string) error {
	path := m.MemoryPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write temp memory: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename memory: %w", err)
	}
	return nil
}

// ReconcileFacts merges new facts with existing memory (upsert + dedup + evict).
//
// The document is merged, not rebuilt: the fact lines are updated in place and
// every other line — the user's prose, the template's comments, headings they
// added — is preserved byte for byte. Rebuilding the file from the fact set,
// which is what this used to do, erased all of it on the first consolidation,
// including the default template's "critical reminders the agent must never
// forget".
func (m *Manager) ReconcileFacts(ctx context.Context, facts []Fact) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Read the document as it stands on disk. Reading it here, under the
	// write lock, is what keeps the read-modify-write atomic against a
	// concurrent consolidation.
	document, err := osReadFile(m.MemoryPath())
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read memory: %w", err)
		}
		document = nil
	}

	// Only the facts joshbot itself wrote count here. parseFacts is
	// permissive enough to read the user's own bullets as facts, and
	// counting those would give their prose a confidence of 1.0, rank it
	// above everything the system learned, and spend a fact slot on it.
	existing := parseSystemFacts(string(document))

	factMap := make(map[string]Fact, len(existing))
	for _, f := range existing {
		factMap[f.ID] = f
	}

	for _, f := range facts {
		if prev, ok := factMap[f.ID]; ok {
			factMap[f.ID] = MergeFacts(prev, f)
		} else {
			factMap[f.ID] = f
		}
	}

	merged := make([]Fact, 0, len(factMap))
	for _, f := range factMap {
		merged = append(merged, f)
	}

	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Confidence != merged[j].Confidence {
			return merged[i].Confidence > merged[j].Confidence
		}
		return merged[i].UpdatedAt.After(merged[j].UpdatedAt)
	})

	const maxFacts = 100
	if len(merged) > maxFacts {
		merged = merged[:maxFacts]
	}

	return m.writeDocument(mergeFactsDocument(string(document), merged))
}

const defaultMemoryTemplate = `# Long-Term Memory

## User Information
<!-- facts about the user will accumulate here -->

## Preferences
<!-- preferences, likes, dislikes -->

## Projects & Context
<!-- project details and decisions -->

## Important Notes
<!-- critical reminders the agent must never forget -->
`

const defaultHistoryTemplate = `# Conversation History

- Append short 2-5 sentence summaries here.
`
