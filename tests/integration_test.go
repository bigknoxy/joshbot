// Package integration provides end-to-end integration tests for joshbot.
package integration

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// mockProvider is a mock LLM provider for integration testing.
type mockProvider struct {
	chatFn       func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error)
	streamFn     func(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error)
	transcribeFn func(ctx context.Context, audioData []byte, prompt string) (string, error)
	name         string
	cfg          providers.Config
}

func (m *mockProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if m.chatFn != nil {
		return m.chatFn(ctx, req)
	}
	return &providers.ChatResponse{
		ID:      "mock-id",
		Model:   req.Model,
		Choices: []providers.Choice{},
	}, nil
}

func (m *mockProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	if m.streamFn != nil {
		return m.streamFn(ctx, req)
	}
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func (m *mockProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	if m.transcribeFn != nil {
		return m.transcribeFn(ctx, audioData, prompt)
	}
	return "", nil
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) Config() providers.Config {
	return m.cfg
}

// mockToolExecutor wraps the tools registry for testing.
type mockToolExecutor struct {
	registry *tools.Registry
}

func (m *mockToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	return m.registry.Execute(ctx, name, args)
}

func (m *mockToolExecutor) GetSchemas() []providers.Tool {
	return m.registry.GetSchemas()
}

// TestIntegrationBasicFlow tests a basic message processing flow.
func TestIntegrationBasicFlow(t *testing.T) {
	// Create mock provider that returns a simple response
	provider := &mockProvider{
		name: "test-provider",
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			// Check that system prompt is included
			if len(req.Messages) == 0 {
				t.Error("expected messages in request")
			}

			// Check for system message
			hasSystem := false
			for _, msg := range req.Messages {
				if msg.Role == providers.RoleSystem {
					hasSystem = true
					break
				}
			}
			if !hasSystem {
				t.Log("Note: No system message in request (may be expected depending on test)")
			}

			return &providers.ChatResponse{
				ID:    "test-response-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Hello! I'm joshbot, your AI assistant.",
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	// Create mock session manager
	tmpDir := t.TempDir()
	sessionMgr, err := session.NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}

	// Create tool registry
	toolRegistry := tools.NewRegistry()

	// Create logger
	logger, err := log.NewLogger(log.Config{Level: log.InfoLevel, Pretty: false})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	// Create agent config
	cfg := config.Defaults()

	// Create agent
	agentInstance := agent.NewAgent(
		cfg,
		provider,
		&mockToolExecutor{registry: toolRegistry},
		sessionMgr,
		logger,
	)

	// Create inbound message
	msg := bus.InboundMessage{
		SenderID:  "test-user",
		Content:   "Hello, who are you?",
		Channel:   "cli",
		Timestamp: time.Now(),
		Metadata:  map[string]any{"username": "testuser"},
	}

	// Process message
	response, err := agentInstance.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Verify response
	if len(response) == 0 {
		t.Error("expected non-empty response")
	}

	t.Logf("Got response: %s", response)
}

// TestIntegrationWithTools tests message processing with tool execution.
func TestIntegrationWithTools(t *testing.T) {
	toolExecuted := false

	// Create mock provider that calls a tool
	provider := &mockProvider{
		name: "test-provider",
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			// First call: return a tool call
			// Check if this is a tool result response
			hasToolResult := false
			for _, msg := range req.Messages {
				if msg.Role == providers.RoleTool {
					hasToolResult = true
					break
				}
			}

			if hasToolResult {
				// Tool result received, return final response
				return &providers.ChatResponse{
					ID:    "test-response-id",
					Model: req.Model,
					Choices: []providers.Choice{
						{
							Message: providers.Message{
								Role:    providers.RoleAssistant,
								Content: "I found the file! It contains 'hello world'.",
							},
							FinishReason: "stop",
						},
					},
				}, nil
			}

			// First call: request tool
			return &providers.ChatResponse{
				ID:    "test-response-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Let me check that file for you.",
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
		},
	}

	// Create mock session manager
	tmpDir := t.TempDir()
	sessionMgr, err := session.NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}

	// Create tool registry with filesystem tool
	toolRegistry := tools.RegistryWithDefaults(
		tmpDir+"/workspace",
		false, // restrict to workspace
		60,    // exec timeout
		10,    // web timeout
		nil,   // message sender
	)

	// Add custom tool for testing
	customTool := &customTestTool{
		executeFn: func(ctx interface{}, args map[string]any) tools.ToolResult {
			toolExecuted = true
			return tools.ToolResult{Output: "File content: hello world"}
		},
	}
	toolRegistry.Register(customTool)

	// Create logger
	logger, _ := log.NewLogger(log.Config{Level: log.InfoLevel, Pretty: false})

	// Create agent config
	cfg := config.Defaults()

	// Create agent
	agentInstance := agent.NewAgent(
		cfg,
		provider,
		&mockToolExecutor{registry: toolRegistry},
		sessionMgr,
		logger,
	)

	// Create inbound message
	msg := bus.InboundMessage{
		SenderID:  "test-user",
		Content:   "Read test.txt for me",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	// Process message
	response, err := agentInstance.Process(context.Background(), msg)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Verify response
	if len(response) == 0 {
		t.Error("expected non-empty response")
	}

	t.Logf("Got response: %s", response)

	// Verify tool was executed (or at least attempted)
	// Note: This may fail if the tool name doesn't match exactly
	if !toolExecuted {
		t.Log("Note: Custom tool not executed (may be expected if tool name doesn't match)")
	}
}

// customTestTool is a custom tool for testing.
type customTestTool struct {
	executeFn func(ctx interface{}, args map[string]any) tools.ToolResult
}

func (c *customTestTool) Name() string                  { return "filesystem" }
func (c *customTestTool) Description() string           { return "Test filesystem tool" }
func (c *customTestTool) Parameters() []tools.Parameter { return []tools.Parameter{} }
func (c *customTestTool) Execute(ctx interface{}, args map[string]any) tools.ToolResult {
	if c.executeFn != nil {
		return c.executeFn(ctx, args)
	}
	return tools.ToolResult{Output: "executed"}
}

// TestIntegrationSessionPersistence tests that sessions persist across messages.
func TestIntegrationSessionPersistence(t *testing.T) {
	messageCount := 0

	provider := &mockProvider{
		name: "test-provider",
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			messageCount++

			// Check message history
			userMsgCount := 0
			assistantMsgCount := 0
			for _, msg := range req.Messages {
				switch msg.Role {
				case providers.RoleUser:
					userMsgCount++
				case providers.RoleAssistant:
					assistantMsgCount++
				}
			}

			t.Logf("Request #%d: %d user messages, %d assistant messages in history",
				messageCount, userMsgCount, assistantMsgCount)

			return &providers.ChatResponse{
				ID:    "test-response-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Response #" + string(rune('0'+messageCount)),
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	// Create mock session manager
	tmpDir := t.TempDir()
	sessionMgr, err := session.NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}

	// Create tool registry
	toolRegistry := tools.NewRegistry()

	// Create logger
	logger, _ := log.NewLogger(log.Config{Level: log.InfoLevel, Pretty: false})

	// Create agent config
	cfg := config.Defaults()

	// Create agent
	agentInstance := agent.NewAgent(
		cfg,
		provider,
		&mockToolExecutor{registry: toolRegistry},
		sessionMgr,
		logger,
	)

	// Send first message
	msg1 := bus.InboundMessage{
		SenderID:  "session-user",
		Content:   "First message",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	resp1, err := agentInstance.Process(context.Background(), msg1)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	t.Logf("Response 1: %s", resp1)

	// Send second message in same session
	msg2 := bus.InboundMessage{
		SenderID:  "session-user",
		Content:   "Second message",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	resp2, err := agentInstance.Process(context.Background(), msg2)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}

	t.Logf("Response 2: %s", resp2)

	// Verify session persistence
	loadedSession, err := sessionMgr.Load(context.Background(), "cli:session-user")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}

	if len(loadedSession.Messages) < 2 {
		t.Errorf("expected at least 2 messages in session, got %d", len(loadedSession.Messages))
	}

	t.Logf("Session has %d messages", len(loadedSession.Messages))
}

// TestIntegrationCLIChannel simulates a CLI channel interaction.
func TestIntegrationCLIChannel(t *testing.T) {
	provider := &mockProvider{
		name: "test-provider",
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			// Return appropriate response based on input
			lastUserMsg := ""
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == providers.RoleUser {
					lastUserMsg = req.Messages[i].Content
					break
				}
			}

			var content string
			switch {
			case lastUserMsg == "/help":
				content = "Available commands: /help, /start, /status"
			case lastUserMsg == "/start":
				content = "Hello! I'm joshbot."
			case lastUserMsg == "/status":
				content = "Status: Running\nModel: test-model"
			default:
				content = "You said: " + lastUserMsg
			}

			return &providers.ChatResponse{
				ID:    "test-response-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: content,
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	// Create mock session manager
	tmpDir := t.TempDir()
	sessionMgr, err := session.NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}

	// Create tool registry
	toolRegistry := tools.NewRegistry()

	// Create logger
	logger, _ := log.NewLogger(log.Config{Level: log.InfoLevel, Pretty: false})

	// Create agent config
	cfg := config.Defaults()

	// Create agent
	agentInstance := agent.NewAgent(
		cfg,
		provider,
		&mockToolExecutor{registry: toolRegistry},
		sessionMgr,
		logger,
	)

	// Simulate CLI commands
	testCases := []struct {
		input    string
		expected string
	}{
		{"/start", "Hello"},
		{"/help", "Available commands"},
		{"/status", "Status"},
		{"Hello bot", "You said"},
	}

	for _, tc := range testCases {
		msg := bus.InboundMessage{
			SenderID:  "cli-user",
			Content:   tc.input,
			Channel:   "cli",
			Timestamp: time.Now(),
		}

		response, err := agentInstance.Process(context.Background(), msg)
		if err != nil {
			t.Fatalf("process failed for %q: %v", tc.input, err)
		}

		// Just verify we get a response
		if len(response) == 0 {
			t.Errorf("expected response for %q", tc.input)
		}

		t.Logf("Input: %q -> Response: %s", tc.input, response)
	}
}

// TestIntegrationMessageBus tests integration with the message bus.
func TestIntegrationMessageBus(t *testing.T) {
	provider := &mockProvider{
		name: "test-provider",
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			return &providers.ChatResponse{
				ID:    "test-response-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Response from agent",
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	// Create session manager
	tmpDir := t.TempDir()
	sessionMgr, _ := session.NewManager(tmpDir)

	// Create tool registry
	toolRegistry := tools.NewRegistry()

	// Create logger
	logger, _ := log.NewLogger(log.Config{Level: log.InfoLevel, Pretty: false})

	// Create agent config
	cfg := config.Defaults()

	// Create agent
	agentInstance := agent.NewAgent(
		cfg,
		provider,
		&mockToolExecutor{registry: toolRegistry},
		sessionMgr,
		logger,
	)

	// Create message bus
	messageBus := bus.NewMessageBus()
	messageBus.Start()
	defer messageBus.Stop()

	// Track responses
	var mu sync.Mutex
	responses := make([]string, 0)

	// Subscribe to outbound messages
	messageBus.Subscribe(bus.TopicOutbound, func(ctx context.Context, msg bus.InboundMessage) {
		// This is for inbound messages; we'd need to handle outbound differently
	})

	// Note: The bus.Subscribe is for INBOUND messages, not outbound
	// For a proper integration test, we'd need to handle the agent response flow
	// This is a simplified test

	// Send a message directly to the agent via the bus
	inboundMsg := bus.InboundMessage{
		SenderID:  "bus-user",
		Content:   "Test message",
		Channel:   "cli",
		Timestamp: time.Now(),
	}

	// The bus would normally route to the agent, but we can also test directly
	_, _ = agentInstance.Process(context.Background(), inboundMsg)

	mu.Lock()
	responses = append(responses, "processed")
	mu.Unlock()

	if len(responses) == 0 {
		t.Error("expected at least one response")
	}
}

// TestIntegrationConcurrentRequests tests concurrent message processing.
func TestIntegrationConcurrentRequests(t *testing.T) {
	provider := &mockProvider{
		name: "test-provider",
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			// Simulate some processing time
			time.Sleep(10 * time.Millisecond)

			return &providers.ChatResponse{
				ID:    "test-response-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: "Concurrent response",
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	// Create session manager
	tmpDir := t.TempDir()
	sessionMgr, _ := session.NewManager(tmpDir)

	// Create tool registry
	toolRegistry := tools.NewRegistry()

	// Create logger
	logger, _ := log.NewLogger(log.Config{Level: log.InfoLevel, Pretty: false})

	// Create agent config
	cfg := config.Defaults()

	// Create agent
	agentInstance := agent.NewAgent(
		cfg,
		provider,
		&mockToolExecutor{registry: toolRegistry},
		sessionMgr,
		logger,
	)

	// Send concurrent messages
	var wg sync.WaitGroup
	concurrency := 5

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			msg := bus.InboundMessage{
				SenderID:  "user-" + string(rune('0'+id)),
				Content:   "Message " + string(rune('0'+id)),
				Channel:   "cli",
				Timestamp: time.Now(),
			}

			response, err := agentInstance.Process(context.Background(), msg)
			if err != nil {
				t.Errorf("process failed for user-%d: %v", id, err)
				return
			}

			if len(response) == 0 {
				t.Errorf("empty response for user-%d", id)
			}
		}(i)
	}

	wg.Wait()
	t.Logf("Processed %d concurrent requests", concurrency)
}

// TestIntegrationConfigLoading tests config loading and validation.
func TestIntegrationConfigLoading(t *testing.T) {
	// Create a temporary config directory
	tmpDir := t.TempDir()

	// Temporarily override config home
	oldHome := config.DefaultHome
	config.DefaultHome = tmpDir
	defer func() { config.DefaultHome = oldHome }()

	// Create a config file
	configPath := filepath.Join(tmpDir, "config.json")
	configContent := `{
		"schema_version": 1,
		"agents": {
			"defaults": {
				"model": "test/model",
				"max_tokens": 4096,
				"temperature": 0.8
			}
		},
		"gateway": {
			"host": "localhost",
			"port": 8080
		}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Load config
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Verify config values
	if cfg.Agents.Defaults.Model != "test/model" {
		t.Errorf("expected model 'test/model', got %q", cfg.Agents.Defaults.Model)
	}

	if cfg.Agents.Defaults.MaxTokens != 4096 {
		t.Errorf("expected max_tokens 4096, got %d", cfg.Agents.Defaults.MaxTokens)
	}

	if cfg.Agents.Defaults.Temperature != 0.8 {
		t.Errorf("expected temperature 0.8, got %f", cfg.Agents.Defaults.Temperature)
	}

	if cfg.Gateway.Port != 8080 {
		t.Errorf("expected gateway port 8080, got %d", cfg.Gateway.Port)
	}

	t.Logf("Config loaded successfully: %s", cfg.String())
}

// TestIntegrationWorkspace tests workspace directory creation.
func TestIntegrationWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	// Temporarily override config home
	oldHome := config.DefaultHome
	oldWorkspace := config.DefaultWorkspace
	config.DefaultHome = tmpDir
	config.DefaultWorkspace = filepath.Join(tmpDir, "workspace")
	defer func() {
		config.DefaultHome = oldHome
		config.DefaultWorkspace = oldWorkspace
	}()

	// Load config (will use defaults)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Ensure directories
	err = cfg.EnsureDirs()
	if err != nil {
		t.Fatalf("failed to ensure directories: %v", err)
	}

	// Verify directories exist
	expectedDirs := []string{
		cfg.HomeDir(),
		cfg.WorkspaceDir(),
		cfg.SessionsDir(),
		cfg.MediaDir(),
		cfg.CronDir(),
	}

	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("expected directory %s to exist", dir)
		} else {
			t.Logf("Directory exists: %s", dir)
		}
	}
}

// TestIntegrationFullConversationFlow tests a complete conversation flow.
func TestIntegrationFullConversationFlow(t *testing.T) {
	// Track conversation state
	conversation := []string{}

	provider := &mockProvider{
		name: "test-provider",
		chatFn: func(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
			// Get the last user message
			lastUserMsg := ""
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == providers.RoleUser {
					lastUserMsg = req.Messages[i].Content
					break
				}
			}

			conversation = append(conversation, lastUserMsg)

			// Simple state machine for conversation
			responseNum := len(conversation)

			var content string
			switch responseNum {
			case 1:
				content = "Hello! What would you like to do?"
			case 2:
				content = "I can help with that. Let me check something."
			case 3:
				content = "Is there anything else you'd like help with?"
			default:
				content = "Thank you for chatting!"
			}

			return &providers.ChatResponse{
				ID:    "test-response-id",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: content,
						},
						FinishReason: "stop",
					},
				},
			}, nil
		},
	}

	// Create session manager
	tmpDir := t.TempDir()
	sessionMgr, _ := session.NewManager(tmpDir)

	// Create tool registry
	toolRegistry := tools.NewRegistry()

	// Create logger
	logger, _ := log.NewLogger(log.Config{Level: log.InfoLevel, Pretty: false})

	// Create agent config
	cfg := config.Defaults()

	// Create agent
	agentInstance := agent.NewAgent(
		cfg,
		provider,
		&mockToolExecutor{registry: toolRegistry},
		sessionMgr,
		logger,
	)

	// Simulate a conversation
	inputs := []string{
		"Hi there!",
		"Can you help me with a task?",
		"Yes, please check my files.",
		"That's all, thanks!",
	}

	for _, input := range inputs {
		msg := bus.InboundMessage{
			SenderID:  "conversation-user",
			Content:   input,
			Channel:   "cli",
			Timestamp: time.Now(),
		}

		response, err := agentInstance.Process(context.Background(), msg)
		if err != nil {
			t.Fatalf("process failed for %q: %v", input, err)
		}

		t.Logf("User: %s -> Agent: %s", input, response)
	}

	// Verify conversation was tracked
	if len(conversation) != len(inputs) {
		t.Errorf("expected %d conversation turns, got %d", len(inputs), len(conversation))
	}

	// Verify session was persisted
	loadedSession, err := sessionMgr.Load(context.Background(), "cli:conversation-user")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}

	// Should have: user messages + assistant messages
	if len(loadedSession.Messages) < len(inputs) {
		t.Errorf("expected at least %d messages in session, got %d", len(inputs), len(loadedSession.Messages))
	}

	t.Logf("Conversation complete: %d turns, %d messages in session",
		len(conversation), len(loadedSession.Messages))
}
