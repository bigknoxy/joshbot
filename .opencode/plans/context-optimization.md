# Plan: Context Management & Tool Output Optimization

## Goal
Make joshbot work reliably on small local CPU models (4k-8k context) by:
1. Using conservative context defaults
2. Proactively compacting context during ReAct loops
3. Capping tool output sizes
4. Improving system prompt for efficient tool selection

## Root Cause Analysis
User ran: `joshbot agent -m "do the kansas city royals play today?"`
Result: 6 tool calls → 133k tokens → overflow error (model limit: 131k)

Problems identified:
1. **Model not in registry**: `arcee-ai/trinity-large-preview:free` has 131k context, but registry defaults to 8192 for unknown models → budget calculation completely wrong
2. **No proactive compaction**: Compression only at message build time, not during ReAct loop
3. **Shell output uncapped**: Full stdout/stderr returned, no limit
4. **Inefficient tool selection**: LLM chose web_search → shell curl/grep repeatedly instead of single web_fetch

## Acceptance Criteria
- Unknown models default to 4096 token context (safe for small models)
- Context compaction triggers at 70% budget during ReAct loop
- Shell output capped at 4000 chars
- Web tool output capped at 5000 chars
- System prompt guides efficient tool selection (prefer built-in tools over shell)
- No context overflow errors in normal usage
- All existing tests pass

---

## Implementation Plan

### Phase 1: Conservative Context Defaults
**File: `internal/context/context.go`**
- [ ] Change default from 8192 → 4096 tokens for unknown models
- [ ] Add heuristic detection for large models by name pattern:
  - Models with "large", "128k", "200k" in name → 131072 tokens
  - Models with "32k" in name → 32768 tokens
  - Models with "16k" in name → 16384 tokens
- [ ] Add `arcee-ai/trinity-large-preview` to registry with 131072 tokens

### Phase 2: Proactive Context Compaction
**File: `internal/agent/agent.go`**
- [ ] Add `checkAndCompactContext()` method called after each tool execution
- [ ] Compaction threshold: 70% of budget
- [ ] Log when compaction occurs (for debugging)

**File: `internal/config/config.go`**
- [ ] Add `CompactionThreshold float64` to AgentDefaults (default 0.7)

### Phase 3: Tool Output Capping
**File: `internal/tools/shell.go`**
- [ ] Add `maxOutputChars` field (default 4000)
- [ ] Truncate stdout/stderr with "... (truncated, N chars total)" suffix

**File: `internal/tools/web.go`**
- [ ] Reduce web_fetch truncation from 10000 → 5000 chars
- [ ] Reduce web_search truncation from 10000 → 5000 chars
- [ ] Reduce exaCLICrawl truncation from 15000 → 5000 chars

**File: `internal/tools/filesystem.go`**
- [ ] Add max chars limit to read_file (default 8000 chars)

**File: `internal/config/config.go`**
- [ ] Add `ToolOutputMaxChars int` to ToolsConfig (default 4000)

### Phase 4: Improve System Prompt for Tool Selection
**File: `internal/agent/context.go`**
- [ ] Add tool selection guidance to system prompt:
  - Prefer built-in tools (web, filesystem) over shell commands
  - Use web_fetch for single URLs, web_search for finding information
  - Avoid repeated tool calls - plan ahead and batch operations
  - Shell commands are not context-aware

**File: `internal/tools/web.go`** (tool descriptions)
- [ ] Update Description() to mention exa-cli preference and efficiency

**File: `internal/tools/shell.go`** (tool descriptions)
- [ ] Update Description() to warn about context usage

### Phase 5: Remove/Reduce Reflection Prompts
**File: `internal/agent/agent.go`**
- [ ] Remove or shorten reflection prompts (saves ~100 tokens/iteration)

---

## Verification
1. Unit tests: `go test ./...`
2. Manual test: `joshbot agent -m "do the kansas city royals play today?"`
3. Verify no context overflow error
4. Check logs for compaction triggers

## Open Questions for User
1. **Reflection prompts**: Remove entirely or keep shortened version?
2. **Compaction strategy**: Truncate old messages (cheap) or LLM summarization (expensive)?
3. **Tool output caps**: Per-tool configurable or global setting?
