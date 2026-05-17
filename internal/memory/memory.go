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

// Manager provides thread-safe access to the MEMORY.md and HISTORY.md files.
type Manager struct {
	workspace string
	memoryDir string
	now       func() time.Time
	mu        sync.RWMutex
}

// New returns a Manager rooted inside the provided workspace directory.
func New(workspace string) (*Manager, error) {
	memoryDir := filepath.Join(workspace, "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}

	return &Manager{
		workspace: workspace,
		memoryDir: memoryDir,
		now:       time.Now,
	}, nil
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
func (m *Manager) LoadMemory(ctx context.Context) (string, error) {
	path := m.MemoryPath()

	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read memory: %w", err)
	}

	return string(data), nil
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

	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
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

	f, err := os.OpenFile(m.HistoryPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(formatted); err != nil {
		return fmt.Errorf("append history: %w", err)
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
		if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
			return fmt.Errorf("write template %s: %w", path, err)
		}
	}
	return nil
}

// WriteFacts writes structured facts into MEMORY.md, organized by category.
func (m *Manager) WriteFacts(ctx context.Context, facts []Fact) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Group facts by category
	byCategory := map[FactCategory][]Fact{}
	for _, f := range facts {
		byCategory[f.Category] = append(byCategory[f.Category], f)
	}

	// Build markdown content
	var b strings.Builder
	b.WriteString("# Long-Term Memory\n\n")

	// Sort categories for deterministic output
	categories := make([]FactCategory, 0, len(byCategory))
	for cat := range byCategory {
		categories = append(categories, cat)
	}
	sort.Slice(categories, func(i, j int) bool {
		return categories[i] < categories[j]
	})

	for _, cat := range categories {
		b.WriteString(fmt.Sprintf("## %s\n", string(cat)))
		for _, f := range byCategory[cat] {
			b.WriteString(FormatFactMarkdown(f))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Atomic write
	path := m.MemoryPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write temp memory: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename memory: %w", err)
	}

	return nil
}

// ReconcileFacts merges new facts with existing memory (upsert + dedup + evict).
func (m *Manager) ReconcileFacts(ctx context.Context, facts []Fact) error {
	existing, err := m.loadFactsLocked()
	if err != nil {
		return err
	}

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

	return m.WriteFacts(ctx, merged)
}

const defaultMemoryTemplate = `# Long-Term Memory

## User Information
- (facts about the user will accumulate here)

## Preferences
- (preferences, likes, dislikes)

## Projects & Context
- (project details and decisions)

## Important Notes
- (critical reminders the agent must never forget)
`

const defaultHistoryTemplate = `# Conversation History

- Append short 2-5 sentence summaries here.
`
