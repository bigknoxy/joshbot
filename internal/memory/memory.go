package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return fmt.Errorf("write template %s: %w", path, err)
	}

	return nil
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
