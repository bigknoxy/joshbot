package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
)

// DefaultMaxIterations is the default max iterations for a subagent ReAct loop.
const DefaultMaxIterations = 20

// DefaultMaxTokens is the default max output tokens for a subagent.
const DefaultMaxTokens = 4096

// DefaultTemperature is the default temperature for a subagent.
const DefaultTemperature = 0.3

// DefaultTimeout is the default timeout for a subagent run.
const DefaultTimeout = 60 * time.Second

// DefaultMaxDepth is the default maximum nesting depth for subagent
// delegation. A depth of 0 means the top-level subagent; each delegate_subagent
// call descends one level, and a call that would exceed the limit is refused.
const DefaultMaxDepth = 2

// depthKey is the context key carrying the current subagent nesting depth.
type depthKey struct{}

// WithDepth returns a context carrying the current subagent nesting depth.
func WithDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, depthKey{}, depth)
}

// DepthFromContext returns the subagent nesting depth carried on the context,
// or 0 when none is set (a top-level subagent).
func DepthFromContext(ctx context.Context) int {
	if d, ok := ctx.Value(depthKey{}).(int); ok {
		return d
	}
	return 0
}

// roleKey is the context key carrying the current subagent role.
type roleKey struct{}

// WithRole returns a context carrying the role of the currently-running
// subagent. The top-level agent (not a subagent) has no role set, so
// RoleFromContext returns RoleLeaf (the zero value) for it.
func WithRole(ctx context.Context, role Role) context.Context {
	return context.WithValue(ctx, roleKey{}, role)
}

// RoleFromContext returns the subagent role carried on the context, or
// RoleLeaf when none is set (the top-level agent, or a caller that did not
// record a role).
func RoleFromContext(ctx context.Context) Role {
	if r, ok := ctx.Value(roleKey{}).(Role); ok {
		return r
	}
	return RoleLeaf
}

// Role controls what a subagent can do and whether it can spawn further subagents.
type Role int

const (
	// RoleLeaf is a restricted subagent: it can execute tools but cannot
	// spawn further subagents. Ideal for sandboxed, self-contained tasks.
	RoleLeaf Role = iota
	// RoleOrchestrator is a full-capability subagent: it can spawn and
	// delegate to child subagents, enabling hierarchical multi-agent workflows.
	RoleOrchestrator
)

// ToolResult is the result of a tool execution inside a subagent.
// It mirrors tools.ToolResult to avoid an import cycle.
type ToolResult struct {
	Output string
	Error  error
}

// AsyncResult is delivered to the async callback when a tool completes asynchronously.
// It mirrors tools.AsyncResult to avoid an import cycle.
// When tools.AsyncResult gains new fields, they MUST be added here too — the
// TestFieldParity test enforces this.
type AsyncResult struct {
	ToolName string
	Args     map[string]any
	Output   string
	Error    error
	Metadata map[string]any
	Channel  string
	ChatID   string
}

// ToolExecutor is a minimal interface for tool execution inside a subagent.
// The main agent's tools.Registry satisfies this interface via the
// ToolExecutorAdapter wrapper.
type ToolExecutor interface {
	GetSchemas() []providers.Tool
	ExecuteWithContext(ctx context.Context, name string, args map[string]any, channel, channelID string, callback func(AsyncResult)) (ToolResult, bool)
}

// ProgressEvent represents a single progress event from a subagent.
type ProgressEvent struct {
	Phase   ProgressPhase
	Tool    string
	Summary string
	Elapsed time.Duration
	Err     error
}

// ProgressPhase identifies whether a ProgressEvent marks the start or completion of a tool call.
type ProgressPhase int

const (
	// ToolProgressStart is emitted immediately before a tool call is executed.
	ToolProgressStart ProgressPhase = iota
	// ToolProgressDone is emitted when a tool call completes.
	ToolProgressDone
)

// ProgressFunc receives progress events as the subagent runs.
type ProgressFunc func(event ProgressEvent)

// Config holds all tunable parameters for a subagent run.
type Config struct {
	Model       string
	MaxTokens   int
	Temperature float64
	MaxIter     int
	Timeout     time.Duration
	Role        Role
	// MaxDepth is the maximum nesting depth this subagent may delegate to.
	// 0 means the DefaultMaxDepth. A delegate_subagent call that would exceed
	// it is refused.
	MaxDepth int
}

// StreamSink receives streaming text deltas and tool progress events from a subagent.
type StreamSink interface {
	OnTextDelta(content string)
	OnToolStart(tool string, args map[string]any)
	OnToolDone(tool string, result ToolResult, elapsed time.Duration)
}

// SubResult is the result of a subagent run.
type SubResult struct {
	Output     string
	TokenUsage providers.Usage
	Iterations int
	TimedOut   bool
	Truncated  bool
}

// Runner runs a short-lived, isolated subagent for focused tasks.
//
// A subagent gets its own isolated provider session, its own message list,
// and (optionally) access to the same tool set as the parent agent. It runs
// a bounded ReAct loop: the model is called repeatedly, tools are executed,
// and the loop stops when the model returns no tool calls, hits the iteration
// limit, or the context/timeout is exceeded.
//
// Isolation guarantees:
//   - Each Run creates a fresh message list — no session state leaks between runs.
//   - The provider and tool registry are injected, so the subagent uses the same
//     backend as the parent but with independent execution context.
//   - Leaf subagents cannot spawn child subagents; only orchestrators can.
type Runner struct {
	provider     providers.Provider
	tools        ToolExecutor
	streaming    bool
	streamSink   StreamSink
	logger       *log.Logger
	defaultModel string
	maxIter      int
	maxTokens    int
	temperature  float64
	timeout      time.Duration
	maxDepth     int
}

// OptFunc configures a Runner.
type OptFunc func(*Runner)

// WithTools injects a tool executor so the subagent can call tools.
func WithTools(t ToolExecutor) OptFunc {
	return func(r *Runner) {
		if t != nil {
			r.tools = t
		}
	}
}

// WithStreaming enables streaming output via the provided sink.
func WithStreaming(sink StreamSink) OptFunc {
	return func(r *Runner) {
		if sink != nil {
			r.streaming = true
			r.streamSink = sink
		}
	}
}

// WithLogger injects a logger.
func WithLogger(l *log.Logger) OptFunc {
	return func(r *Runner) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithMaxTokens sets the default max output tokens for subagent responses.
func WithMaxTokens(n int) OptFunc {
	return func(r *Runner) {
		if n > 0 {
			r.maxTokens = n
		}
	}
}

// WithTemperature sets the default sampling temperature for subagent responses.
func WithTemperature(t float64) OptFunc {
	return func(r *Runner) {
		if t > 0 {
			r.temperature = t
		}
	}
}

// WithMaxIter sets the default max iterations for subagent ReAct loops.
func WithMaxIter(n int) OptFunc {
	return func(r *Runner) {
		if n > 0 {
			r.maxIter = n
		}
	}
}

// WithTimeout sets the default per-run timeout for subagent execution.
func WithTimeout(d time.Duration) OptFunc {
	return func(r *Runner) {
		if d > 0 {
			r.timeout = d
		}
	}
}

// WithMaxDepth sets the default maximum nesting depth for subagent delegation.
func WithMaxDepth(n int) OptFunc {
	return func(r *Runner) {
		if n > 0 {
			r.maxDepth = n
		}
	}
}

// firstNonZero returns the first non-zero int from the given values.
func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// firstNonZeroFloat returns the first non-zero float64 from the given values.
func firstNonZeroFloat(vals ...float64) float64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// firstNonZeroDur returns the first non-zero time.Duration from the given values.
func firstNonZeroDur(vals ...time.Duration) time.Duration {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// firstNonZeroStr returns the first non-empty string from the given values.
func firstNonZeroStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// NewRunner creates a Runner with the given provider and default model.
// The subagent starts with no tools; use WithTools to enable tool access.
func NewRunner(provider providers.Provider, model string, opts ...OptFunc) *Runner {
	r := &Runner{
		provider:     provider,
		defaultModel: model,
		logger:       log.Get(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run executes a subagent task synchronously, returning the final output.
// This is the simple path — no streaming, no progress callbacks.
// The context controls the overall timeout.
func (r *Runner) Run(ctx context.Context, prompt string, cfg Config) (*SubResult, error) {
	return r.RunWithProgress(ctx, prompt, cfg, nil)
}

// SimpleRun executes a subagent with default config, satisfying the
// tools.SubagentRunner interface: Run(ctx, prompt) → (string, error).
func (r *Runner) SimpleRun(ctx context.Context, prompt string) (string, error) {
	res, err := r.Run(ctx, prompt, Config{})
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return res.Output, nil
}

// RunWithProgress is like Run but delivers progress events to the provided
// ProgressFunc (which may be nil).
func (r *Runner) RunWithProgress(ctx context.Context, prompt string, cfg Config, onProgress ProgressFunc) (*SubResult, error) {
	return r.RunWithCallback(ctx, prompt, cfg, nil, onProgress)
}

// RunWithCallback runs a subagent with an async callback handler for tools
// that support async execution. The callback is invoked for each async tool
// result; if nil, async tools run synchronously.
func (r *Runner) RunWithCallback(ctx context.Context, prompt string, cfg Config, asyncCallback func(AsyncResult), onProgress ProgressFunc) (*SubResult, error) {
	if r.provider == nil {
		return nil, errors.New("no provider configured")
	}

	// Apply defaults: Runner-level overrides > global defaults.
	// Config-level values (when > 0) win over everything.
	cfg.MaxIter = firstNonZero(cfg.MaxIter, r.maxIter, DefaultMaxIterations)
	cfg.MaxTokens = firstNonZero(cfg.MaxTokens, r.maxTokens, DefaultMaxTokens)
	cfg.Temperature = firstNonZeroFloat(cfg.Temperature, r.temperature, DefaultTemperature)
	cfg.Timeout = firstNonZeroDur(cfg.Timeout, r.timeout, DefaultTimeout)
	cfg.MaxDepth = firstNonZero(cfg.MaxDepth, r.maxDepth, DefaultMaxDepth)
	model := firstNonZeroStr(cfg.Model, r.defaultModel)

	// Enforce the nesting-depth limit. The current depth rides the context
	// (set by delegate_subagent); a call that would exceed the configured max
	// is refused rather than spawning an unbounded chain of subagents.
	if depth := DepthFromContext(ctx); depth > cfg.MaxDepth {
		return nil, fmt.Errorf("subagent nesting depth %d exceeds the maximum of %d", depth, cfg.MaxDepth)
	}

	// Build the isolated system prompt based on role.
	sysPrompt := r.buildSystemPrompt(cfg.Role, cfg.MaxIter, cfg.MaxDepth)

	messages := []providers.Message{
		{Role: providers.RoleSystem, Content: sysPrompt},
		{Role: providers.RoleUser, Content: prompt},
	}

	// Apply timeout from config (in addition to any caller-provided context),
	// and carry the role on the context so a nested delegate_subagent call can
	// enforce the "leaf cannot spawn" contract at runtime.
	runCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	runCtx = WithRole(runCtx, cfg.Role)

	var totalUsage providers.Usage
	var iterations int
	var timedOut bool

	// Get tool schemas once — they don't change mid-run. The subagent-spawning
	// tools are only advertised to orchestrators: a leaf is not offered them, so
	// it cannot spawn children even if it ignores its system prompt.
	var toolSchemas []providers.Tool
	if r.tools != nil {
		toolSchemas = filterSchemasByRole(r.tools.GetSchemas(), cfg.Role)
	}

	for i := 0; i < cfg.MaxIter; i++ {
		iterations = i + 1

		if runCtx.Err() != nil {
			timedOut = true
			break
		}

		req := providers.ChatRequest{
			Model:       model,
			MaxTokens:   cfg.MaxTokens,
			Temperature: cfg.Temperature,
			Messages:    messages,
			Tools:       toolSchemas,
			Stream:      r.streaming,
		}

		var resp *providers.ChatResponse
		var err error

		if r.streaming && r.streamSink != nil {
			resp, err = r.streamChat(runCtx, req)
		} else {
			resp, err = r.provider.Chat(runCtx, req)
		}

		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				timedOut = true
				break
			}
			return nil, fmt.Errorf("subagent LLM call failed: %w", err)
		}

		if len(resp.Choices) == 0 {
			break
		}

		// Accumulate token usage
		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens

		choice := resp.Choices[0]
		assistantMsg := choice.Message

		// Report streaming text delta
		if r.streaming && r.streamSink != nil && assistantMsg.Content != "" {
			r.streamSink.OnTextDelta(assistantMsg.Content)
		}

		// If no tool calls, we're done.
		if len(assistantMsg.ToolCalls) == 0 {
			content := assistantMsg.Content
			if content == "" {
				content = "Subagent completed with no output."
			}
			return &SubResult{
				Output:     content,
				TokenUsage: totalUsage,
				Iterations: iterations,
				TimedOut:   timedOut,
			}, nil
		}

		// Add assistant message to the conversation
		messages = append(messages, assistantMsg)

		// Execute tool calls
		for _, tc := range assistantMsg.ToolCalls {
			if onProgress != nil {
				onProgress(ProgressEvent{
					Phase: ToolProgressStart,
					Tool:  tc.Function.Name,
				})
			}

			// Parse arguments
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				if onProgress != nil {
					onProgress(ProgressEvent{
						Phase: ToolProgressDone,
						Tool:  tc.Function.Name,
						Err:   err,
					})
				}
				resultStr := fmt.Sprintf("Error: Invalid arguments: %v", err)
				toolMsg := providers.Message{
					Role:       providers.RoleTool,
					Content:    resultStr,
					ToolCallID: tc.ID,
				}
				messages = append(messages, toolMsg)
				continue
			}

			if r.streaming && r.streamSink != nil {
				r.streamSink.OnToolStart(tc.Function.Name, args)
			}

			// Execute the tool. Async tools return a "started in background"
			// placeholder immediately and deliver the real result through the
			// callback. The subagent must not feed the placeholder to the model
			// as if it were the final answer, so when a tool is async we wait
			// for the callback to deliver the real result.
			toolStart := time.Now()
			asyncCh := make(chan AsyncResult, 1)
			result, isAsync := r.tools.ExecuteWithContext(runCtx, tc.Function.Name, args, "subagent", "", func(ar AsyncResult) {
				if asyncCallback != nil {
					asyncCallback(ar)
				}
				select {
				case asyncCh <- ar:
				default:
				}
			})
			if isAsync {
				select {
				case ar := <-asyncCh:
					result = ToolResult{Output: ar.Output, Error: ar.Error}
				case <-runCtx.Done():
					result = ToolResult{Error: runCtx.Err()}
				}
			}
			elapsed := time.Since(toolStart)

			// Build tool result message
			var resultStr string
			if result.Error != nil {
				resultStr = fmt.Sprintf("Error: %v", result.Error)
			} else {
				resultStr = result.Output
			}
			toolMsg := providers.Message{
				Role:       providers.RoleTool,
				Content:    resultStr,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolMsg)

			// Report progress
			if onProgress != nil {
				onProgress(ProgressEvent{
					Phase:   ToolProgressDone,
					Tool:    tc.Function.Name,
					Summary: summarizeArgs(args),
					Elapsed: elapsed,
					Err:     result.Error,
				})
			}
			if r.streaming && r.streamSink != nil {
				r.streamSink.OnToolDone(tc.Function.Name, result, elapsed)
			}
		}
	}

	// If we exited the loop without a clean finish, return what we have.
	lastMsg := ""
	if len(messages) > 0 {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == providers.RoleAssistant {
				lastMsg = messages[i].Content
				break
			}
		}
	}

	if timedOut {
		lastMsg += fmt.Sprintf("\n\n[Subagent timed out or hit iteration limit (%d)]", cfg.MaxIter)
	}

	return &SubResult{
		Output:     lastMsg,
		TokenUsage: totalUsage,
		Iterations: iterations,
		TimedOut:   timedOut,
	}, nil
}

// RunLeaf is a convenience method for running a leaf-role subagent.
func (r *Runner) RunLeaf(ctx context.Context, prompt string, cfg Config, onProgress ProgressFunc) (*SubResult, error) {
	cfg.Role = RoleLeaf
	return r.RunWithProgress(ctx, prompt, cfg, onProgress)
}

// RunOrchestrator is a convenience method for running an orchestrator subagent.
func (r *Runner) RunOrchestrator(ctx context.Context, prompt string, cfg Config, onProgress ProgressFunc) (*SubResult, error) {
	cfg.Role = RoleOrchestrator
	return r.RunWithProgress(ctx, prompt, cfg, onProgress)
}

// buildSystemPrompt constructs the system prompt based on the subagent's role.
func (r *Runner) buildSystemPrompt(role Role, maxIter, maxDepth int) string {
	var roleDesc string
	var constraints string

	switch role {
	case RoleLeaf:
		roleDesc = "restricted leaf subagent"
		constraints = "You cannot spawn subagents. Complete the task directly with available tools."
	case RoleOrchestrator:
		roleDesc = "orchestrator subagent"
		constraints = fmt.Sprintf("You can delegate sub-tasks to child subagents via the delegate_subagent tool, up to a nesting depth of %d. Combine their results.", maxDepth)
	}

	return fmt.Sprintf(`You are an isolated %s running in a separate sandboxed context.

Your task: execute the user's instruction precisely and return only the final result. You will not be able to ask clarifying questions — make reasonable assumptions and proceed.

Constraints:
- %s
- You have a maximum of %d reasoning iterations.
- Be concise but thorough. If the task is complex, break it into sub-steps.
- Your entire context is isolated — there is no shared state with the parent agent unless you use tools.
- Report your final answer as plain text.`, roleDesc, constraints, maxIter)
}

// streamChat performs a streaming chat request and returns the accumulated response.
func (r *Runner) streamChat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	stream, err := r.provider.ChatStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return providers.AccumulateStream(stream)
}

// summarizeArgs produces a short summary string for tool arguments.
func summarizeArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}

// spawnToolNames are the tools that can spawn or fan out further subagents.
// They are filtered out of a leaf subagent's schema so a leaf cannot delegate
// or fan out even if it ignores its system prompt.
var spawnToolNames = map[string]bool{
	"delegate_subagent": true,
	"parallel_subagent": true,
	"chain_execution":   true,
	"subagent_profile":  true,
}

// filterSchemasByRole returns the tool schemas appropriate for the given role.
// Orchestrators (and the top-level agent, whose role is the zero value RoleLeaf
// but which is not a subagent) keep every tool; a leaf subagent loses the
// subagent-spawning tools.
func filterSchemasByRole(schemas []providers.Tool, role Role) []providers.Tool {
	if role != RoleLeaf || len(schemas) == 0 {
		return schemas
	}
	out := make([]providers.Tool, 0, len(schemas))
	for _, t := range schemas {
		if spawnToolNames[t.Function.Name] {
			continue
		}
		out = append(out, t)
	}
	return out
}
