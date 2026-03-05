package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/tools"
)

const (
	// DefaultTimeout is the default timeout for agent operations.
	DefaultTimeout = 120 * time.Second
	// DefaultMaxIterations is the default max iterations for ReAct loop.
	DefaultMaxIterations = 20
)

// ToolExecutor is an interface for executing tool calls.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (string, error)
	ExecuteWithContext(ctx context.Context, name string, args map[string]any, channel, channelID string, callback func(tools.AsyncResult)) (tools.ToolResult, bool)
	GetSchemas() []providers.Tool
}

// SessionManager is an interface for managing sessions.
type SessionManager interface {
	GetOrCreate(ctx context.Context, key string) (*session.Session, error)
	Save(ctx context.Context, sess *session.Session) error
	Delete(ctx context.Context, key string) error
}

// MemoryLoader is an interface for loading memory content.
type MemoryLoader interface {
	LoadMemory(ctx context.Context) (string, error)
	LoadHistory(ctx context.Context, query string) (string, error)
}

// SkillsLoader is an interface for loading skills.
type SkillsLoader interface {
	LoadSummary(ctx context.Context) (string, error)
}

// HistoryAppender appends condensed high-signal turns to HISTORY.md.
type HistoryAppender interface {
	AppendHistory(ctx context.Context, entry string) error
}

// Agent represents the main agent that processes messages using ReAct loop.
type Agent struct {
	cfg           *config.Config
	provider      providers.Provider
	tools         ToolExecutor
	sessions      SessionManager
	memory        MemoryLoader
	history       HistoryAppender
	skills        SkillsLoader
	logger        *log.Logger
	budget        *ctxpkg.BudgetManager
	compressor    *ctxpkg.Compressor
	maxIterations int
	timeout       time.Duration
}

// Option is a functional option for configuring Agent.
type Option func(*Agent)

// WithMaxIterations sets the maximum number of ReAct iterations.
func WithMaxIterations(n int) Option {
	return func(a *Agent) {
		if n > 0 {
			a.maxIterations = n
		}
	}
}

// WithTimeout sets the processing timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(a *Agent) {
		if timeout > 0 {
			a.timeout = timeout
		}
	}
}

// NewAgent creates a new Agent with the given dependencies.
func NewAgent(
	cfg *config.Config,
	provider providers.Provider,
	tools ToolExecutor,
	sessions SessionManager,
	logger *log.Logger,
	opts ...Option,
) *Agent {
	if logger == nil {
		logger = log.Get()
	}

	a := &Agent{
		cfg:           cfg,
		provider:      provider,
		tools:         tools,
		sessions:      sessions,
		logger:        logger,
		maxIterations: cfg.Agents.Defaults.MaxToolIterations,
		timeout:       DefaultTimeout,
	}

	// Apply options
	for _, opt := range opts {
		opt(a)
	}

	// Ensure sensible defaults
	if a.maxIterations <= 0 {
		a.maxIterations = DefaultMaxIterations
	}

	return a
}

// WithMemoryLoader injects a memory loader implementation.
func WithMemoryLoader(loader MemoryLoader) Option {
	return func(a *Agent) {
		if loader != nil {
			a.memory = loader
		}
	}
}

// WithHistoryAppender injects a history appender implementation.
func WithHistoryAppender(appender HistoryAppender) Option {
	return func(a *Agent) {
		if appender != nil {
			a.history = appender
		}
	}
}

// WithSkillsLoader injects a skills loader implementation.
func WithSkillsLoader(loader SkillsLoader) Option {
	return func(a *Agent) {
		if loader != nil {
			a.skills = loader
		}
	}
}

// WithBudgetManager injects a BudgetManager for context budgeting.
func WithBudgetManager(budget *ctxpkg.BudgetManager) Option {
	return func(a *Agent) {
		if budget != nil {
			a.budget = budget
		}
	}
}

// WithCompressor injects a Compressor used to compact messages when needed.
func WithCompressor(c *ctxpkg.Compressor) Option {
	return func(a *Agent) {
		if c != nil {
			a.compressor = c
		}
	}
}

// Process handles an inbound message and returns the response content.
// It implements the full ReAct loop: receive message, call LLM, execute tools, repeat.
func (a *Agent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	startTime := time.Now()
	a.logger.Info("Processing message",
		"channel", msg.Channel,
		"sender", msg.SenderID,
		"content_len", len(msg.Content),
	)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	// Handle commands (pass context for session deletion)
	if isCommand(msg.Content) {
		response := a.handleCommand(ctx, msg)
		if response != "" {
			return response, nil
		}
	}

	// Get or create session
	sessionKey := getSessionKey(msg)
	sess, err := a.sessions.GetOrCreate(ctx, sessionKey)
	if err != nil {
		a.logger.Error("Failed to get session", "error", err)
		return fmt.Sprintf("Error: Failed to load session: %v", err), nil
	}

	startSessionLen := len(sess.Messages)

	// Build system prompt
	systemPrompt := a.BuildSystemPrompt(ctx)

	// Add user message to session
	userMsg := session.Message{
		Role:      session.RoleUser,
		Content:   msg.Content,
		Timestamp: time.Now(),
	}
	sess.AddMessage(userMsg)

	// Build messages for LLM (system + session messages)
	messages := a.buildMessages(systemPrompt, sess)

	// Run ReAct loop with channel info for async callbacks
	channelID := msg.SenderID // Use SenderID as the channel identifier
	responseContent, err := a.reactLoop(ctx, messages, sess, msg.Channel, channelID)
	if err != nil {
		a.logger.Error("ReAct loop error", "error", err)
		// Check for timeout
		if ctx.Err() == context.DeadlineExceeded {
			return "I'm sorry, but processing your request took too long. Please try again or simplify your request.", nil
		}
		return fmt.Sprintf("Error processing request: %v", err), nil
	}

	if a.history != nil {
		newMessages := sess.Messages[startSessionLen:]
		if shouldRecordSignificantTurn(newMessages, msg.Content, responseContent) {
			entry := formatHistoryEntry(msg.Content, responseContent, newMessages)
			if err := a.history.AppendHistory(ctx, entry); err != nil {
				a.logger.Warn("Failed to append history", "error", err)
			}
		}
	}

	// Save session
	if err := a.sessions.Save(ctx, sess); err != nil {
		a.logger.Warn("Failed to save session", "error", err)
	}

	elapsed := time.Since(startTime)
	a.logger.Info("Message processed",
		"elapsed", elapsed.Seconds(),
		"response_len", len(responseContent),
	)

	return responseContent, nil
}

// BuildSystemPrompt builds the system prompt with memory and skills context.
func (a *Agent) BuildSystemPrompt(ctx context.Context) string {
	return BuildPrompt(a.cfg.Agents.Defaults.Workspace, a.skills, a.memory, a.cfg.User.Name)
}

// reactLoop executes the ReAct loop: LLM -> tools -> reflect -> repeat.
func (a *Agent) reactLoop(ctx context.Context, messages []providers.Message, sess *session.Session, channel, channelID string) (string, error) {
	for iteration := 0; iteration < a.maxIterations; iteration++ {
		a.logger.Debug("ReAct iteration", "iteration", iteration+1, "max", a.maxIterations)

		// Get tool schemas if available
		var toolSchemas []providers.Tool
		if a.tools != nil {
			toolSchemas = a.tools.GetSchemas()
		}

		// Call LLM
		req := providers.ChatRequest{
			Model:       a.cfg.Agents.Defaults.Model,
			Messages:    messages,
			Temperature: a.cfg.Agents.Defaults.Temperature,
			MaxTokens:   a.cfg.Agents.Defaults.MaxTokens,
			Tools:       toolSchemas,
		}

		resp, err := a.provider.Chat(ctx, req)
		if err != nil {
			return "", fmt.Errorf("LLM call failed: %w", err)
		}

		// Check if we have a valid response
		if len(resp.Choices) == 0 {
			a.logger.Warn("Empty response from LLM")
			return "I didn't get a response. Please try again.", nil
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message

		// DEBUG: Log LLM response details
		a.logger.Debug("LLM response received",
			"content_length", len(assistantMsg.Content),
			"content_preview", truncate(assistantMsg.Content, 200),
			"tool_calls_count", len(assistantMsg.ToolCalls),
			"finish_reason", choice.FinishReason,
		)

		// If no tool calls, we're done
		if len(assistantMsg.ToolCalls) == 0 {
			content := assistantMsg.Content
			if content == "" {
				a.logger.Warn("Empty content from LLM - triggering fallback message",
					"model", a.cfg.Agents.Defaults.Model,
					"iteration", iteration+1,
				)
				content = "I've processed your request."
			}

			// Add assistant message to session
			sess.AddMessage(session.Message{
				Role:      session.RoleAssistant,
				Content:   content,
				Timestamp: time.Now(),
			})

			return content, nil
		}

		// Convert tool calls to session format
		toolCalls := make([]session.ToolCall, len(assistantMsg.ToolCalls))
		for i, tc := range assistantMsg.ToolCalls {
			toolCalls[i] = session.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			}
		}

		// Add assistant message with tool calls to session
		assistantSessionMsg := session.Message{
			Role:      session.RoleAssistant,
			Content:   assistantMsg.Content,
			ToolCalls: toolCalls,
			Timestamp: time.Now(),
		}
		sess.AddMessage(assistantSessionMsg)

		// Add to LLM messages
		messages = append(messages, assistantMsg)

		// Execute all tool calls
		for _, tc := range assistantMsg.ToolCalls {
			a.logger.Info("Executing tool",
				"name", tc.Function.Name,
				"args", truncate(tc.Function.Arguments, 100),
			)

			// Parse arguments
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				a.logger.Warn("Failed to parse tool arguments",
					"error", err,
					"args", tc.Function.Arguments,
				)
				result := fmt.Sprintf("Error: Invalid arguments: %v", err)
				toolMsg := a.formatToolResult(tc.ID, tc.Function.Name, result)
				messages = append(messages, toolMsg)
				sess.AddMessage(session.Message{
					Role:       session.RoleTool,
					Content:    result,
					ToolCallID: tc.ID,
					Timestamp:  time.Now(),
				})
				continue
			}

			// Execute tool
			result, isAsync := a.tools.ExecuteWithContext(ctx, tc.Function.Name, args, channel, channelID, nil)
			var resultStr string
			if result.Error != nil {
				a.logger.Error("Tool execution failed",
					"tool", tc.Function.Name,
					"error", result.Error,
				)
				resultStr = fmt.Sprintf("Error executing %s: %v", tc.Function.Name, result.Error)
			} else {
				resultStr = result.Output
			}

			// For async tools, add placeholder message
			if isAsync {
				resultStr = result.Output // Contains "Started in background..." message
			}

			// Format tool result for LLM
			toolMsg := a.formatToolResult(tc.ID, tc.Function.Name, resultStr)
			messages = append(messages, toolMsg)

			// Add to session
			sess.AddMessage(session.Message{
				Role:       session.RoleTool,
				Content:    resultStr,
				ToolCallID: tc.ID,
				Timestamp:  time.Now(),
			})

			// DEBUG: Log tool result
			a.logger.Debug("Tool result",
				"tool", tc.Function.Name,
				"result_length", len(resultStr),
				"result_preview", truncate(resultStr, 200),
				"is_async", isAsync,
			)
		}

		// Proactive context compaction: check if we need to compact after tool execution
		messages = a.checkAndCompactContext(messages, sess)
	}

	// Hit max iterations
	a.logger.Warn("Hit max iterations", "max", a.maxIterations)
	return "I've been working on this for a while. Here's what I found so far - let me know if you'd like me to continue.", nil
}

// checkAndCompactContext estimates current message tokens and compacts context if threshold is exceeded.
// It returns the original messages if under threshold, or compacted messages otherwise.
func (a *Agent) checkAndCompactContext(messages []providers.Message, sess *session.Session) []providers.Message {
	// Only proceed if we have budget manager and compressor
	if a.budget == nil || a.compressor == nil {
		return messages
	}

	threshold := a.cfg.Agents.Defaults.CompactionThreshold
	if threshold <= 0 || threshold > 1.0 {
		threshold = 0.7 // default fallback
	}

	model := a.cfg.Agents.Defaults.Model
	maxCompletion := a.cfg.Agents.Defaults.MaxTokens
	budget := a.budget.ComputeBudget(model, maxCompletion)
	thresholdBudget := int(float64(budget) * threshold)

	// Estimate tokens for all messages (excluding system message at index 0)
	totalTokens := 0
	for i := 1; i < len(messages); i++ {
		totalTokens += ctxpkg.TokenEstimator(messages[i].Content)
	}

	a.logger.Debug("Context compaction check",
		"total_tokens", totalTokens,
		"threshold_budget", thresholdBudget,
		"full_budget", budget,
		"threshold", threshold,
	)

	// If under threshold, no compaction needed
	if totalTokens <= thresholdBudget {
		return messages
	}

	// Threshold exceeded - compact messages
	a.logger.Info("Compacting context", "total_tokens", totalTokens, "threshold_budget", thresholdBudget)

	// Get session messages for compression (excluding system prompt)
	sessionMsgs := messages[1:] // Skip system message
	compressed, err := a.compressor.CompressMessages(model, sessionMsgs, thresholdBudget)
	if err != nil {
		a.logger.Warn("Context compaction failed", "error", err)
		return messages
	}

	// Return new message list with compressed content
	newMessages := []providers.Message{
		messages[0], // Keep system message
		{
			Role:    providers.RoleUser,
			Content: "<conversation_summary>\n" + compressed,
		},
	}

	a.logger.Debug("Context compacted", "original_messages", len(sessionMsgs), "new_content_len", len(compressed))
	return newMessages
}

// buildMessages builds the message list for LLM from session and system prompt.
func (a *Agent) buildMessages(systemPrompt string, sess *session.Session) []providers.Message {
	msgs := []providers.Message{
		{
			Role:    providers.RoleSystem,
			Content: systemPrompt,
		},
	}

	// Convert session messages to provider messages
	providerMsgs := make([]providers.Message, 0, len(sess.Messages))
	for _, msg := range sess.Messages {
		providerMsg := providers.Message{
			Role:       providers.MessageRole(msg.Role),
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}

		// Convert tool calls
		if len(msg.ToolCalls) > 0 {
			providerToolCalls := make([]providers.ToolCall, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				providerToolCalls[i] = providers.ToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: providers.FunctionCall{
						Name:      tc.Name,
						Arguments: string(tc.Arguments),
					},
				}
			}
			providerMsg.ToolCalls = providerToolCalls
		}

		providerMsgs = append(providerMsgs, providerMsg)
	}

	window := a.cfg.Agents.Defaults.MemoryWindow
	if window > 0 && len(providerMsgs) > window {
		providerMsgs = providerMsgs[len(providerMsgs)-window:]
	}

	// If we have a budget manager and compressor, consider compressing older messages
	if a.budget != nil && a.compressor != nil {
		model := a.cfg.Agents.Defaults.Model
		maxCompletion := a.cfg.Agents.Defaults.MaxTokens
		budget := a.budget.ComputeBudget(model, maxCompletion)
		budget -= ctxpkg.TokenEstimator(systemPrompt)
		if budget < 256 {
			budget = 256
		}

		a.logger.Debug("Context budgeting",
			"model", model,
			"history_messages", len(providerMsgs),
			"system_tokens", ctxpkg.TokenEstimator(systemPrompt),
			"budget_tokens", budget,
		)

		// Estimate total tokens for providerMsgs
		totalTokens := 0
		for _, m := range providerMsgs {
			totalTokens += ctxpkg.TokenEstimator(m.Content)
		}

		if totalTokens > budget {
			a.logger.Debug("Compressing context", "estimated_tokens", totalTokens, "budget_tokens", budget)
			// Compress messages to fit within budget
			compressed, err := a.compressor.CompressMessages(model, providerMsgs, budget)
			if err == nil && compressed != "" {
				// Append a single summarized user message instead of full history
				msgs = append(msgs, providers.Message{Role: providers.RoleUser, Content: "<conversation_summary>\n" + compressed})
				return msgs
			}
			// on error, fallthrough and append full messages (best-effort)
		}
	}

	// No compression required or compressor not available: append all messages
	for _, pm := range providerMsgs {
		msgs = append(msgs, pm)
	}

	return msgs
}

func shouldRecordSignificantTurn(newMessages []session.Message, userContent, assistantContent string) bool {
	if strings.TrimSpace(userContent) == "" || strings.TrimSpace(assistantContent) == "" {
		return false
	}

	for _, m := range newMessages {
		if m.Role == session.RoleTool {
			return true
		}
	}

	if len(userContent) > 220 || len(assistantContent) > 320 {
		return true
	}

	signals := []string{"important", "decision", "decided", "preference", "prefer", "always", "never", "remember"}
	text := strings.ToLower(userContent + " " + assistantContent)
	for _, signal := range signals {
		if strings.Contains(text, signal) {
			return true
		}
	}

	return false
}

func formatHistoryEntry(userContent, assistantContent string, newMessages []session.Message) string {
	toolCalls := 0
	for _, m := range newMessages {
		if m.Role == session.RoleTool {
			toolCalls++
		}
	}

	entry := fmt.Sprintf("User: %s | Assistant: %s", compactSnippet(userContent, 180), compactSnippet(assistantContent, 220))
	if toolCalls > 0 {
		entry += fmt.Sprintf(" | Tools used: %d", toolCalls)
	}
	return entry
}

func compactSnippet(s string, maxLen int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// formatToolResult formats a tool result as a message for the LLM.
func (a *Agent) formatToolResult(toolCallID, name, result string) providers.Message {
	return providers.Message{
		Role:       providers.RoleTool,
		Content:    result,
		Name:       name,
		ToolCallID: toolCallID,
	}
}

// handleCommand handles slash commands.
func (a *Agent) handleCommand(ctx context.Context, msg bus.InboundMessage) string {
	cmd := cleanCommand(msg.Content)

	switch cmd {
	case "start":
		return "Hello! I'm joshbot, your personal AI assistant. How can I help you today?"
	case "new":
		// Delete the session to start fresh
		sessionKey := getSessionKey(msg)
		if err := a.sessions.Delete(ctx, sessionKey); err != nil {
			// Log the error but don't fail - session might not exist
			a.logger.Debug("Could not delete session for /new", "session", sessionKey, "error", err)
		}
		return "🔄 Started a new conversation! All previous context has been cleared."
	case "help":
		return `Available commands:
/start - Start a conversation
/new - Start fresh (saves memory first)
/help - Show this help
/status - Show system status

Just type normally to chat with me!`
	case "status":
		toolCount := 0
		if a.tools != nil {
			toolCount = len(a.tools.GetSchemas())
		}
		return fmt.Sprintf(`Status:
  Model: %s
  Tools: %d registered
  Memory window: %d
  Max iterations: %d`,
			a.cfg.Agents.Defaults.Model,
			toolCount,
			a.cfg.Agents.Defaults.MemoryWindow,
			a.cfg.Agents.Defaults.MaxToolIterations,
		)
	}

	return "" // Not a known command, process normally
}

// isCommand checks if the message content is a command.
func isCommand(content string) bool {
	return len(content) > 0 && content[0] == '/'
}

// cleanCommand cleans a command string.
func cleanCommand(content string) string {
	if len(content) > 0 && content[0] == '/' {
		content = content[1:]
	}
	// Remove extra whitespace
	for len(content) > 0 && (content[len(content)-1] == ' ' || content[len(content)-1] == '\n') {
		content = content[:len(content)-1]
	}
	return content
}

// getSessionKey generates a session key from the message.
func getSessionKey(msg bus.InboundMessage) string {
	// Use channel + sender ID as session key
	return fmt.Sprintf("%s:%s", msg.Channel, msg.SenderID)
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
