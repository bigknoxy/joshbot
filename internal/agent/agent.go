package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	// Increased from 20 to 50 (issue #192) to support longer reasoning chains.
	DefaultMaxIterations = 50
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
	cfg      *config.Config
	provider providers.Provider
	tools    ToolExecutor
	sessions SessionManager
	// noLockerWarning keeps the "store cannot lock" warning out of the per-turn
	// path; the condition is a property of the store, so it is true forever or
	// never.
	noLockerWarning sync.Once
	memory          MemoryLoader
	history         HistoryAppender
	skills          SkillsLoader
	logger          *log.Logger
	budget          *ctxpkg.BudgetManager
	compressor      ContextCompressor
	maxIterations   int
	timeout         time.Duration
	skillDetector   *skills.SkillDetector
	extractor       *skills.Extractor
	skillLoader     *skills.Loader
	// modelName holds a runtime global model override set by `joshbot`
	// /model ... --global. It is read on every model resolution, so it is
	// mutex-guarded: two sessions processed concurrently (the Telegram bus
	// does exactly that) must not tear the read or race the write.
	modelName string
	modelMu   sync.RWMutex
}

func (a *Agent) getModelName() string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.getModelNameLocked()
}

// getModelNameLocked is getModelName without acquiring modelMu; the caller
// must already hold a read lock.
func (a *Agent) getModelNameLocked() string {
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
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.getResolvedModelNameLocked()
}

// getResolvedModelNameLocked is getResolvedModelName without acquiring modelMu;
// the caller must already hold a read lock.
func (a *Agent) getResolvedModelNameLocked() string {
	if a.modelName != "" {
		return a.resolveModelNameLocked(a.modelName)
	}
	if !a.cfg.UseModelsConfig() {
		return a.cfg.Agents.Defaults.Model
	}
	if modelConfig, err := a.cfg.GetActiveModel(); err == nil {
		return modelConfig.Model
	}
	return a.cfg.ModelsConfig.Agent.Model
}

// resolveModelName maps a model name to the concrete model ID when the
// model-centric config knows it; otherwise the spec is passed through
// verbatim (a bare model ID, or a "provider:model" spec).
func (a *Agent) resolveModelName(name string) string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.resolveModelNameLocked(name)
}

// resolveModelNameLocked is resolveModelName without acquiring modelMu; the
// caller must already hold a read lock.
func (a *Agent) resolveModelNameLocked(name string) string {
	if !a.cfg.UseModelsConfig() {
		return name
	}
	if m, ok := a.cfg.GetModel(name); ok {
		return m.Model
	}
	return name
}

// modelForSession returns the effective model for a session: the per-session
// override wins, then the runtime global override, then the config default.
// The session override never leaks between chats and survives restarts.
func (a *Agent) modelForSession(sess *session.Session) string {
	if sess != nil && sess.ModelOverride != "" {
		return sess.ModelOverride
	}
	return a.getModelName()
}

// modelForSessionLocked is modelForSession without acquiring modelMu; the
// caller must already hold a read lock.
func (a *Agent) modelForSessionLocked(sess *session.Session) string {
	if sess != nil && sess.ModelOverride != "" {
		return sess.ModelOverride
	}
	return a.getModelNameLocked()
}

// resolvedModelFor is modelForSession with the name resolved to the concrete
// model ID for context budgeting.
func (a *Agent) resolvedModelFor(sess *session.Session) string {
	if sess != nil && sess.ModelOverride != "" {
		return a.resolveModelName(sess.ModelOverride)
	}
	return a.getResolvedModelName()
}

// resolvedModelForLocked is resolvedModelFor without acquiring modelMu; the
// caller must already hold a read lock.
func (a *Agent) resolvedModelForLocked(sess *session.Session) string {
	if sess != nil && sess.ModelOverride != "" {
		return a.resolveModelNameLocked(sess.ModelOverride)
	}
	return a.getResolvedModelNameLocked()
}

// setGlobalModel records a runtime-wide model override. It also persists the
// change to config.json so it survives a restart; on the next boot the config
// read supplies the same value.
func (a *Agent) setGlobalModel(name string) {
	a.modelMu.Lock()
	a.modelName = name
	a.modelMu.Unlock()
}

// currentGlobalModel returns the runtime-wide model override, if any.
func (a *Agent) currentGlobalModel() string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.modelName
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

// SetMaxIterations updates the ReAct loop iteration limit at runtime.
// Returns the agent for chaining.
func (a *Agent) SetMaxIterations(n int) *Agent {
	if n > 0 {
		a.maxIterations = n
	}
	return a
}

// MaxIterations returns the current ReAct loop iteration limit.
func (a *Agent) MaxIterations() int {
	return a.maxIterations
}

// Timeout reports the per-turn deadline this agent runs under. It exists so
// cmd/joshbot can assert that agents.defaults.timeout actually reached the
// agent: that wiring is one line, and deleting it turns nothing else red.
func (a *Agent) Timeout() time.Duration { return a.timeout }

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

// SessionLocker is implemented by session stores that can serialise whole turns
// for one session key.
//
// It is optional and checked with a type assertion, like session.Archiver, so
// that test doubles and alternative SessionManager implementations are not
// forced to implement it. A store that does not implement it keeps the previous
// behaviour: turns for one key may interleave and lose each other's messages.
type SessionLocker interface {
	LockSession(ctx context.Context, sessionID string) (func(), error)
}

// Process handles an inbound message and returns the response content.
// It implements the full ReAct loop: receive message, call LLM, execute tools, repeat.
//
// The turn is serialised per session key. A turn is a read-modify-write over
// one session file, so two overlapping turns for the same key would each load
// the same prefix and the later save would publish a file missing the earlier
// turn's messages (#236). This is reachable by default through the HTTP API,
// where two requests carrying the same `user` land on one session.
//
// Acquisition happens after the timeout context is built, so a turn queued
// behind a long one waits against its own deadline rather than forever, and
// reports the timeout instead of hanging.
func (a *Agent) Process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	locker, ok := a.sessions.(SessionLocker)
	if !ok {
		// Failing open is deliberate — an alternative store must not be
		// unusable — but it is #236 restored, and the symptom is messages
		// vanishing from a transcript under load with nothing in the log to
		// correlate against. *session.Manager implements the interface, so this
		// branch only opens when something wraps it. Say so, once.
		a.noLockerWarning.Do(func() {
			a.logger.Warn("Session store does not support locking; concurrent turns for one session may lose messages",
				"type", fmt.Sprintf("%T", a.sessions))
		})
	}
	if ok {
		sessionKey := getSessionKey(msg)
		waitStart := time.Now()
		release, err := locker.LockSession(ctx, sessionKey)
		if err != nil {
			// LockSession validates before it queues, so a rejected session id
			// arrives here too. Reporting that as a lock failure sends the
			// reader hunting for a concurrency problem that does not exist.
			if errors.Is(err, session.ErrInvalidSessionID) {
				a.logger.Error("Rejected an invalid session id", "session", sessionKey, "error", err)
				return ReplyPrefix + fmt.Sprintf("invalid session id: %v", err), nil
			}
			a.logger.Error("Failed to acquire session lock",
				"session", sessionKey, "waited", time.Since(waitStart), "error", err)
			// ReplyPrefix, not a prefix of its own: Process reports failures in
			// band as reply text with a nil error, and every non-interactive
			// caller translates them back with ReplyError. A turn that gave up
			// waiting for the lock must reach `agent -m` as exit 1 and the HTTP
			// API as a 502, not as a 200 carrying an error string as the answer.
			return ReplyPrefix + fmt.Sprintf("could not acquire the session lock: %v", err), nil
		}
		defer release()
	}

	return a.process(ctx, msg)
}

// process is Process without the lock. handleResumeCommand re-enters it to run
// the resumed turn through the normal ReAct loop, and re-entering Process there
// would deadlock on the lock this goroutine already holds.
func (a *Agent) process(ctx context.Context, msg bus.InboundMessage) (string, error) {
	startTime := time.Now()
	a.logger.Info("Processing message",
		"channel", msg.Channel,
		"sender", msg.SenderID,
		"content_len", len(msg.Content),
	)

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
	if sess.Personality != "" {
		systemPrompt += "\n\n<personality>\n" + sess.Personality + "\n</personality>"
	}

	// Add user message to session
	userMsg := session.Message{
		Role:      session.RoleUser,
		Content:   msg.Content,
		Timestamp: time.Now(),
		Images:    imageRefs(msg.Images),
	}
	sess.AddMessage(userMsg)

	// Clear any existing checkpoint — a new user message starts a fresh run.
	// /resume is handled via handleCommand before we get here (isCommand
	// returns true for it), so reaching this point means the user typed
	// something other than /resume.
	sess.Checkpoint = nil

	// Build messages for LLM (system + session messages)
	messages := a.buildMessages(systemPrompt, sess)

	// Attach this turn's image bytes to the message that was just added. They
	// are deliberately not in the session — only a descriptor is (see
	// session.ImageRef) — so they are carried here for this request alone.
	attachImages(messages, msg.Images)

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
			Model:       a.modelForSession(sess),
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

		// Capture a fallback notice for this LLM call so the user learns
		// their answer came from a different provider than configured. The
		// streaming path installs its own callback inside streamChat (the
		// notice has to enter the delta stream before any content, so the
		// buffer and the reply text stay identical); this one covers the
		// non-streaming calls only.
		var fallbackNotice string
		callCtx := ctx
		if !a.cfg.Agents.Defaults.QuietFallback {
			callCtx = providers.WithFallbackNotice(ctx, func(n providers.FallbackNotice) {
				fallbackNotice = formatFallbackNotice(n)
			})
		}

		var resp *providers.ChatResponse
		var err error
		if streaming {
			resp, err = a.streamChat(ctx, req, sink)
			// A provider with no streaming endpoint (github-copilot today)
			// must not fail the turn: streaming is on by default, so erroring
			// here would break every interactive message on that provider.
			// The fallback is safe because the stream never opened, so nothing
			// has been delivered to the sink yet.
			if errors.Is(err, providers.ErrStreamingUnsupported) {
				resp, err = a.provider.Chat(callCtx, req)
			}
			// A stream that died before delivering anything is retried once
			// through the non-streaming path — safe for the same reason as
			// above (nothing reached the sink), and Chat brings the whole
			// retry/fallback chain with it, so a mid-open connection reset no
			// longer ends the turn with "[stream error: ...]" as the answer.
			// The reply arrives unstreamed; every sink consumer already
			// handles that (it is the ErrStreamingUnsupported path).
			if errors.Is(err, errStreamDiedEmpty) {
				a.logger.Warn("Stream died before any content, retrying via Chat", "error", err)
				resp, err = a.provider.Chat(callCtx, req)
			}
		} else {
			resp, err = a.provider.Chat(callCtx, req)
		}
		if err != nil {
			return "", fmt.Errorf("LLM call failed: %w", err)
		}

		// Surface per-call token usage to any headless caller (e.g. the CLI
		// JSON output modes) via the per-request usage sink. Concurrency-safe:
		// the sink rides the context, never shared Agent state.
		if usageSink := usageFromContext(ctx); usageSink != nil {
			usageSink(resp.Usage)
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

			// A non-streaming reply answered by a fallback carries the
			// notice as a visible first line. (Streamed replies had it
			// woven in by streamChat already.)
			if fallbackNotice != "" {
				content = fallbackNotice + content
			}

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

	// Hit max iterations — save a checkpoint so the user can resume.
	// The session has already accumulated all messages and tool results from
	// this run, so on /resume we just re-enter reactLoop with the existing
	// session and a fresh message count.
	a.logger.Warn("Hit max iterations", "max", a.maxIterations)

	// Persist a checkpoint marker in the session for /resume detection.
	//
	// checkpointSaved gates the `/resume` suggestion, and it is a bool rather
	// than a nil error check because there are two ways not to have a
	// checkpoint: the save failed, or there is no session manager at all. A
	// `err == nil` gate reads the second case as success and offers `/resume`,
	// which then answers "session manager not initialized" — the same dead end
	// the discarded error caused (#244).
	checkpointSaved := false
	if a.sessions != nil {
		sess.Checkpoint = &session.Checkpoint{
			Iteration:     a.maxIterations,
			MaxIterations: a.maxIterations,
			CreatedAt:     time.Now(),
			UserMessage:   userMessage,
		}
		// Save the session so the checkpoint survives across requests.
		saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := a.sessions.Save(saveCtx, sess)
		cancel()
		if err != nil {
			a.logger.Error("Failed to save checkpoint", "session", sess.ID, "error", err)
		}
		checkpointSaved = err == nil
	}

	resp := fmt.Sprintf("I've been working on this for a while. Here's what I found so far.\n\n"+
		"⚠️ Hit the max iteration limit (%d).", a.maxIterations)
	if checkpointSaved {
		resp += "\nTo continue, type `/resume` and I'll pick up where we left off."
	}
	return resp, nil
}

// errStreamDiedEmpty reports a stream that died before delivering any text.
// Unlike a mid-text death, nothing has reached the sink, so the call can be
// retried invisibly — returning a marker for this case ended the turn with
// "[stream error: ...]" standing in for an answer the chain could still have
// produced. Text already shown can never be retried; that case keeps the
// marker.
var errStreamDiedEmpty = errors.New("stream died before any content was delivered")

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
	// Fallback notice for streamed replies. It fires while the stream is
	// being opened, but it is emitted lazily, right before the first content
	// delta: emitted eagerly it would surface for a tool-call-only response
	// that shows no text, and the notice must enter both the sink and the
	// accumulated content so the streamed buffer and the reply text stay
	// identical — a divergence there re-sends the whole reply through the
	// bus fallback on Telegram.
	var notice string
	if !a.cfg.Agents.Defaults.QuietFallback {
		ctx = providers.WithFallbackNotice(ctx, func(n providers.FallbackNotice) {
			notice = formatFallbackNotice(n)
		})
	}
	noticeShown := false
	emitNotice := func() string {
		if notice == "" || noticeShown {
			return ""
		}
		noticeShown = true
		sink(StreamEvent{Delta: notice})
		return notice
	}

	stream, err := a.provider.ChatStream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("stream failed to open: %w", err)
	}

	acc := providers.NewChunkAccumulator()
	var accumulatedContent string

	for chunk := range stream {
		if err := acc.Accumulate(chunk); err != nil {
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

			// Nothing delivered yet: the caller can retry invisibly.
			if accumulatedContent == "" {
				return nil, fmt.Errorf("%w: %w", errStreamDiedEmpty, err)
			}

			// Stream error mid-text — append a visible marker to what was
			// already shown and return the partial content, not an error, so
			// the caller sees the text that was already delivered.
			marker := streamErrorMarker(err, true)
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

		// Forward text deltas to the sink as they arrive.
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				accumulatedContent = emitNotice() + accumulatedContent
				accumulatedContent += choice.Delta.Content
				sink(StreamEvent{Delta: choice.Delta.Content})
			}
		}
	}

	resp, err := acc.Result()
	if err != nil {
		// Nothing delivered yet: the caller can retry invisibly. (A stream
		// that dies before any text used to end the turn with a
		// "[stream error: ...]" marker standing in for an answer the chain
		// could still have produced.)
		if accumulatedContent == "" {
			return nil, fmt.Errorf("%w: %w", errStreamDiedEmpty, err)
		}

		// Stream ended with a truncation error mid-text — append a visible
		// marker to what was already shown.
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

	// The reply text must carry the notice the sink already showed, or the
	// session, the history and every non-streaming consumer of this response
	// records a different answer than the one on screen.
	if noticeShown && len(resp.Choices) > 0 {
		resp.Choices[0].Message.Content = notice + resp.Choices[0].Message.Content
	}

	// Signal completion to the sink.
	sink(StreamEvent{Done: true})

	return resp, nil
}

// formatFallbackNotice renders the one-line, user-facing note that a reply
// was answered by a fallback provider. Prepended to the reply rather than
// sent out of band so every channel — CLI, Telegram, Discord, HTTP API —
// carries it without per-channel wiring.
func formatFallbackNotice(n providers.FallbackNotice) string {
	return fmt.Sprintf("⚠️ %s unavailable (%s) — answered by %s (%s)\n\n", n.From, n.Reason, n.To, n.Model)
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

	model := a.resolvedModelFor(sess)
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
		model := a.resolvedModelFor(sess)
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
	cmdLine := cleanCommand(msg.Content)
	cmd := cmdLine
	if i := strings.IndexAny(cmdLine, " \t"); i >= 0 {
		cmd = cmdLine[:i]
	}

	switch cmd {
	case "start":
		return "Hello! I'm joshbot, your personal AI assistant. How can I help you today?"
	case "new":
		// Clear messages but preserve conversation context (user facts survive
		// /new). The per-session model override and personality are scoped to
		// the conversation, so they are cleared too.
		sessionKey := getSessionKey(msg)
		sess, err := a.sessions.GetOrCreate(ctx, sessionKey)
		if err == nil {
			sess.ClearMessages()
			sess.ModelOverride = ""
			sess.Personality = ""
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
/new - Start fresh (clears session model/personality)
/model [name] - Switch model for this session (--global for all)
/personality [name] - Set or clear a personality
/compact - Summarize older context now
/resume - Continue after hitting the iteration limit
/status - Show system status
/help - Show this help

Just type normally to chat with me!`
	case "status":
		toolCount := 0
		if a.tools != nil {
			toolCount = len(a.tools.GetSchemas())
		}
		model := a.getModelName()
		if sess, err := a.sessions.GetOrCreate(ctx, getSessionKey(msg)); err == nil {
			model = a.modelForSession(sess)
		}
		status := fmt.Sprintf(`Status:
  Model: %s
  Tools: %d registered
  Memory window: %d
  Max iterations: %d`,
			model,
			toolCount,
			a.cfg.Agents.Defaults.MemoryWindow,
			a.cfg.Agents.Defaults.MaxToolIterations,
		)
		// Provider health is optional (checked by type assertion, like
		// session.Archiver) and process-local, so it only appears when the
		// LLM is a MultiProvider with something to report.
		if hp, ok := a.provider.(interface {
			HealthSnapshot() []providers.ProviderHealthInfo
		}); ok {
			if infos := hp.HealthSnapshot(); len(infos) > 0 {
				status += "\n  Provider health:"
				for _, info := range infos {
					line := fmt.Sprintf("\n    %s: %d consecutive failure(s), last: %s", info.Name, info.Failures, info.LastErr)
					if until := time.Until(info.CoolUntil); until > 0 {
						line += fmt.Sprintf(", deprioritized for %s", until.Round(time.Second))
					}
					status += line
				}
			}
		}
		return status
	case "model":
		return a.handleModelCommand(ctx, msg)
	case "personality":
		return a.handlePersonalityCommand(ctx, msg)
	case "compact":
		return a.handleCompactCommand(ctx, msg)
	case "resume":
		return a.handleResumeCommand(ctx, msg)
	}

	return "" // Not a known command, process normally
}

// handleModelCommand implements /model. With no argument it lists the current
// model and the ones a user can switch to. With an argument it sets a
// per-session override (persisted, cleared by /new); with --global it changes
// the default for every session and writes it to config.json.
func (a *Agent) handleModelCommand(ctx context.Context, msg bus.InboundMessage) string {
	sessionKey := getSessionKey(msg)
	sess, err := a.sessions.GetOrCreate(ctx, sessionKey)
	if err != nil {
		return fmt.Sprintf("Error: Failed to load session: %v", err)
	}

	rest, global := parseModelArgs(cleanCommand(msg.Content))

	if len(rest) == 0 {
		if global {
			return "Usage: /model <name> --global"
		}
		return a.modelList(sess)
	}

	canonical, err := a.resolveModelSpec(rest[0])
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	if global {
		hadOverride := sess.ModelOverride != ""
		sess.ModelOverride = ""
		if err := a.setGlobalModelAndPersist(canonical); err != nil {
			a.logger.Warn("Failed to persist global model", "error", err)
			return fmt.Sprintf("Error: failed to save config: %v", err)
		}
		if hadOverride {
			if err := a.sessions.Save(ctx, sess); err != nil {
				a.logger.Warn("Failed to clear session override after global change", "error", err)
				return fmt.Sprintf("Error: changed the global default but could not clear this session's override: %v", err)
			}
		}
		return fmt.Sprintf("✓ Default model changed to %s for all sessions.\n\nNew conversations use it, and this session's override (if any) was cleared.", canonical)
	}

	sess.ModelOverride = canonical
	if err := a.sessions.Save(ctx, sess); err != nil {
		a.logger.Warn("Failed to save session after /model", "error", err)
		return fmt.Sprintf("Error: could not save the session, so the model change will not persist: %v", err)
	}
	return fmt.Sprintf("✓ Model switched to %s for this session.\n\nUse /model %s --global to make it the default for all sessions.", canonical, canonical)
}

// parseModelArgs splits a /model line into the model spec tokens and whether
// the --global flag was present. It expects the slash-stripped command
// ("model smart --global"). Trailing flags are read here rather than by the
// caller so the order of the flag relative to the argument does not matter.
func parseModelArgs(content string) (rest []string, global bool) {
	fields := strings.Fields(content)
	for _, f := range fields[1:] {
		switch f {
		case "--global":
			global = true
		default:
			rest = append(rest, f)
		}
	}
	return rest, global
}

// modelList renders the effective model for the session and everything a user
// can switch to.
func (a *Agent) modelList(sess *session.Session) string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()

	active := a.modelForSessionLocked(sess)
	var b strings.Builder
	if a.cfg.UseModelsConfig() {
		fmt.Fprintf(&b, "Current model: %s\n\nAvailable models:\n", active)
		for _, m := range a.cfg.ModelsConfig.Models {
			if m.Disabled {
				continue
			}
			marker := "  "
			if m.Name == active {
				marker = "> "
			}
			fmt.Fprintf(&b, "%s%s - %s\n", marker, m.Name, m.Model)
		}
		b.WriteString("\nUsage: /model <name>  or  /model <name> --global")
		return b.String()
	}
	fmt.Fprintf(&b, "Current model: %s\n\nAvailable providers:\n", active)
	for name, p := range a.cfg.Providers {
		if !p.Enabled || p.APIKey == "" {
			continue
		}
		marker := "  "
		if active == name || strings.HasPrefix(active, name+":") {
			marker = "> "
		}
		model := p.Model
		if model == "" {
			model = a.cfg.Agents.Defaults.Model
		}
		fmt.Fprintf(&b, "%s%s - %s\n", marker, name, model)
	}
	b.WriteString("\nUsage: /model <provider:model>  or  /model <name> --global")
	return b.String()
}

// resolveModelSpec validates a /model argument and returns the canonical spec
// to persist: a configured model name in model-centric mode, or a
// provider:model (or bare provider) spec in legacy mode.
func (a *Agent) resolveModelSpec(spec string) (string, error) {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.resolveModelSpecLocked(spec)
}

// resolveModelSpecLocked is resolveModelSpec without acquiring modelMu; the
// caller must already hold a read lock.
func (a *Agent) resolveModelSpecLocked(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", fmt.Errorf("model name is required")
	}
	if a.cfg.UseModelsConfig() {
		if m, ok := a.cfg.GetModel(spec); ok && !m.Disabled {
			return m.Name, nil
		}
		for _, m := range a.cfg.ModelsConfig.Models {
			if m.Disabled {
				continue
			}
			if m.Model == spec || config.StripProviderPrefix(m.Model) == spec {
				return m.Name, nil
			}
		}
		return "", fmt.Errorf("unknown model %q (see /model for the list)", spec)
	}
	if idx := strings.Index(spec, ":"); idx > 0 {
		provider := spec[:idx]
		if p, ok := a.cfg.Providers[provider]; !ok || !p.Enabled {
			return "", fmt.Errorf("unknown or disabled provider %q", provider)
		}
		return spec, nil
	}
	if p, ok := a.cfg.Providers[spec]; ok && p.Enabled {
		return spec, nil
	}
	return "", fmt.Errorf("unknown provider %q (see /model for the list)", spec)
}

// setGlobalModelAndPersist records a runtime-wide model override and writes it
// to config.json so it survives a restart. Both the in-memory config and the
// runtime override are mutated under modelMu so concurrent model resolution
// never observes a torn value. The whole write — including config.Save's
// marshal of the shared config — runs under the write lock, because a
// concurrent /model list on another session reads the same fields.
func (a *Agent) setGlobalModelAndPersist(name string) error {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()

	a.modelName = name
	if a.cfg.UseModelsConfig() {
		a.cfg.ModelsConfig.Agent.Model = name
	} else {
		a.cfg.Agents.Defaults.Model = name
	}
	return config.Save(a.cfg)
}

// handlePersonalityCommand implements /personality. With no argument it shows
// the current personality. With "none" it clears it. A known preset name
// expands to a canned instruction; anything else is used verbatim as the
// personality instruction.
func (a *Agent) handlePersonalityCommand(ctx context.Context, msg bus.InboundMessage) string {
	sessionKey := getSessionKey(msg)
	sess, err := a.sessions.GetOrCreate(ctx, sessionKey)
	if err != nil {
		return fmt.Sprintf("Error: Failed to load session: %v", err)
	}

	arg := strings.TrimSpace(strings.TrimPrefix(cleanCommand(msg.Content), "personality"))

	if arg == "" {
		if sess.Personality == "" {
			return fmt.Sprintf("No personality set for this session.\n\nTry one of: %s\nOr your own instruction: /personality <text>\nClear it with: /personality none", presetNames())
		}
		return fmt.Sprintf("Current personality: %s\n\nUse /personality none to clear it.", sess.Personality)
	}

	if strings.EqualFold(arg, "none") {
		sess.Personality = ""
		if err := a.sessions.Save(ctx, sess); err != nil {
			a.logger.Warn("Could not save session after /personality none", "error", err)
			return fmt.Sprintf("Error: could not clear the personality, it may still be active: %v", err)
		}
		return "✓ Personality cleared."
	}

	if preset, ok := personalityPresets[strings.ToLower(arg)]; ok {
		sess.Personality = preset
	} else {
		sess.Personality = arg
	}
	if err := a.sessions.Save(ctx, sess); err != nil {
		a.logger.Warn("Could not save session after /personality", "error", err)
		return fmt.Sprintf("Error: could not save the personality, it will not persist: %v", err)
	}
	return fmt.Sprintf("✓ Personality set for this session:\n\n%s\n\nUse /personality none to clear it.", sess.Personality)
}

// handleCompactCommand implements /compact: it summarizes the session history
// immediately instead of waiting for the context budget to cross its
// threshold. The summarized prefix is replaced by a compaction record (via
// applyCompaction, which archives the replaced messages) and persisted.
func (a *Agent) handleCompactCommand(ctx context.Context, msg bus.InboundMessage) string {
	sessionKey := getSessionKey(msg)
	sess, err := a.sessions.GetOrCreate(ctx, sessionKey)
	if err != nil {
		return fmt.Sprintf("Error: Failed to load session: %v", err)
	}
	if a.budget == nil || a.compressor == nil {
		return "Context compression is not available in this build."
	}
	if len(sess.Messages) == 0 {
		return "Nothing to compact yet — this session has no messages."
	}

	model := a.resolvedModelFor(sess)
	maxCompletion := a.cfg.Agents.Defaults.MaxTokens
	budget := a.budget.ComputeBudget(model, maxCompletion)

	sessionMsgs := sessionToProviderMessages(sess)
	compressed, err := a.compressor.CompressMessages(ctx, model, sessionMsgs, budget)
	if err != nil {
		a.logger.Warn("Manual context compaction failed", "error", err)
		return fmt.Sprintf("Error: compaction failed: %v", err)
	}
	if strings.TrimSpace(compressed) == "" {
		return "Error: compaction produced an empty summary."
	}

	prefixLen := len(sess.Messages)
	a.applyCompaction(ctx, sess, compactionState{summary: compressed, prefixLen: prefixLen, active: true})
	if err := a.sessions.Save(ctx, sess); err != nil {
		a.logger.Warn("Failed to save session after /compact", "error", err)
	}
	return fmt.Sprintf("✓ Compressed the last %d messages into a summary. The earlier conversation is archived in this session's history.", prefixLen)
}

// handleResumeCommand implements /resume. It picks up a ReAct loop that was
// interrupted by the iteration limit, using the session's checkpoint as proof
// that a resumed run is valid. If no checkpoint exists, it tells the user.
func (a *Agent) handleResumeCommand(ctx context.Context, msg bus.InboundMessage) string {
	if a.sessions == nil {
		return "Error: session manager not initialized; cannot resume."
	}
	sessionKey := getSessionKey(msg)
	sess, err := a.sessions.GetOrCreate(ctx, sessionKey)
	if err != nil {
		return fmt.Sprintf("Error: Failed to load session: %v", err)
	}

	cp := sess.Checkpoint
	if cp == nil {
		return "No checkpoint found — there's nothing to resume. The iteration limit hasn't been hit in this session yet."
	}

	// Clear the checkpoint so a plain user message after /resume starts fresh.
	sess.Checkpoint = nil
	if err := a.sessions.Save(ctx, sess); err != nil {
		a.logger.Warn("Failed to save session after clearing checkpoint", "error", err)
	}

	// Build a synthetic "continue" message and process it through the normal
	// ReAct loop. The session already holds all the accumulated messages and
	// tool results, so reactLoop will pick up from where it left off.
	continueMsg := bus.InboundMessage{
		Channel:   msg.Channel,
		SenderID:  msg.SenderID,
		Content:   "Please continue the task you were working on, using the context already established in this conversation.",
		Timestamp: msg.Timestamp,
	}

	// a.process, not a.Process: this goroutine already holds the session lock,
	// and re-entering Process would deadlock against itself.
	resp, err := a.process(ctx, continueMsg)
	if err != nil {
		return fmt.Sprintf("Error: resume failed: %v", err)
	}

	return fmt.Sprintf("Resuming from checkpoint (stopped at iteration %d/%d).\n\n%s",
		cp.Iteration, cp.MaxIterations, resp)
}

// CurrentModel reports the effective model for the CLI session. The interactive
// TTY status bar uses it; sessions (and their overrides) belong to the caller
// of Process, so a per-session override on any other chat never shows here.
func (a *Agent) CurrentModel() string {
	sess, err := a.sessions.GetOrCreate(context.Background(), "cli:cli_user")
	if err != nil {
		return a.getModelName()
	}
	return a.modelForSession(sess)
}

// personalityPresets is the fixed set of named personalities /personality
// knows. Any other argument is treated as a custom instruction verbatim.
var personalityPresets = map[string]string{
	"concise":   "Answer concisely. Use short, direct sentences and avoid filler.",
	"technical": "Assume a technical audience. Prefer precise terminology and cite file paths or commands where relevant.",
	"pirate":    "Talk like a pirate. Sprinkle in 'arr', 'matey' and 'ye'.",
	"cheerful":  "Be warm, upbeat and encouraging, with light humour.",
	"formal":    "Use formal, professional language and address the user politely.",
}

// presetNames lists the named personalities for the /personality help text.
func presetNames() string {
	names := make([]string, 0, len(personalityPresets))
	for name := range personalityPresets {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
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
