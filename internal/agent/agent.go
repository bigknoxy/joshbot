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
	"unicode/utf8"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/commands"
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

// SessionResetter is implemented by session stores that can clear a session's
// transcript under a generation fence.
//
// It is optional and checked with a type assertion, for the same reason
// SessionLocker is: a test double or an alternative SessionManager must not be
// forced to implement it. A store that does not implement it keeps the previous
// behaviour, where /new clears the session in place while holding the turn lock.
type SessionResetter interface {
	ResetConversation(ctx context.Context, sessionID string) (*session.Session, error)
}

// isResetCommand reports whether content is the /new command.
//
// /new is the one command routed ahead of the per-key turn lock: a reset that
// queues behind the stuck turn the user is trying to escape is not a reset, and
// under the per-key cap it is not merely slow but refused outright with
// ErrKeyLockBusy (#319). session.Manager.ResetConversation bumps the session
// generation and Save refuses any older write, so the in-flight turn cannot
// republish the transcript the user just cleared.
func isResetCommand(content string) bool {
	if !isCommand(content) {
		return false
	}
	cmd := cleanCommand(content)
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		cmd = cmd[:i]
	}
	return cmd == "new"
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

	// A reset skips the lock entirely when the store can fence it. See
	// isResetCommand.
	if _, canReset := a.sessions.(SessionResetter); canReset && isResetCommand(msg.Content) {
		return a.process(ctx, msg)
	}

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
			// A key over its per-turn cap gets clean backpressure, not the generic
			// lock-failure text: the session is healthy, it is just busy. Still in
			// band (ReplyPrefix) so `agent -m` exits 1 and the HTTP API answers 502 —
			// a turn that did not run is a failure, not an answer (#245).
			if errors.Is(err, session.ErrKeyLockBusy) {
				a.logger.Warn("Session key over its concurrent-turn cap; applying backpressure",
					"session", sessionKey, "cap", session.MaxConcurrentTurnsPerKey, "waited", time.Since(waitStart))
				return ReplyPrefix + fmt.Sprintf(
					"This conversation already has too many turns in flight (cap %d); please wait for the current turn to finish and try again.",
					session.MaxConcurrentTurnsPerKey), nil
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

	// The turn's channel rides the context for the whole turn, so every tool
	// call in it sees the same recipient. send_file resolves its address from
	// here rather than taking one from the model.
	ctx = tools.WithChannel(ctx, msg.Channel)

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
		// ReplyPrefix, never a bespoke prefix: this is a failed turn, and a
		// non-interactive caller translates it back into an error only when
		// the text matches ReplyError. The old "Error: Failed to load
		// session" reached the HTTP API as a 200 and `agent -m` as exit 0.
		return ReplyPrefix + fmt.Sprintf("failed to load session: %v", err), nil
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
		Documents: documentRefs(msg.Documents),
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
	attachDocuments(messages, msg.Documents)

	// Run ReAct loop with channel info for async callbacks
	channelID := msg.SenderID // Use SenderID as the channel identifier
	compaction := compactionState{turnStart: len(sess.Messages) - 1}
	responseContent, err := a.reactLoop(ctx, messages, sess, msg.Channel, channelID, msg.Content, &compaction)
	if err != nil {
		a.logger.Error("ReAct loop error", "error", err)
		// A spent turn budget must not take the accumulated conversation with
		// it. The session already holds every message and tool result the loop
		// accumulated, and it used to be dropped on every error path because
		// the return happened before the save at the end of Process. This
		// covers both expiry (DeadlineExceeded) and a client-disconnect
		// cancellation (Canceled, e.g. a dropped HTTP request) — the real
		// Manager.Save refuses a spent context, so a fresh one is required.
		if ctx.Err() != nil {
			persistCtx, pcancel := a.persistenceCtx(ctx)
			defer pcancel()
			if err := a.sessions.Save(persistCtx, sess); err != nil {
				a.logger.Warn("Failed to save session after a spent-budget turn", "session", sess.ID, "error", err)
			}
		}
		// Check for timeout
		if ctx.Err() == context.DeadlineExceeded {
			reply := "I'm sorry, but processing your request took too long. Please try again or simplify your request."
			// The reply must reach the stream sink: every consumer decides
			// "was the answer shown?" by "did anything stream this turn?", so
			// a turn that already sent any delta (narration, a progress line)
			// has made that true and a plain-text return is then suppressed —
			// the timeout notice reached nobody (#283).
			// And it must not glue onto narration the turn already streamed:
			// "Let me try one more angle:I'm sorry, but processing your
			// request took too long" read as one sentence (#347). The
			// separator goes to the sink only; the returned text is a
			// message of its own on every non-streaming path.
			emitThroughSink(ctx, narrationSeparator(a.cfg.Agents.Defaults.Streaming, sess.Messages[startSessionLen:])+reply)
			return reply, nil
		}
		reply := fmt.Sprintf("Error processing request: %v", err)
		// A raw provider error tells the user what broke, never what to do
		// about it. The hint is appended, not substituted: callers that
		// match on the error text (tests, the ReplyPrefix contract, humans
		// filing issues) keep the full detail.
		if hint := llmErrorHint(err); hint != "" {
			reply += "\n\n" + hint
		}
		return reply, nil
	}

	// The writes below (history, compaction, session, topic) must survive a
	// spent turn budget. A max-iteration turn whose last tool ran past the
	// deadline needs its checkpoint and session saved more than any other
	// turn, and every write used to inherit the dead turn context and fail
	// instantly (#283).
	pctx, pcancel := a.persistenceCtx(ctx)
	defer pcancel()

	if a.history != nil {
		newMessages := sess.Messages[startSessionLen:]
		if shouldRecordSignificantTurn(newMessages, msg.Content, responseContent) {
			entry := formatHistoryEntry(msg.Content, responseContent, newMessages)
			if err := a.history.AppendHistory(pctx, entry); err != nil {
				a.logger.Warn("Failed to append history", "error", err)
			}
		}
	}

	// Fold in any compaction produced during the turn. This runs after the
	// history append above, which slices sess.Messages from startSessionLen —
	// shrinking the session before that point would slice out of range.
	a.applyCompaction(pctx, sess, compaction)

	// Save session. A superseded write is the generation fence working, not a
	// failure: /new landed while this turn was running and writing would
	// resurrect the transcript the user just cleared (#319).
	if err := a.sessions.Save(pctx, sess); err != nil {
		if errors.Is(err, session.ErrSessionSuperseded) {
			a.logger.Debug("Dropped a session write superseded by a reset", "error", err)
		} else {
			a.logger.Warn("Failed to save session", "error", err)
		}
	}

	// Update conversation topic based on what happened
	if !isCommand(msg.Content) && responseContent != "" {
		updatedTopic := updateTopic(sess.ConversationTopic, msg.Content, responseContent)
		if updatedTopic != "" {
			sess.ConversationTopic = updatedTopic
			if err := a.sessions.Save(pctx, sess); err != nil {
				if errors.Is(err, session.ErrSessionSuperseded) {
					a.logger.Debug("Dropped a topic write superseded by a reset", "error", err)
				} else {
					a.logger.Warn("Failed to save updated topic", "error", err)
				}
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

	// Turn-scoped stream state. The fallback notice is captured once for the
	// whole turn, not once per LLM call: a turn that calls a tool makes two
	// or more LLM calls, and a per-call notice reached the chat once per
	// call — twice at the top of a reply, and glued mid-sentence onto the
	// narration of the iteration before (#339).
	//
	// And once per *outage*, not once per turn, in full: the first turn a
	// fallback answers carries the whole notice; later turns answered by the
	// same fallback for the same kind of failure carry a one-line marker,
	// because a provider that retired the configured model is down for the
	// life of the session and the same paragraph at the top of every reply
	// buries the one instruction in it (#348). The key is persisted on the
	// session and cleared when the addressed provider answers again.
	ts := &turnStream{}
	if !a.cfg.Agents.Defaults.QuietFallback {
		ctx = providers.WithFallbackNotice(ctx, func(n providers.FallbackNotice) {
			if ts.notice == "" {
				ts.noticeKey = fallbackNoticeKey(n)
				if sess != nil && sameOutage(sess.FallbackNoticed, n) {
					ts.noticeKey = sess.FallbackNoticed
					ts.notice = formatFallbackMarker(n)
				} else {
					ts.notice = formatFallbackNotice(n)
					if observe := fallbackObserverFromContext(ctx); observe != nil {
						observe(n)
					}
				}
			}
		})
		defer func() {
			if sess == nil {
				return
			}
			switch {
			case ts.noticeKey != "":
				sess.FallbackNoticed = ts.noticeKey
			case ts.answered:
				// The addressed provider answered: the next outage is news.
				sess.FallbackNoticed = ""
			}
		}()
	}
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

		var resp *providers.ChatResponse
		var err error
		if streaming {
			resp, err = a.streamChat(ctx, req, sink, ts)
			// A provider with no streaming endpoint (github-copilot today)
			// must not fail the turn: streaming is on by default, so erroring
			// here would break every interactive message on that provider.
			// The fallback is safe because the stream never opened, so nothing
			// has been delivered to the sink yet.
			if errors.Is(err, providers.ErrStreamingUnsupported) {
				resp, err = a.provider.Chat(ctx, req)
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
				resp, err = a.provider.Chat(ctx, req)
			}
		} else {
			resp, err = a.provider.Chat(ctx, req)
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
		ts.answered = true

		// Check if we have a valid response
		if len(resp.Choices) == 0 {
			// Same contract as empty content below: a choiceless response is
			// a failure, and returning friendly text with a nil error let it
			// reach scripts as exit 0 and the HTTP API as a 200.
			a.logger.Warn("Empty response from LLM")
			return "", fmt.Errorf("the model returned a response with no choices — this is a provider problem, not an answer")
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
				// An empty reply with no tool calls is a provider problem,
				// and it used to be papered over with "I've processed your
				// request." — a confident non-answer that reached scripts as
				// exit 0 and the HTTP API as a 200. Report it as the failure
				// it is; Process wraps it into the in-band ReplyPrefix
				// contract every caller already translates.
				a.logger.Warn("Empty content from LLM",
					"model", a.getModelName(),
					"iteration", iteration+1,
				)
				return "", fmt.Errorf("the model returned an empty reply (no text, no tool calls) — this is a provider problem, not an answer")
			}

			// Sanitize: strip internal context tags from response
			content = sanitizeResponse(content)

			// The fallback notice opens the reply exactly once per turn. A
			// streamed turn already showed it through the sink (before the
			// first delta, whichever iteration produced it); prepending it
			// here keeps the session, the history and every non-streaming
			// consumer on the same text the user saw. It is never stored on
			// a tool-call iteration's message (see below), so the model does
			// not read its own earlier reply as starting with a warning and
			// echo it.
			if ts.notice != "" {
				content = ts.notice + content
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
		// Save the session so the checkpoint survives across requests. The
		// save must not inherit a spent turn context: a max-iteration turn
		// whose last tool outran the deadline is exactly when the checkpoint
		// is needed, and a dead context fails the write instantly.
		saveParent, pcancel := a.persistenceCtx(ctx)
		saveCtx, cancel := context.WithTimeout(saveParent, 5*time.Second)
		err := a.sessions.Save(saveCtx, sess)
		cancel()
		pcancel()
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
	// The reply must reach the stream sink. Every consumer decides "was the
	// answer shown?" by "did anything stream this turn?" — the sink exists
	// before the first delta, so even a tool-call-only turn (or one where
	// only narration streamed) counts — and a plain-text return is then
	// suppressed. The reply that explains why the turn stopped must ride the
	// sink, or it reaches nobody (#283).
	emitThroughSink(ctx, narrationSeparator(a.cfg.Agents.Defaults.Streaming, turnMessages(sess, st))+resp)
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
func (a *Agent) streamChat(ctx context.Context, req providers.ChatRequest, sink StreamSink, ts *turnStream) (*providers.ChatResponse, error) {
	// Everything that must precede this call's first content delta goes
	// through here, lazily: emitted eagerly it would surface for a
	// tool-call-only response that shows no text. Two things can precede it.
	// A paragraph break, when an earlier iteration of this turn already
	// streamed text that did not end its line — otherwise the narration
	// before a tool call ("Let me check that.") and the answer after it are
	// glued into one sentence on every channel. And the turn's fallback
	// notice, once: the notice callback fires while the stream is being
	// opened (or fired on an earlier call of the turn), and the reply text is
	// given the same notice by reactLoop, so what the sink showed and what
	// the session stores stay the same answer.
	firstDelta := true
	beforeFirstDelta := func() {
		if !firstDelta {
			return
		}
		firstDelta = false
		if ts.streamed && !ts.tailNewline {
			// Sink only, never content: it belongs to the join between two
			// iterations, not to either one's message. That is safe for
			// the Telegram streamer, whose Finish suppression compares what
			// it flushed against its own delta buffer (shown == buf) — the
			// reply string is not part of that contract, and a multi-
			// iteration turn already returns only its last iteration's
			// text while the chat holds every iteration's.
			sink(StreamEvent{Delta: "\n\n"})
		}
		if ts.notice != "" && !ts.noticeShown {
			ts.noticeShown = true
			sink(StreamEvent{Delta: ts.notice})
		}
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
				beforeFirstDelta()
				accumulatedContent += choice.Delta.Content
				ts.streamed = true
				ts.tailNewline = strings.HasSuffix(choice.Delta.Content, "\n")
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
		// marker to what was already shown. Not for a cancellation the user
		// asked for (the Stop button, a dropped client): the caller marks a
		// stopped turn itself, and "[stream error: context canceled]" above
		// "stopped by you" reads as two failures.
		if !errors.Is(ctx.Err(), context.Canceled) {
			marker := streamErrorMarker(err, accumulatedContent != "")
			sink(StreamEvent{Delta: marker, Done: true})
			accumulatedContent += marker
		}
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

// llmErrorHint maps the common LLM failure classes to one actionable next
// step. The raw error stays in the reply (see the call site); this only adds
// what to do about it. An empty return means no hint applies.
func llmErrorHint(err error) string {
	if err == nil {
		return ""
	}
	// The aggregate "all providers failed" error carries several statuses at
	// once; a single-status hint would pick one arbitrarily and mislead.
	if strings.Contains(err.Error(), "all providers failed") {
		return "Every provider in the fallback chain failed. Run `joshbot preflight` to check the configuration without dialling anyone, or add a fallback with `joshbot configure --fallback`."
	}

	provider := providers.ProviderFromError(err)
	switch providers.StatusCodeFromError(err) {
	case 401, 403:
		who := "The provider"
		if provider != "" {
			who = provider
		}
		return fmt.Sprintf("%s rejected the API key — update it with `joshbot configure --provider <name> --api-key <key>`, or run `joshbot preflight` to see which credential is in use.", who)
	case 404:
		if provider == "ollama" {
			return "Ollama does not have that model — pull it with `ollama pull <model>`."
		}
		return "The model was not found — check the model name with `/model`, or run `joshbot preflight` to see the exact ID being sent."
	case 429:
		return "The provider is rate-limiting — a fallback provider keeps the conversation going: `joshbot configure --fallback \"<primary>,<backup>\"`."
	}
	return ""
}

// turnStream is the per-turn state shared by every LLM call of one ReAct
// loop. It lives on the stack of reactLoop, never on the Agent, for the same
// reason the sink rides the context: concurrent turns must not share it.
type turnStream struct {
	// notice is the turn's fallback notice, set by the first fallback that
	// answers any call of the turn and left alone after that.
	notice string
	// noticeShown records that the notice went through the stream sink.
	noticeShown bool
	// noticeKey identifies the outage the notice describes (fallbackNoticeKey);
	// answered records that some LLM call of the turn succeeded.
	noticeKey string
	answered  bool
	// streamed records that some text delta reached the sink this turn;
	// tailNewline whether the last one ended its line.
	streamed    bool
	tailNewline bool
}

// fallbackNoticeKey identifies an outage for once-per-outage notice purposes:
// the addressed provider, plus whether the failure is a retired model
// (404/410) rather than a transient one. A cooldown following a 410 keys the
// same as the 410 — it is the same outage, deprioritized — so the user is not
// told twice. Rate limits and 5xx share the transient key.
func fallbackNoticeKey(n providers.FallbackNotice) string {
	if isRetiredModelReason(n.Reason) {
		return n.From + "|retired"
	}
	return n.From
}

// sameOutage reports whether the notice n describes the outage already
// recorded as noticed. A cooldown is the chain declining to dial a provider
// that failed earlier, so it continues whatever outage was recorded for that
// provider rather than starting a new one.
func sameOutage(noticed string, n providers.FallbackNotice) bool {
	if noticed == "" {
		return false
	}
	if noticed == fallbackNoticeKey(n) {
		return true
	}
	return n.Reason == "cooldown" && (noticed == n.From || strings.HasPrefix(noticed, n.From+"|"))
}

// isRetiredModelReason reports a not-found class on the addressed provider.
func isRetiredModelReason(reason string) bool {
	return providers.FallbackNotice{Reason: reason}.ModelRetired()
}

// formatFallbackMarker is the short form shown once the full notice has
// already been given for this outage: who answered, nothing else.
func formatFallbackMarker(n providers.FallbackNotice) string {
	return fmt.Sprintf("↪ answered by %s (%s)\n\n", n.To, n.Model)
}

// narrationSeparator returns the "\n\n" a synthesized reply needs before it
// rides the sink after streamed narration that did not end its line, and ""
// when nothing could have streamed (streaming off) or the last thing the
// model said this turn already ended with a newline. It mirrors the
// separator streamChat inserts between iterations (#339) for the replies the
// loop synthesizes itself — timeout and max-iteration — which used to be
// glued onto "Let me try one more angle:" (#347).
func narrationSeparator(streaming bool, turn []session.Message) string {
	if !streaming {
		return ""
	}
	for i := len(turn) - 1; i >= 0; i-- {
		m := turn[i]
		if m.Role != session.RoleAssistant || m.Content == "" {
			continue
		}
		if strings.HasSuffix(m.Content, "\n") {
			return ""
		}
		return "\n\n"
	}
	return ""
}

// turnMessages returns the messages appended to sess during the turn st
// describes, or nothing when the turn start is unknown.
func turnMessages(sess *session.Session, st *compactionState) []session.Message {
	if sess == nil || st == nil || st.turnStart < 0 || st.turnStart > len(sess.Messages) {
		return nil
	}
	return sess.Messages[st.turnStart:]
}

// formatFallbackNotice renders the one-line, user-facing note that a reply
// was answered by a fallback provider. Prepended to the reply rather than
// sent out of band so every channel — CLI, Telegram, Discord, HTTP API —
// carries it without per-channel wiring.
func formatFallbackNotice(n providers.FallbackNotice) string {
	hint := ""
	switch {
	case isRetiredModelReason(n.Reason):
		// A not-found on the addressed provider is not an outage: the
		// configured model is gone (hosted catalogs retire models — NVIDIA
		// answered 410 for z-ai/glm-5.2 — and a typo looks the same). The
		// chain covers the turn, but every later turn pays the same dead
		// call first, so say what to do rather than report a status code.
		hint = fmt.Sprintf(" — %s no longer serves this model; pick another with /model", n.From)
	}
	return fmt.Sprintf("⚠️ %s unavailable (%s)%s — answered by %s (%s)\n\n", n.From, n.Reason, hint, n.To, n.Model)
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

	// The extraction LLM call runs off the turn's critical path, in its own
	// goroutine with its own bounded timeout — not inline, and not on
	// context.Background() unbounded. This used to block the reply the user
	// is waiting on behind a second full LLM call that ignored
	// agents.defaults.timeout entirely: a turn configured with a short
	// timeout specifically to avoid hangs could still hang here, and the
	// trigger bar is low (>=3 tool calls plus an ordinary word like "steps"
	// in the answer is enough to fire SkillDetector.Detect in ordinary use).
	existingSkills := a.skillLoader.List()
	go a.extractAndCreateSkill(candidate.Name, trace, existingSkills)
}

// extractAndCreateSkill runs the skill-extraction LLM call and the resulting
// file write off the turn's goroutine. See afterReActDetection for why this
// must never run inline or on an unbounded context.
func (a *Agent) extractAndCreateSkill(candidateName string, trace skills.Trace, existingSkills []*skills.Skill) {
	ctx, cancel := context.WithTimeout(context.Background(), skillExtractionTimeout)
	defer cancel()

	skillContent, err := a.extractor.Extract(ctx, trace, existingSkills)
	if err != nil {
		a.logger.Warn("Skill extraction failed", "error", err)
		return
	}

	if skillContent == "" {
		a.logger.Warn("Skill extraction returned empty content")
		return
	}

	// Create the skill
	if err := a.skillLoader.Create(candidateName, skillContent); err != nil {
		a.logger.Warn("Failed to create skill", "name", candidateName, "error", err)
		return
	}

	a.logger.Info("Skill created successfully", "name", candidateName)
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
	// turnStart is the index in sess.Messages of this turn's user message.
	// Everything from it onward is the live turn and is never summarized.
	turnStart int
}

// compactionKeepTail is the number of most recent session messages held out
// of a compaction verbatim, in addition to the whole live turn. A summary
// stands in for the *earlier* conversation; the exchange the user is in the
// middle of must reach the model as the user wrote it, or the model answers a
// paraphrase of the request — "what are you referring to with 'them'?" (#346).
const compactionKeepTail = 6

// persistenceWriteTimeout bounds the fresh context given to session/history
// writes once a turn's budget has been spent.
const persistenceWriteTimeout = 10 * time.Second

// skillExtractionTimeout bounds the background skill-extraction LLM call
// afterReActDetection spawns. It is deliberately generous (a full LLM call,
// not a quick check) but finite — this used to run on context.Background()
// with no bound at all, up to the provider's own client timeout (300s for
// ollama), and inline before the turn's reply was returned.
const skillExtractionTimeout = 60 * time.Second

// persistenceCtx returns a context for persistence writes (session, history,
// checkpoint) that must complete even when the turn's budget has already been
// spent. The turn context is a deadline: once it fires, every child write
// fails instantly and the whole conversation is lost on exactly the turns that
// need saving most — a timeout, or a max-iteration turn whose last tool ran
// past the limit. When the turn context is still live it is returned
// unchanged, so a genuine cancellation still aborts the write; when it is
// already done, a fresh bounded context takes over.
func (a *Agent) persistenceCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() != nil {
		return context.WithTimeout(context.Background(), persistenceWriteTimeout)
	}
	return ctx, func() {}
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
	//
	// And never compress the live turn. The prefix ends before this turn's
	// user message and before the last compactionKeepTail messages, whichever
	// is earlier, so the request the user just made — and the tool exchange
	// answering it — reach the model verbatim. Summarizing them produced a
	// reply that asked what the user meant (#346).
	if sess == nil || len(sess.Messages) == 0 {
		return messages
	}
	prefixLen := compactionPrefixLen(sess, st)
	if prefixLen <= 0 {
		a.logger.Debug("Context over threshold but nothing before the live turn to compact",
			"session_messages", len(sess.Messages))
		return messages
	}
	if st != nil && st.active && st.prefixLen == prefixLen {
		// Already compacted this exact prefix earlier in the turn; the excess
		// is in the tail we refuse to summarize, so a second pass changes
		// nothing.
		return messages
	}
	sessionMsgs := sessionToProviderMessages(&session.Session{Messages: sess.Messages[:prefixLen]})
	prefixTokens := 0
	for _, m := range sessionMsgs {
		prefixTokens += ctxpkg.TokenEstimator(m.Content)
	}
	if prefixTokens < thresholdBudget/4 {
		// The prefix is not what is over budget — the protected tail is
		// (one huge tool result, say). Summarizing a few hundred tokens of
		// prefix spends an LLM call to recover nothing.
		a.logger.Debug("Context over threshold but the compactable prefix is small; skipping",
			"prefix_tokens", prefixTokens, "threshold_budget", thresholdBudget)
		return messages
	}
	compressed, err := a.compressor.CompressMessages(ctx, model, sessionMsgs, thresholdBudget)
	if err != nil {
		a.logger.Warn("Context compaction failed", "error", err)
		return messages
	}

	// The tail is taken from `messages`, not rebuilt from the session, so this
	// turn's image and document bytes (which live only on the request) survive.
	// The loop appends to both in step, so the last tailLen entries line up.
	tailLen := len(sess.Messages) - prefixLen
	var tail []providers.Message
	if len(messages)-1 >= tailLen {
		tail = messages[len(messages)-tailLen:]
	} else {
		tail = sessionToProviderMessages(&session.Session{Messages: sess.Messages[prefixLen:]})
	}

	// Return new message list with compressed content
	newMessages := make([]providers.Message, 0, 2+len(tail))
	newMessages = append(newMessages,
		messages[0], // Keep system message
		providers.Message{
			Role:    providers.RoleUser,
			Content: session.CompactionEnvelope(compressed),
		},
	)
	newMessages = append(newMessages, dropOrphanedToolResults(tail)...)

	// Record it for the write-back at the end of the turn. A later compaction in
	// the same turn overwrites this: its summary already subsumes the earlier
	// one (the compressed text is carried forward in `messages`), and its
	// prefixLen covers strictly more of the session.
	if st != nil {
		st.summary = compressed
		st.prefixLen = prefixLen
		st.active = true
	}

	a.logger.Debug("Context compacted", "original_messages", len(sessionMsgs), "new_content_len", len(compressed))
	return newMessages
}

// compactionPrefixLen returns how many leading session messages a compaction
// may summarize: everything before the live turn and before the last
// compactionKeepTail messages, backed up so the kept tail never opens with a
// tool result whose announcing assistant message was summarized away. It
// returns 0 when there is nothing worth summarizing — a prefix that is only an
// earlier compaction record would just be re-summarized into itself.
func compactionPrefixLen(sess *session.Session, st *compactionState) int {
	n := len(sess.Messages)
	// The tail is anchored to the start of the live turn, not to the end of
	// the session: the turn grows with every tool iteration, and an anchor
	// at the end would move the prefix boundary on every pass and defeat the
	// "already compacted this prefix" check in checkAndCompactContext.
	keepFrom := n - compactionKeepTail
	if st != nil && st.turnStart >= 0 && st.turnStart <= n {
		keepFrom = st.turnStart - compactionKeepTail
	}
	for keepFrom > 0 && sess.Messages[keepFrom].Role == session.RoleTool {
		keepFrom--
	}
	if keepFrom <= 0 {
		return 0
	}
	if keepFrom == 1 && sess.Messages[0].Compaction {
		return 0
	}
	return keepFrom
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
		// /start and /help are the same answer on every channel: a user who
		// sends either wants to know what the bot can do.
		return commands.HelpText()
	case "new":
		// Clear messages but preserve conversation context (user facts survive
		// /new). The per-session model override and personality are scoped to
		// the conversation, so they are cleared too.
		sessionKey := getSessionKey(msg)
		if resetter, ok := a.sessions.(SessionResetter); ok {
			if _, err := resetter.ResetConversation(ctx, sessionKey); err != nil {
				a.logger.Warn("Could not reset session for /new", "session", sessionKey, "error", err)
			}
		} else if sess, err := a.sessions.GetOrCreate(ctx, sessionKey); err == nil {
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
		return commands.HelpText()
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
	// The conversation is kept, deliberately: a switch mid-task is usually
	// "try a smarter model on this", and a button that silently wiped the
	// transcript would be a destructive action one tap away. The trade-off
	// (a transcript answered by two models) is named so the user can pick.
	return fmt.Sprintf("✓ Model switched to %s for this session.\n\nThe conversation continues with the new model; send /new to start fresh. Use /model %s --global to make it the default for all sessions.", canonical, canonical)
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
	// Strip question words for cleaner topic. The match is on the lowercase
	// copy but the strip is applied to the original by the prefix's own
	// (ASCII) byte length: strings.ToLower can change a string's byte length
	// (İ is 2 bytes, its lowercase 3), so an offset taken from the lowered
	// copy does not index the original.
	cleaned := content
	for _, prefix := range []string{"what's ", "what are ", "what ", "tell me about ", "tell me "} {
		if strings.HasPrefix(lower, prefix) {
			cleaned = cleaned[len(prefix):]
			lower = lower[len(prefix):]
		}
	}
	cleaned = strings.TrimSpace(cleaned)
	// Cut on a word boundary with no ellipsis. A mid-word cut with "..."
	// appended — "wanting to do a new topi..." — read to the model as a user
	// message that broke off, and it answered the "cut-off" message instead
	// of the real one. A single long word is cut hard, backed off to a rune
	// start so the hint is never invalid UTF-8.
	if len(cleaned) > topicMaxLen {
		cut := strings.LastIndex(cleaned[:topicMaxLen], " ")
		if cut < topicMaxLen/2 {
			cut = topicMaxLen
			for cut > 0 && !utf8.RuneStart(cleaned[cut]) {
				cut--
			}
		}
		cleaned = strings.TrimRight(cleaned[:cut], " ,;:-")
	}
	return cleaned
}

// topicMaxLen bounds the auto-derived topic hint.
const topicMaxLen = 60

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
