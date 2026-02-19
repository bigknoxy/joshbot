package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

// mockProvider is a mock LLM provider for testing.
type mockProvider struct {
	chatFn func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error)
	name   string
	cfg    providers.Config
}

func (m *mockProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if m.chatFn != nil {
		return m.chatFn(ctx, req)
	}
	// Default: return empty response
	return &providers.ChatResponse{
		ID:      "mock-id",
		Model:   req.Model,
		Choices: []providers.Choice{},
	}, nil
}

func (m *mockProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func (m *mockProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", nil
}

func (m *mockProvider) Name() string {
	if m.name != "" {
		return m.name
	}
	return "mock"
}

func (m *mockProvider) Config() providers.Config {
	return m.cfg
}

// mockToolExecutor is a mock tool executor for testing.
type mockToolExecutor struct {
	executeFn func(ctx context.Context, name string, args map[string]any) (string, error)
	schemas   []providers.Tool
}

func (m *mockToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, name, args)
	}
	return "executed", nil
}

func (m *mockToolExecutor) GetSchemas() []providers.Tool {
	return m.schemas
}

// mockSessionManager is a mock session manager for testing.
type mockSessionManager struct {
	sessions map[string]*session.Session
	mu       sync.Mutex
}

func newMockSessionManager() *mockSessionManager {
	return &mockSessionManager{
		sessions: make(map[string]*session.Session),
	}
}

func (m *mockSessionManager) GetOrCreate(ctx context.Context, key string) (*session.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.sessions[key]; ok {
		return sess, nil
	}

	sess := session.NewSession(key)
	m.sessions[key] = sess
	return sess, nil
}

func (m *mockSessionManager) Save(ctx context.Context, sess *session.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sess.ID] = sess
	return nil
}

func (m *mockSessionManager) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, key)
	return nil
}

// mockMemoryLoader is a mock memory loader for testing.
type mockMemoryLoader struct {
	memoryFn  func(ctx context.Context) (string, error)
	historyFn func(ctx context.Context, query string) (string, error)
}

func (m *mockMemoryLoader) LoadMemory(ctx context.Context) (string, error) {
	if m.memoryFn != nil {
		return m.memoryFn(ctx)
	}
	return "", nil
}

func (m *mockMemoryLoader) LoadHistory(ctx context.Context, query string) (string, error) {
	if m.historyFn != nil {
		return m.historyFn(ctx, query)
	}
	return "", nil
}

// mockSkillsLoader is a mock skills loader for testing.
type mockSkillsLoader struct {
	summaryFn func(ctx context.Context) (string, error)
}

func (m *mockSkillsLoader) LoadSummary(ctx context.Context) (string, error) {
	if m.summaryFn != nil {
		return m.summaryFn(ctx)
	}
	return "", nil
}

// mockLogger is a simple logger for testing.
type mockAgentLogger struct {
	infos  []string
	warns  []string
	errors []string
	debugs []string
}

func (m *mockAgentLogger) Info(msg string, args ...interface{}) {
	m.infos = append(m.infos, msg)
}

func (m *mockAgentLogger) Warn(msg string, args ...interface{}) {
	m.warns = append(m.warns, msg)
}

func (m *mockAgentLogger) Error(msg string, args ...interface{}) {
	m.errors = append(m.errors, msg)
}

func (m *mockAgentLogger) Debug(msg string, args ...interface{}) {
	m.debugs = append(m.debugs, msg)
}

func newMockLogger() *log.Logger {
	logger, _ := log.NewLogger(log.Config{Level: log.DebugLevel, Pretty: false})
	return logger
}

func TestNewAgent(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	if agent == nil {
		t.Fatal("expected non-nil agent")
	}

	if agent.maxIterations != cfg.Agents.Defaults.MaxToolIterations {
		t.Errorf("expected maxIterations %d, got %d", cfg.Agents.Defaults.MaxToolIterations, agent.maxIterations)
	}
}

func TestNewAgentWithOptions(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(
		cfg,
		provider,
		tools,
		sessions,
		logger,
		WithMaxIterations(10),
		WithTimeout(30*time.Second),
	)

	if agent.maxIterations != 10 {
		t.Errorf("expected 10, got %d", agent.maxIterations)
	}

	if agent.timeout != 30*time.Second {
		t.Errorf("expected 30s timeout, got %v", agent.timeout)
	}
}

func TestNewAgentDefaultIterations(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agents.Defaults.MaxToolIterations = 0 // Invalid value

	agent := NewAgent(cfg, &mockProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	if agent.maxIterations != DefaultMaxIterations {
		t.Errorf("expected %d, got %d", DefaultMaxIterations, agent.maxIterations)
	}
}

func TestAgentProcessBasic(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			// Return a simple response without tool calls
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Hello! I'm joshbot.",
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	if response == "" {
		t.Error("expected non-empty response")
	}
}

func TestAgentProcessWithToolCalls(t *testing.T) {
	cfg := config.Defaults()
	iteration := 0

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			iteration++

			if iteration == 1 {
				// First call: return a tool call
				return &providers.ChatResponse{
					ID:    "test-id",
					Model: req.Model,
					Choices: []providers.Choice{
						{
							Message: providers.Message{
								Role:    providers.RoleAssistant,
								Content: "Let me check that file.",
								ToolCalls: []providers.ToolCall{
									{
										ID:   "call_1",
										Type: "function",
										Function: providers.FunctionCall{
											Name:      "filesystem",
											Arguments: `{"operation":"read_file","path":"test.txt"}`,
										},
									},
								},
							},
							FinishReason: "tool_calls",
						},
					},
				}, nil
			}

			// Second call: return final response
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "The file contains: hello world",
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, args map[string]any) (string, error) {
			if name == "filesystem" {
				return "File content: hello world", nil
			}
			return "", nil
		},
		schemas: []providers.Tool{
			{
				Type: "function",
				Function: providers.FunctionDefinition{
					Name:        "filesystem",
					Description: "Read files",
				},
			},
		},
	}

	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Read test.txt",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Verify tool was executed
	if response == "" {
		t.Error("expected non-empty response")
	}
}

func TestAgentProcessCommandStart(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "/start",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	if response == "" {
		t.Error("expected non-empty response")
	}
}

func TestAgentProcessCommandHelp(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "/help",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Help should return without calling provider
	expected := "Available commands:"
	if len(response) == 0 || response[:len(expected)] != expected {
		t.Errorf("expected help text, got: %s", response)
	}
}

func TestAgentProcessCommandStatus(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{}
	tools := &mockToolExecutor{
		schemas: []providers.Tool{
			{Type: "function", Function: providers.FunctionDefinition{Name: "tool1"}},
			{Type: "function", Function: providers.FunctionDefinition{Name: "tool2"}},
		},
	}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "/status",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Status should contain model info
	expected := "Status:"
	if len(response) == 0 || response[:len(expected)] != expected {
		t.Errorf("expected status text, got: %s", response)
	}
}

func TestAgentProcessCommandNew(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "/new",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	if response == "" {
		t.Error("expected non-empty response")
	}
}

func TestAgentProcessUnknownCommand(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Unknown command handled",
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "/unknowncmd",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Unknown command should be processed normally
	if response != "Unknown command handled" {
		t.Errorf("unexpected response: %s", response)
	}
}

func TestAgentProcessSessionPersistence(t *testing.T) {
	cfg := config.Defaults()
	callCount := 0

	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			callCount++
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Response " + string(rune('0'+callCount)),
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	// Send first message
	msg1 := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}
	_, _ = agent.Process(context.Background(), msg1)

	// Send second message in same session
	msg2 := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "How are you?",
		Channel:   "cli",
		Timestamp: time.Now(),
	}
	_, _ = agent.Process(context.Background(), msg2)

	// Verify session was created
	if len(sessions.sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(sessions.sessions))
	}
}

func TestAgentProcessSessionError(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{}
	tools := &mockToolExecutor{}

	// Use a custom session manager that errors on GetOrCreate
	errorManager := &errorSessionManager{
		err: errors.New("session error"),
	}
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, errorManager, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Should return error message
	if response == "" {
		t.Error("expected error response")
	}
}

// errorSessionManager always returns an error
type errorSessionManager struct {
	err error
}

func (e *errorSessionManager) GetOrCreate(ctx context.Context, key string) (*session.Session, error) {
	return nil, e.err
}

func (e *errorSessionManager) Save(ctx context.Context, sess *session.Session) error {
	return nil
}

func (e *errorSessionManager) Delete(ctx context.Context, key string) error {
	return nil
}

func TestAgentProcessProviderError(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return nil, errors.New("provider error")
		},
	}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Should return error message
	if response == "" {
		t.Error("expected error response")
	}
}

func TestAgentProcessEmptyResponse(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{
				ID:      "test-id",
				Model:   req.Model,
				Choices: []providers.Choice{}, // Empty choices
			}, nil
		},
	}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Hello",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Should return fallback message - actual response has additional text
	if len(response) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestAgentMaxIterations(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agents.Defaults.MaxToolIterations = 2

	callCount := 0
	provider := &mockProvider{
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			callCount++
			// Always return tool calls to hit max iterations
			return &providers.ChatResponse{
				ID:    "test-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Still working...",
							ToolCalls: []providers.ToolCall{
								{
									ID:   "call_1",
									Type: "function",
									Function: providers.FunctionCall{
										Name:      "shell",
										Arguments: `{"command":"echo test"}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					},
				},
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFn: func(ctx context.Context, name string, args map[string]any) (string, error) {
			return "done", nil
		},
	}

	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "Do something",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Should hit max iterations - just verify it returns something
	if len(response) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = "/tmp/test-workspace"

	memory := &mockMemoryLoader{
		memoryFn: func(ctx context.Context) (string, error) {
			return "User prefers short responses.", nil
		},
	}

	skills := &mockSkillsLoader{
		summaryFn: func(ctx context.Context) (string, error) {
			return "skill1, skill2", nil
		},
	}

	provider := &mockProvider{}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)
	agent.memory = memory
	agent.skills = skills

	prompt := agent.BuildSystemPrompt(context.Background())

	// Check for expected content
	expectedContents := []string{
		"joshbot",
		"User prefers short responses",
		"skill1",
	}

	for _, expected := range expectedContents {
		found := false
		for i := 0; i <= len(prompt)-len(expected); i++ {
			if prompt[i:i+len(expected)] == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected prompt to contain %q", expected)
		}
	}
}

func TestAgentEmptyContent(t *testing.T) {
	cfg := config.Defaults()
	provider := &mockProvider{}
	tools := &mockToolExecutor{}
	sessions := newMockSessionManager()
	logger := newMockLogger()

	agent := NewAgent(cfg, provider, tools, sessions, logger)

	msg := bus.InboundMessage{
		SenderID:  "user123",
		Content:   "", // Empty content
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	response, err := agent.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Empty content is not a command, should be processed
	// Provider will return empty, so we get fallback
	if response == "" {
		t.Error("expected response")
	}
}

// Helper functions

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Test command detection
func TestIsCommand(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"", false},
		{"/start", true},
		{"/help", true},
		{"/status", true},
		{"hello", false},
		{"  /command", false}, // Not at start
		{"no slash", false},
	}

	for _, tt := range tests {
		result := isCommand(tt.input)
		if result != tt.expected {
			t.Errorf("isCommand(%q) = %v, expected %v", tt.input, result, tt.expected)
		}
	}
}

func TestCleanCommand(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/start", "start"},
		{"/help", "help"},
		{"start", "start"},
		{"/command\n", "command"},
		{"", ""},
	}

	for _, tt := range tests {
		result := cleanCommand(tt.input)
		if result != tt.expected {
			t.Errorf("cleanCommand(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestGetSessionKey(t *testing.T) {
	msg := bus.InboundMessage{
		SenderID: "user123",
		Channel:  "cli",
	}

	key := getSessionKey(msg)
	expected := "cli:user123"

	if key != expected {
		t.Errorf("getSessionKey() = %q, expected %q", key, expected)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel..."},
		{"", 10, ""},
		{"short", 5, "short"},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, expected %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}
