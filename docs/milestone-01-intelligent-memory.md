# Milestone 1: Intelligent Cross-Session Memory

## Why This Matters

Memory is the #1 most requested feature across every open-source AI assistant. The
difference between "a chatbot that answers questions" and "a personal assistant that
remembers" is cross-session memory. joshbot already has flat MEMORY.md and HISTORY.md
files. This milestone makes them intelligent.

**Market position:** Every competitor goes either too simple (flat dumps) or too
complex (vector DBs, cognitive architectures). joshbot's markdown-based approach
is already aligned with the emerging "files as memory" pattern (Acontext, Fera).
This milestone fills the gap: lightweight, user-editable, structured enough to be
useful.

## Current State

- `internal/memory/`: Thread-safe file manager (MEMORY.md + HISTORY.md), atomic
  writes via temp+rename, append-only history
- `internal/learning/`: Consolidator summarizes last N history lines into MEMORY.md
  every 10 min via LLM call (or heuristic fallback). Deduplication by exact string
  match. Max 20 facts.
- Memory is injected as raw text into system prompt via `loadIdentityFiles` in
  `internal/agent/context.go`
- Agent already has core identity prompt telling it to "remember important
  information by updating your memory files"

## Design

### 1. Structured Fact Model

Replace flat "## Consolidated Facts" section with categorized facts:

```go
type Fact struct {
    ID        string    // hash(content + category) for dedup
    Category  string    // "user_info" | "preference" | "project" | "decision" | "skill" | "system"
    Content   string    // the fact itself ("User prefers dark mode")
    Tags      []string  // e.g. ["work", "frontend"]
    Source    string    // session ID or "user" (if manually written)
    CreatedAt time.Time
    UpdatedAt time.Time
    AccessCount int     // how often referenced (for eviction)
    Confidence  float64 // 0.0-1.0 (inferred from repetition across sessions)
}
```

Storage remains markdown for user editability:

```markdown
# Long-Term Memory

## user_info
- [2026-05-17] User prefers dark mode (confidence: 0.9, tags: work, frontend)
- [2026-05-16] User lives in SF, works at a startup (confidence: 1.0)

## preference
- [2026-05-17] Prefers concise answers, no small talk (confidence: 0.8)

## project
- [2026-05-15] Working on joshbot milestone memory system (confidence: 1.0)

## decision
- [2026-05-14] Decided to use SQLite for storage backend (confidence: 0.9)
```

### 2. Fact Extraction (smarter consolidation)

Enhance `Consolidator.RunOnce()` to emit structured facts instead of raw text:

- LLM prompt: "Extract factual statements from this conversation. Classify each
  into one of: user_info, preference, project, decision, skill, system. Return
  only new/updated facts. Skip greetings, small talk, and transient state."
- Confidence scoring:
  - 1.0: User explicitly stated it ("I live in SF")
  - 0.8: Inferred from multiple statements across sessions
  - 0.6: Inferred from single conversation
  - 0.3: Weak inference, needs confirmation
- Deduplication: by semantic similarity (not exact match), keeping the version
  with higher confidence

### 3. Cross-Session Recall

Add a `MemorySearch` tool that the agent can invoke to query past facts:

```go
type MemorySearch struct {
    Query    string   // free-text search
    Category string   // filter by category (optional)
    Tags     []string // filter by tags (optional)
    Max      int      // max results (default 5)
}
```

Implementation: simple keyword matching across MEMORY.md + HISTORY.md.
The agent is prompted to use this tool when the user references past
conversations ("remember when we talked about...").

This replaces the current blunt approach of dumping all memory into every prompt.
Memory injection becomes smart: relevant facts are injected, not everything.

### 4. Memory Budget & Relevance Scoring

Not all facts fit in every prompt. Use a scoring function:

```go
func scoreRelevance(fact Fact, context string) float64 {
    // Factors:
    // - recency: newer facts score higher
    // - confidence: higher confidence scores higher
    // - access count: frequently referenced facts score higher
    // - keyword overlap with current conversation
    // - category match with current task
}
```

Inject top-N facts where N is configurable (default 15) and computed as
`min(tokenBudget/maxFactTokens, maxFacts)`. This prevents prompt bloat
and keeps the agent focused on what's relevant.

### 5. User Metadata Management

Add structured tracking for user metadata that changes slowly:

```go
type UserMetadata struct {
    Name        string
    Preferences map[string]string // "response_style" -> "concise"
    Patterns    map[string]string // "work_hours" -> "9-5 PST"
    Context     map[string]string // "current_project" -> "joshbot memory system"
}
```

Stored as structured YAML frontmatter in USER.md (already exists in workspace
identity files). The agent can update specific fields via a tool instead of
wrestling with raw markdown.

## Files to Create/Modify

### New files

| File | Purpose |
|------|---------|
| `internal/memory/fact.go` | `Fact` struct, serialization, dedup |
| `internal/memory/search.go` | `MemorySearch` + relevance scoring |
| `internal/memory/metadata.go` | `UserMetadata` + tool wrapper |
| `internal/tools/memory_tool.go` | Tool exposing memory search/update to the agent |

### Modified files

| File | Change |
|------|--------|
| `internal/learning/learning.go` | Fact extraction via LLM, structured output parsing |
| `internal/memory/memory.go` | Categorized section management, confidence tracking |
| `internal/agent/context.go` | Smart memory injection (scored, not dumped) |
| `internal/agent/agent.go` | Wire new memory tools into agent's registry |
| `internal/tools/registry.go` | Register `MemorySearch` tool |

## Interfaces

```go
// internal/memory/fact.go
package memory

type FactCategory string
const (
    FactUserInfo   FactCategory = "user_info"
    FactPreference FactCategory = "preference"
    FactProject    FactCategory = "project"
    FactDecision   FactCategory = "decision"
    FactSkill      FactCategory = "skill"
    FactSystem     FactCategory = "system"
)

type Fact struct {
    ID         string       `json:"id"`
    Category   FactCategory `json:"category"`
    Content    string       `json:"content"`
    Tags       []string     `json:"tags,omitempty"`
    Source     string       `json:"source"`
    Confidence float64      `json:"confidence"`
    CreatedAt  time.Time    `json:"created_at"`
    UpdatedAt  time.Time    `json:"updated_at"`
}

// internal/memory/search.go
type SearchQuery struct {
    Text     string
    Category FactCategory // "" for all
    Tags     []string     // optional filter
    Max      int          // max results
}

type SearchResult struct {
    Fact     Fact
    Score    float64 // relevance 0.0-1.0
}

// Manager gets new methods:
func (m *Manager) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error)
func (m *Manager) WriteFact(ctx context.Context, f Fact) error
func (m *Manager) ReconcileFacts(ctx context.Context, facts []Fact) error // upsert + dedup + evict
```

## Test Plan

### Unit tests

| Test | What it covers |
|------|----------------|
| `TestFactDedup_ExactMatch` | Same content returns same ID |
| `TestFactDedup_SemanticMatch` | "likes dark mode" vs "prefers dark theme" → same fact |
| `TestFactConfidence_Merge` | Two sessions with same fact → confidence increases |
| `TestFactConfidence_Overwrite` | User corrects a fact → new version with old confidence |
| `TestSearch_ByCategory` | Category filters work correctly |
| `TestSearch_ByTags` | Tag filters work correctly |
| `TestSearch_RelevanceRanking` | Results sorted by score descending |
| `TestSearch_EmptyQuery` | Returns recent facts |
| `TestReconcileFacts_Dedup` | Merge duplicates, keep highest confidence |
| `TestReconcileFacts_Eviction` | Exceeds maxFacts → evict lowest confidence + oldest |
| `TestReconcileFacts_Update` | Same fact, new content → update timestamp |
| `TestMarkdownRoundtrip` | Fact → markdown → Fact preserves all fields |
| `TestLoadMemory_WithCategories` | Parse categorized memory file |
| `TestMemorySearchTool_Execute` | Tool integration test |
| `TestRelevanceScore_Recency` | Newer facts score higher |
| `TestRelevanceScore_Confidence` | Higher confidence scores higher |

### Edge cases

- Empty memory file → returns empty, no crash
- Fact with no tags → tags slice is nil, not empty
- Confidence > 1.0 → clamped to 1.0
- Confidence < 0.0 → clamped to 0.0
- Concurrent fact writes from agent + consolidator → last writer wins, no corruption
- Search with 0 results → empty slice, not nil
- Memory file > 10MB → graceful degradation (truncation warning)
- Unicode in fact content (Chinese, emoji) → preserved

### Integration tests

- Consolidator extracts facts → facts appear in memory → agent references them
- Agent updates USER.md via tool → next session picks up changes

## Effort

| Component | CC time | Human time (equiv) |
|-----------|---------|-------------------|
| Fact struct + serialization | 10 min | 4 hours |
| Category-based markdown I/O | 15 min | 6 hours |
| Fact extraction prompt + parsing | 20 min | 1 day |
| Search implementation | 15 min | 6 hours |
| Relevance scoring | 10 min | 4 hours |
| MemorySearch tool | 10 min | 3 hours |
| Agent context injection changes | 10 min | 3 hours |
| Tests (17 unit + 2 integration) | 30 min | 1.5 days |
| **Total** | **~2 hours** | **~5 days** |

## Success Criteria

- Agent can recall user facts from previous sessions when asked
- Agent can search and find specific past decisions
- Consolidator extracts structured facts (not raw text) from conversations
- User can read and edit memory files directly (transparency preserved)
- Memory injection stays under token budget, never bloats prompts
- All tests pass with `go test -race -count=1`
- Coverage on `internal/memory/` >= 80%, `internal/learning/` >= 75%
