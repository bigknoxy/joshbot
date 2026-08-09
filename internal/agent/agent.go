package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/skills"
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

// ContextCompressor summarizes a run of messages into a bounded string.
//
// The agent depends on the behaviour, not on *ctxpkg.Compressor, so a test can
// substitute a double and count how often compression actually runs — which is
// the property issue #125 is about.
type ContextCompressor interface {
	CompressMessages(ctx context.Context, model string, messages []providers.Message, budget int) (string, error)
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
	compressor    ContextCompressor
	maxIterations int
	timeout       time.Duration
	skillDetector *skills.SkillDetector
	extractor     *skills.Extractor
	skillLoader   *skills.Loader
	modelName     string
}

func (a *Agent) getModelName() string {
	if a.modelName != "" {
		return a.modelName
	}
	if a.cfg.UseModelsConfig() {
		return a.cfg.ModelsConfig.Agent.Model
	}
	return a.cfg.Agents.Defaults.Model
}

// getResolvedModelName returns the actual model string for context budgeting.
// In model-centric mode, resolves "smart" -> "nvidia/stepfun-ai/step-3.5-flash".
func (a *Agent) getResolvedModelName() string {
	if a.modelName != "" {
		return a.modelName
	}
	if !a.cfg.UseModelsConfig() {
		return a.cfg.Agents.Defaults.Model
	}
	if modelConfig, err := a.cfg.GetActiveModel(); err == nil {
		return modelConfig.Model
	}
	return a.cfg.ModelsConfig.Agent.Model
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
		// Guard against a typed nil: assigning one would leave a.compressor
		// non-nil while every call panics.
		if c != nil {
			a.compressor = c
		}
	}
}

// WithContextCompressor injects any ContextCompressor implementation.
func WithContextCompressor(c ContextCompressor) Option {
	return func(a *Agent) {
		if c != nil {
			a.compressor = c
		}
	}
}

// WithSkillDetector injects a SkillDetector for automatic skill discovery.
func WithSkillDetector(d *skills.SkillDetector) Option {
	return func(a *Agent) {
		if d != nil {
			a.skillDetector = d
		}
	}
}

// WithExtractor injects a skill Extractor for LLM-based skill creation.
func WithExtractor(e *skills.Extractor) Option {
	return func(a *Agent) {
		if e != nil {
			a.extractor = e
		}
	}
}

// WithSkillLoader injects a concrete skills.Loader (needed for Create/Delete/List).
func WithSkillLoader(l *skills.Loader) Option {
	return func(a *Agent) {
		if l != nil {
			a.skillLoader = l
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

	// Auto-extract user facts from message (name, org, role) into persistent ConversationContext
	sess.ExtractUserFacts(msg.Content)

	// Derive topic from user message if conversation is just starting
	if sess.ConversationTopic == "" && len(msg.Content) > 5 {
		topic := inferTopic(msg.Content)
		if topic != "" {
			sess.ConversationTopic = topic
		}
	}

	// Build system prompt with conversation context
	systemPrompt := a.BuildSystemPrompt(ctx)
	if summary := sess.ConversationSummary(); summary != "" {
		systemPrompt += "\n\n<conversation_context>\n" + summary + "\n</conversation_context>"
	}

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
	var compaction compactionState
	responseContent, err := a.reactLoop(ctx, messages, sess, msg.Channel, channelID, msg.Content, &compaction)
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

	// Fold in any compaction produced during the turn. This runs after the
	// history append above, which slices sess.Messages from startSessionLen —
	// shrinking the session before that point would slice out of range.
	a.applyCompaction(ctx, sess, compaction)

	// Save session
	if err := a.sessions.Save(ctx, sess); err != nil {
		a.logger.Warn("Failed to save session", "error", err)
	}

	// Update conversation topic based on what happened
	if !isCommand(msg.Content) && responseContent != "" {
		updatedTopic := updateTopic(sess.ConversationTopic, msg.Content, responseContent)
		if updatedTopic != "" {
			sess.ConversationTopic = updatedTopic
			if err := a.sessions.Save(ctx, sess); err != nil {
				a.logger.Warn("Failed to save updated topic", "error", err)
			}
		}
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
func (a *Agent) reactLoop(ctx context.Context, messages []providers.Message, sess *session.Session, channel, channelID, userMessage string, st *compactionState) (string, error) {
	var toolRecords []skills.ToolCallRecord
	for iteration := 0; iteration < a.maxIterations; iteration++ {
		a.logger.Debug("ReAct iteration", "iteration", iteration+1, "max", a.maxIterations)

		// Get tool schemas if available
		var toolSchemas []providers.Tool
		if a.tools != nil {
			toolSchemas = a.tools.GetSchemas()
		}

		// Call LLM
		req := providers.ChatRequest{
			Model:       a.getModelName(),
			Messages:    messages,
			Temperature: a.cfg.Agents.Defaults.Temperature,
			MaxTokens:   a.cfg.Agents.Defaults.MaxTokens,
			Tools:       toolSchemas,
		}

		// When streaming is enabled and a stream sink is attached to the
		// request context, use ChatStream and forward text deltas to the
		// sink as they arrive. Otherwise, use the non-streaming Chat path.
		sink := streamSinkFromContext(ctx)
		streaming := a.cfg.Agents.Defaults.Streaming && sink != nil

		var resp *providers.ChatResponse
		var err error
		if streaming {
			resp, err = a.streamChat(ctx, req, sink)
		} else {
			resp, err = a.provider.Chat(ctx, req)
		}
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
					"model", a.getModelName(),
					"iteration", iteration+1,
				)
				content = "I've processed your request."
			}

			// Sanitize: strip internal context tags from response
			content = sanitizeResponse(content)

			// Add assistant message to session
			sess.AddMessage(session.Message{
				Role:      session.RoleAssistant,
				Content:   content,
				Timestamp: time.Now(),
			})

			// Skill detection: record trace and check for candidates
			a.afterReActDetection(content, toolRecords, sess.ID, userMessage)

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
			progress := progressFromContext(ctx)
			if progress != nil {
				progress(ToolProgressEvent{
					Tool:    tc.Function.Name,
					Summary: summarizeToolArgs(tc.Function.Name, args),
					Phase:   ToolProgressStart,
				})
			}
			toolStart := time.Now()
			result, isAsync := a.tools.ExecuteWithContext(ctx, tc.Function.Name, args, channel, channelID, nil)
			if progress != nil {
				progress(ToolProgressEvent{
					Tool:    tc.Function.Name,
					Summary: summarizeToolArgs(tc.Function.Name, args),
					Phase:   ToolProgressDone,
					Elapsed: time.Since(toolStart),
					Err:     result.Error,
				})
			}
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

			// Record tool call for skill detection (only synchronous, real tool calls)
			if a.skillDetector != nil {
				toolRecords = append(toolRecords, skills.ToolCallRecord{
					Tool:   tc.Function.Name,
					Args:   args,
					Result: truncate(resultStr, 200),
				})
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
		messages = a.checkAndCompactContext(ctx, messages, sess, st)
	}

	// Hit max iterations
	a.logger.Warn("Hit max iterations", "max", a.maxIterations)
	return "I've been working on this for a while. Here's what I found so far - let me know if you'd like me to continue.", nil
}

// streamErrorMarker renders a mid-stream failure as text the user can see.
//
// A leading newline only when text preceded it, so a stream that failed before
// producing anything does not start the reply with a blank line.
func streamErrorMarker(err error, hadText bool) string {
	if hadText {
		return "\n[stream error: " + err.Error() + "]"
	}
	return "[stream error: " + err.Error() + "]"
}

// drainStream consumes the remainder of a provider stream that is being
// abandoned, so the goroutine feeding it can finish and release its
// connection.
func drainStream(stream <-chan providers.StreamChunk) {
	for range stream {
	}
}

// streamChat sends a streaming chat request, forwards text deltas to the
// sink as they arrive, and accumulates the full response using the stage-2
// ChunkAccumulator. On mid-stream failure, a visible error marker is
// appended to whatever text was already shown — never silently truncated.
//
// The returned *ChatResponse has the same shape as the non-streaming Chat
// path, so everything downstream of the call site is unchanged.
func (a *Agent) streamChat(ctx context.Context, req providers.ChatRequest, sink StreamSink) (*providers.ChatResponse, error) {
	stream, err := a.provider.ChatStream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("stream failed to open: %w", err)
	}

	acc := providers.NewChunkAccumulator()
	var accumulatedContent string

	for chunk := range stream {
		if err := acc.Accumulate(chunk); err != nil {
			// Stream error mid-flight — append a visible marker to what
			// was already shown. Return partial content, not an error,
			// so the caller sees the text that was already delivered.
			//
			// The marker is emitted even when nothing arrived first. A stream
			// that dies before any text is the common case, and suppressing
			// the marker there left an empty response that reactLoop replaced
			// with "I've processed your request." — a confident non-answer
			// standing in for a failure.
			marker := streamErrorMarker(err, accumulatedContent != "")
			sink(StreamEvent{Delta: marker, Done: true})
			accumulatedContent += marker

			// Drain the rest of the stream. The provider goroutine blocks
			// sending into this channel and does not close the response body
			// until it can, so abandoning it would hold the goroutine and its
			// connection until the request context expires.
			//
			// No test reaches this: ChunkAccumulator.Accumulate has no failing
			// path today, and every truncation is reported by Result() below,
			// after the range has drained the channel itself. It guards the
			// contract, not a live bug — the moment Accumulate can fail, an
			// early return here becomes a leak.
			drainStream(stream)

			return &providers.ChatResponse{
				ID:    "stream-error",
				Model: req.Model,
				Choices: []providers.Choice{
					{
						Message: providers.Message{
							Role:    providers.RoleAssistant,
							Content: accumulatedContent,
						},
					},
				},
			}, nil
		}

		// Forward text deltas to the sink as they arrive.
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				accumulatedContent += choice.Delta.Content
				sink(StreamEvent{Delta: choice.Delta.Content})
			}
		}
	}

	resp, err := acc.Result()
	if err != nil {
		// Stream ended with a truncation error — append a visible marker.
		marker := streamErrorMarker(err, accumulatedContent != "")
		sink(StreamEvent{Delta: marker, Done: true})
		accumulatedContent += marker
		return &providers.ChatResponse{
			ID:    "stream-error",
			Model: req.Model,
			Choices: []providers.Choice{
				{
					Message: providers.Message{
						Role:    providers.RoleAssistant,
						Content: accumulatedContent,
					},
				},
			},
		}, nil
	}

	// Signal completion to the sink.
	sink(StreamEvent{Done: true})

	return resp, nil
}

// afterReActDetection records the trace and, if a strong-enough candidate is
// found, asks the extractor to generate a SKILL.md and persists it via the loader.
func (a *Agent) afterReActDetection(finalOutput string, toolRecords []skills.ToolCallRecord, sessionID, userMessage string) {
	if a.skillDetector == nil || len(toolRecords) == 0 {
		return
	}

	trace := skills.Trace{
		UserMessage: userMessage,
		ToolCalls:   toolRecords,
		FinalOutput: finalOutput,
	}

	a.skillDetector.RecordTrace(sessionID, trace)

	candidate := a.skillDetector.Detect(trace)
	if candidate == nil {
		a.logger.Debug("No skill candidate detected", "tool_calls", len(toolRecords))
		return
	}

	a.logger.Info("Skill candidate detected",
		"name", candidate.Name,
		"confidence", candidate.Confidence,
	)

	if a.extractor == nil || a.skillLoader == nil {
		return
	}

	// Ask LLM to generate SKILL.md content from the trace
	existingSkills := a.skillLoader.List()
	skillContent, err := a.extractor.Extract(context.Background(), trace, existingSkills)
	if err != nil {
		a.logger.Warn("Skill extraction failed", "error", err)
		return
	}

	if skillContent == "" {
		a.logger.Warn("Skill extraction returned empty content")
		return
	}

	// Create the skill
	if err := a.skillLoader.Create(candidate.Name, skillContent); err != nil {
		a.logger.Warn("Failed to create skill", "name", candidate.Name, "error", err)
		return
	}

	a.logger.Info("Skill created successfully", "name", candidate.Name)
}

// compactionState carries a compaction produced during a turn so it can be
// applied to the session exactly once, after the turn finishes.
//
// It is a local of Process, never a field on Agent: two Telegram messages are
// processed concurrently, and a shared field would let one conversation's
// summary overwrite another's. This is the same reasoning that moved the
// progress callback onto the context.
//
// Applying at the end rather than in the loop also keeps `startSessionLen` in
// Process valid. That index slices sess.Messages to find the turn's new
// messages for the history log; shrinking the session underneath it would slice
// out of range.
type compactionState struct {
	// summary is the compressed text produced by the compressor.
	summary string
	// prefixLen is len(sess.Messages) at the moment the summary was taken, so
	// the summary stands in for exactly sess.Messages[:prefixLen].
	prefixLen int
	// active reports whether a compaction happened at all this turn.
	active bool
}

// applyCompaction folds a turn's compaction into the session, replacing the
// summarized prefix with a single compaction record.
//
// This is what makes compaction stick. Before it existed the summary lived only
// in the provider-facing slice and was discarded when Process returned, so
// buildMessages rebuilt the full history from sess.Messages on the next turn
// and the compressor ran again — an extra provider round-trip on every turn
// past the threshold, forever, while the session file kept growing (#125).
func (a *Agent) applyCompaction(ctx context.Context, sess *session.Session, st compactionState) {
	if !st.active || st.summary == "" {
		return
	}
	if st.prefixLen <= 0 || st.prefixLen > len(sess.Messages) {
		// The session changed shape in a way we did not predict. Dropping the
		// compaction costs a recomputation next turn; applying it against a
		// stale index could discard live messages.
		a.logger.Warn("Skipping compaction write-back: prefix out of range",
			"prefix_len", st.prefixLen, "session_len", len(sess.Messages))
		return
	}

	summarized := sess.Messages[:st.prefixLen]

	// Preserve the messages the summary replaces. The user asked for a smaller
	// context window, not for their conversation to be deleted. If the store
	// cannot archive them, keep the full session: recomputing a summary is
	// cheap compared with destroying history.
	if archiver, ok := a.sessions.(session.Archiver); ok {
		archived := make([]session.Message, len(summarized))
		copy(archived, summarized)
		if err := archiver.Archive(ctx, sess.ID, archived); err != nil {
			a.logger.Warn("Skipping compaction write-back: archive failed", "error", err)
			return
		}
	}

	tail := make([]session.Message, 0, len(sess.Messages)-st.prefixLen+1)
	tail = append(tail, session.NewCompactionRecord(st.summary))
	tail = append(tail, sess.Messages[st.prefixLen:]...)
	sess.Messages = tail

	a.logger.Info("Context compaction persisted",
		"summarized_messages", len(summarized),
		"session_messages", len(sess.Messages),
	)
}

// checkAndCompactContext estimates current message tokens and compacts context if threshold is exceeded.
// It returns the original messages if under threshold, or compacted messages otherwise.
//
// On success it also records the summary in st so Process can persist it once
// the turn completes; without that the work is repeated on every later turn.
func (a *Agent) checkAndCompactContext(ctx context.Context, messages []providers.Message, sess *session.Session, st *compactionState) []providers.Message {
	// Only proceed if we have budget manager and compressor
	if a.budget == nil || a.compressor == nil {
		return messages
	}

	threshold := a.cfg.Agents.Defaults.CompactionThreshold
	if threshold <= 0 || threshold > 1.0 {
		threshold = 0.7 // default fallback
	}

	model := a.getResolvedModelName()
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

	// Compress the stored session, not the provider slice.
	//
	// `messages` has already been through the memory-window truncation in
	// buildMessages, so it is only the tail of the conversation. The write-back
	// discards sess.Messages[:prefixLen] and puts the summary in its place, so
	// summarizing the tail while claiming the whole prefix would destroy every
	// message the window had already slid past. The two must describe the same
	// set of messages.
	sessionMsgs := messages[1:] // Skip system message
	prefixLen := 0
	if sess != nil && len(sess.Messages) > 0 {
		sessionMsgs = sessionToProviderMessages(sess)
		prefixLen = len(sess.Messages)
	}
	compressed, err := a.compressor.CompressMessages(ctx, model, sessionMsgs, thresholdBudget)
	if err != nil {
		a.logger.Warn("Context compaction failed", "error", err)
		return messages
	}

	// Return new message list with compressed content
	newMessages := []providers.Message{
		messages[0], // Keep system message
		{
			Role:    providers.RoleUser,
			Content: session.CompactionEnvelope(compressed),
		},
	}

	// Record it for the write-back at the end of the turn. A later compaction in
	// the same turn overwrites this: its summary already subsumes the earlier
	// one (the compressed text is carried forward in `messages`), and its
	// prefixLen covers strictly more of the session.
	if st != nil && prefixLen > 0 {
		st.summary = compressed
		st.prefixLen = prefixLen
		st.active = true
	}

	a.logger.Debug("Context compacted", "original_messages", len(sessionMsgs), "new_content_len", len(compressed))
	return newMessages
}

// sessionToProviderMessages converts a session's stored messages to the
// provider wire shape, in order and without truncation.
func sessionToProviderMessages(sess *session.Session) []providers.Message {
	providerMsgs := make([]providers.Message, 0, len(sess.Messages))
	for _, msg := range sess.Messages {
		providerMsg := providers.Message{
			Role:       providers.MessageRole(msg.Role),
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}

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
	return providerMsgs
}

// buildMessages builds the message list for LLM from session and system prompt.
func (a *Agent) buildMessages(systemPrompt string, sess *session.Session) []providers.Message {
	msgs := []providers.Message{
		{
			Role:    providers.RoleSystem,
			Content: systemPrompt,
		},
	}

	providerMsgs := sessionToProviderMessages(sess)

	// A stored compaction record stands in for everything that came before it,
	// so it is held out of the memory-window truncation below. Sliding the
	// window over it would drop the summary and silently lose the whole earlier
	// conversation — the opposite of what compaction is for.
	var compactionMsg []providers.Message
	if _, ok := sess.CompactionRecord(); ok && len(providerMsgs) > 0 {
		compactionMsg = providerMsgs[:1]
		providerMsgs = providerMsgs[1:]
	}

	window := a.cfg.Agents.Defaults.MemoryWindow
	if window > 0 && len(providerMsgs) > window {
		providerMsgs = providerMsgs[len(providerMsgs)-window:]
		providerMsgs = dropOrphanedToolResults(providerMsgs)
	}

	if len(compactionMsg) > 0 {
		// The record replaced a run of messages, so whatever now leads the tail
		// may be a tool result whose announcing assistant message is gone. An
		// OpenAI-compatible provider rejects that with a 400.
		providerMsgs = append(compactionMsg, dropOrphanedToolResults(providerMsgs)...)
	}

	// If we have a budget manager and compressor, consider compressing older messages
	if a.budget != nil && a.compressor != nil {
		model := a.getResolvedModelName()
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
			// Observation masking: keep last 3 exchanges verbatim, strip tool result content from older ones
			// but preserve the message structure so the LLM sees the conversation flow
			masked := a.applyObservationMasking(providerMsgs, budget)
			for _, pm := range masked {
				msgs = append(msgs, pm)
			}
			return msgs
		}
	}

	// No compression required or compressor not available: append all messages
	for _, pm := range providerMsgs {
		msgs = append(msgs, pm)
	}

	return msgs
}

// dropOrphanedToolResults removes tool messages at the head of a truncated
// history whose announcing assistant message was cut away. Sending a tool
// result that answers nothing makes an OpenAI-compatible provider reject the
// whole request with a 400.
func dropOrphanedToolResults(messages []providers.Message) []providers.Message {
	start := 0
	for start < len(messages) && messages[start].Role == providers.RoleTool {
		start++
	}
	return messages[start:]
}

// applyObservationMasking reduces context by stripping tool result content from older messages
// while keeping the last 3 exchanges (user+assistant pairs) fully intact.
// Tool outputs are replaced with "[Tool output truncated]" to save tokens.
func (a *Agent) applyObservationMasking(messages []providers.Message, budget int) []providers.Message {
	// Count total tokens in all messages
	totalTokens := 0
	for _, m := range messages {
		totalTokens += ctxpkg.TokenEstimator(m.Content)
	}

	if totalTokens <= budget {
		return messages
	}

	// Keep last 3 exchanges (user+assistant pairs) intact
	// Walk backwards and find roughly 6 messages (3 user+assistant turns)
	keepVerbatim := 6
	if keepVerbatim > len(messages) {
		keepVerbatim = len(messages)
	}

	result := make([]providers.Message, len(messages))
	// Start from the end, copy intact
	verbatimStart := len(messages) - keepVerbatim
	for i := verbatimStart; i < len(messages); i++ {
		result[i] = messages[i]
	}

	// Mask tool result content in older messages. Only the content is
	// replaced: ToolCalls and ToolCallID must survive, because an
	// OpenAI-compatible provider rejects a tool message with no tool_call_id,
	// or an announced tool call with no answering result, with a 400.
	for i := 0; i < verbatimStart; i++ {
		m := messages[i]
		if m.Role == providers.RoleTool || m.Role == providers.RoleAssistant {
			masked := m
			masked.Content = truncateSummary(m.Content)
			result[i] = masked
		} else {
			result[i] = m
		}
	}

	return result
}

// truncateSummary truncates tool output to a short summary to save tokens.
func truncateSummary(content string) string {
	if len(content) < 200 {
		return content
	}
	return content[:80] + "\n[Tool output truncated]\n" + content[len(content)-80:]
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
		// Clear messages but preserve conversation context (user facts survive /new)
		sessionKey := getSessionKey(msg)
		sess, err := a.sessions.GetOrCreate(ctx, sessionKey)
		if err == nil {
			sess.ClearMessages()
			if err := a.sessions.Save(ctx, sess); err != nil {
				a.logger.Debug("Could not save session after /new", "error", err)
			}
		}
		toolCount := 0
		if a.tools != nil {
			toolCount = len(a.tools.GetSchemas())
		}
		return fmt.Sprintf(`🔄 Started a new conversation! All previous context has been cleared.
		
Session context preserved: name, organization, role (if previously shared)

Model: %s
Tools: %d registered
Memory window: %d

Just type normally to chat with me!`,
			a.getModelName(),
			toolCount,
			a.cfg.Agents.Defaults.MemoryWindow,
		)
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
			a.getModelName(),
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

// cleanCommand strips the leading slash and trailing whitespace from a command.
func cleanCommand(content string) string {
	content = strings.TrimLeft(content, "/")
	return strings.TrimRight(content, " \n")
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

// sanitizeResponse strips internal context tags from LLM responses.
func sanitizeResponse(content string) string {
	// Strip <ctx_compress>...</ctx_compress> blocks (with or without content)
	re := regexp.MustCompile(`(?s)<ctx_compress>.*?</ctx_compress>`)
	content = re.ReplaceAllString(content, "")
	// Strip any bare <ctx_compress> or </ctx_compress> tags
	content = strings.ReplaceAll(content, "<ctx_compress>", "")
	content = strings.ReplaceAll(content, "</ctx_compress>", "")
	return strings.TrimSpace(content)
}

// inferTopic infers a short topic from a user message.
func inferTopic(content string) string {
	content = strings.TrimSpace(content)
	if len(content) < 5 {
		return ""
	}
	lower := strings.ToLower(content)
	// Remove common greetings
	greetings := []string{"hi", "hello", "hey", "sup", "yo", "what's up", "howdy"}
	for _, g := range greetings {
		if lower == g || strings.HasPrefix(lower, g+" ") || strings.HasPrefix(lower, g+",") {
			return ""
		}
	}
	// Strip question words for cleaner topic
	cleaned := strings.TrimPrefix(lower, "what ")
	cleaned = strings.TrimPrefix(cleaned, "what's ")
	cleaned = strings.TrimPrefix(cleaned, "what are ")
	cleaned = strings.TrimPrefix(cleaned, "tell me about ")
	cleaned = strings.TrimPrefix(cleaned, "tell me ")
	// Take first ~60 chars as topic snippet
	if len(cleaned) > 60 {
		cleaned = cleaned[:60] + "..."
	}
	return cleaned
}

// updateTopic updates the conversation topic based on the latest exchange.
func updateTopic(currentTopic, userMsg, assistantMsg string) string {
	userLower := strings.ToLower(userMsg)
	assistantLower := strings.ToLower(assistantMsg)

	// If user is changing the subject with a clear new topic
	if len(userMsg) > 10 && !isFollowUp(userLower) {
		return inferTopic(userMsg)
	}

	// Extract key nouns from assistant response for topic refinement
	if strings.Contains(assistantLower, "here's what") ||
		strings.Contains(assistantLower, "i found") ||
		strings.Contains(assistantLower, "according to") {
		// Topic likely stayed the same, keep current
		return currentTopic
	}

	return currentTopic
}

// isFollowUp checks if a message is a follow-up/continuation rather than a new topic.
func isFollowUp(lower string) bool {
	followUpWords := []string{"yes", "yeah", "do that", "go ahead", "tell me more",
		"continue", "and", "also", "what about", "how about", "ok", "okay",
		"sure", "great", "thanks", "cool"}
	for _, w := range followUpWords {
		if strings.HasPrefix(lower, w) || lower == w {
			return true
		}
	}
	return false
}
