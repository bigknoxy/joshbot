# Milestone 2: Skill Self-Creation from Conversations

## Why This Matters

The #2 most-praised feature in the AI assistant space (after memory) is closed-loop
skill creation: the assistant teaches itself new abilities by observing how it solves
problems. Only Hermes Agent and SwarmAI have it today, and both are 100K+ LOC
Python/TypeScript monoliths. joshbot at ~16K LOC Go can own this in the lightweight
category.

**Market position:** joshbot already has skill discovery (`internal/skills/`) and
the agent prompt telling it to "consider creating a skill when you learn something
new." What's missing is the automated pipeline: detect a repeatable pattern,
extract the procedure, generate a SKILL.md, register it. This milestone closes
that loop.

## Current State

- `internal/skills/`: Auto-discovers SKILL.md files from `workspace/skills/{name}/`
  with YAML frontmatter parsing. Supports requirements (`bin:`, `env:`), tags,
  `always:` flag. Returns XML summary for prompt injection.
- Agent core identity prompt already says: "When you learn something new or
  develop a useful capability, consider creating a skill for it"
- Tools exist for filesystem management (read_file, write_file, search)
- No automated skill generation exists today

## Design

### 1. Skill Detection

Add a detection layer that runs after each agent iteration. When the agent
completes a multi-tool sequence that produces a reusable result, flag it as a
candidate for skill creation.

Detection heuristics (weighted sum, threshold of 3.0 triggers creation):

| Signal | Weight | Example |
|--------|--------|---------|
| 3+ tool calls in sequence | 1.0 | read_file → search → write_file |
| Repeated same pattern across sessions | 2.0 | Same tool sequence twice in different sessions |
| User says "create a skill for this" | 3.0 | Explicit command (always creates) |
| User says "can you always do this" | 2.0 | Strong signal |
| Result is a reusable template/script | 1.5 | Output contains parameterized instructions |
| Agent iterated >5 times with same error | 0.5 | Low signal, probably debugging |

```go
type SkillDetection struct {
    Pattern     string   // hash of tool sequence pattern
    Tools       []string // tool names in order
    Description string   // what the skill does (extracted)
    Frequency   int      // how many times this pattern was observed
    Confidence  float64  // 0.0-1.0
}
```

### 2. Skill Extraction Pipeline

When a detection triggers, extract the skill:

1. **Trace capture**: Record the full tool call sequence (tools + args + results)
   from the current agent iteration
2. **LLM extraction**: Send the trace to the LLM with a skill extraction prompt:
   "Given this conversation trace, generate a SKILL.md for a reusable skill.
   Include YAML frontmatter with name, description, requirements, tags.
   The body should be a clear, step-by-step procedure the agent can follow."
3. **Validation**: Check the generated SKILL.md:
   - Has valid YAML frontmatter
   - Has non-empty body
   - Requirements reference available binaries/env vars
   - Name doesn't conflict with existing skills
4. **Registration**: Write to `workspace/skills/{name}/SKILL.md`, trigger
   skill loader re-discovery

### 3. Skill Prompt Injection

When a relevant skill exists for the current task, inject it into the system
prompt. Relevance is determined by:

- Tag overlap with the current conversation context
- Keyword match between skill description and user's message
- Manual invocation by name

This is already mostly implemented via `LoadSummary` in skills.go. The gap is
automatic relevance detection beyond the current "load all" approach.

### 4. Skill Registry Tool

Add a tool the agent can use to manage skills:

```go
type SkillRegistryTool struct {
    Action string // "list" | "create" | "update" | "delete" | "inspect"
    Name   string // skill name (for create/update/delete/inspect)
    Skill  string // skill content (for create/update)
}
```

This gives the agent a structured way to create skills instead of raw filesystem
wrangling. Behind the scenes it validates, writes, and re-discovers.

### 5. Skill Caching

Skills are loaded and cached in memory. When a new skill is created, invalidate
the cache and re-discover. This is already how the current Loader works - the gap
is that `loaded` flag is internal and there's no public `Invalidate()` method.

```go
func (l *Loader) Invalidate() {
    l.loaded = false
    l.skills = map[string]*Skill{}
}
```

## Files to Create/Modify

### New files

| File | Purpose |
|------|---------|
| `internal/skills/detection.go` | `SkillDetector`, pattern tracking, heuristic scoring |
| `internal/skills/extraction.go` | LLM-based skill extraction from conversation traces |
| `internal/skills/validation.go` | SKILL.md validation (frontmatter, requirements, conflicts) |
| `internal/tools/skill_tool.go` | Tool exposing skill registry to the agent |
| `internal/skills/detection_test.go` | Tests for detection heuristics |

### Modified files

| File | Change |
|------|--------|
| `internal/skills/skills.go` | Add `Invalidate()` method, `Create()` method |
| `internal/agent/agent.go` | Wire detection into agent loop, pass trace to extraction |
| `internal/tools/registry.go` | Register `SkillRegistryTool` |

## Interfaces

```go
// internal/skills/detection.go
type ToolCallRecord struct {
    Tool    string
    Args    map[string]any
    Result  ToolResult // truncated to 200 chars for the trace
}

type Trace struct {
    UserMessage string
    ToolCalls   []ToolCallRecord
    FinalOutput string
}

type CandidateSkill struct {
    Name        string
    Description string
    Trace       Trace
    Confidence  float64
}

type SkillDetector struct {
    // tracks patterns across sessions
    patternCount map[string]*PatternStats
}

type PatternStats struct {
    Pattern    string
    ToolCount  int
    Sessions   []string // session IDs where pattern was observed
    LastSeen   time.Time
    Frequency  int
}

func NewSkillDetector() *SkillDetector
func (d *SkillDetector) RecordTrace(sessionID string, trace Trace)
func (d *SkillDetector) Candidates() []CandidateSkill
func (d *SkillDetector) Detect(trace Trace) *CandidateSkill // returns highest-confidence candidate

// internal/skills/extraction.go
type Extractor struct {
    provider providers.Provider
}

func NewExtractor(provider providers.Provider) *Extractor
func (e *Extractor) Extract(ctx context.Context, trace Trace, existingSkills []Skill) (string, error)
// Returns SKILL.md content as a string (frontmatter + body)

// internal/skills/validation.go
func ValidateSkill(content string) error {
    // Check YAML frontmatter parses
    // Check name != ""
    // Check body != ""
    // Check requirements resolve (bin:, env:)
    // Check no name conflict with existing skills
}
```

## Test Plan

### Unit tests

| Test | What it covers |
|------|----------------|
| `TestDetect_SinglePattern` | No detection with <3 tool calls |
| `TestDetect_ThreeToolSequence` | 3+ tools triggers candidate |
| `TestDetect_RepeatedPattern` | Same pattern across sessions → higher confidence |
| `TestDetect_ExplicitCommand` | User says "create a skill" → immediate detection |
| `TestExtract_ValidOutput` | Valid SKILL.md content with frontmatter |
| `TestValidate_GoodSkill` | Valid skill passes |
| `TestValidate_MissingFrontmatter` | Error returned |
| `TestValidate_EmptyBody` | Error returned |
| `TestValidate_NameConflict` | Error returned when name exists |
| `TestValidate_InvalidRequirements` | `bin:nonexistent-tool` flagged as warning |
| `TestCreateSkill_Persists` | Skill written to disk, discoverable |
| `TestCreateSkill_InvalidatesCache` | New skill appears in LoadSummary after creation |
| `TestSkillRegistryTool_List` | Lists all skills |
| `TestSkillRegistryTool_Create` | Creates valid skill |
| `TestSkillRegistryTool_Create_Invalid` | Returns error for invalid skill |
| `TestSkillRegistryTool_Delete` | Removes skill from disk |
| `TestTrace_Truncation` | Long results truncated to 200 chars |
| `TestPattern_ConcurrentAccess` | Thread-safe pattern tracking |

### Edge cases

- Skill name collision → error, suggest alternative name
- LLM extraction returns invalid YAML → retry with stricter prompt, then fallback
- Trace with 0 tool calls → no candidate (only user message)
- Skill with requirements that can't be met → created but marked unavailable
- User creates a skill with same name as built-in → workspace overrides (existing behavior)
- Detection threshold not met → no skill created, silently tracked
- Empty description in generated skill → use first line of body as description
- Skill file already exists → backup existing, write new, warn user

### Integration tests

- Multi-turn conversation → detection triggers → skill created → skill available
  in next session
- Agent creates skill → re-discovery picks it up → skill injected into prompt

## Effort

| Component | CC time | Human time (equiv) |
|-----------|---------|-------------------|
| SkillDetector + pattern tracking | 15 min | 6 hours |
| Trace capture in agent loop | 10 min | 4 hours |
| LLM extraction prompt + parsing | 20 min | 1 day |
| Validation (frontmatter, requirements) | 10 min | 4 hours |
| SkillRegistryTool | 10 min | 3 hours |
| Invalidate/rediscover wiring | 5 min | 2 hours |
| Tests (18 unit + 2 integration) | 30 min | 1.5 days |
| **Total** | **~1.5 hours** | **~4.5 days** |

## Success Criteria

- Agent auto-creates a SKILL.md after performing a 3+ tool sequence twice
- Agent creates a skill when user explicitly says "create a skill for this"
- Created skills are discoverable and usable in future sessions
- Invalid skill generation is caught by validation before disk write
- Skill name conflicts are detected and reported
- All tests pass with `go test -race -count=1`
- Coverage on `internal/skills/` >= 80%

## Dependency on Milestone 1

Milestone 2 is independent of Milestone 1 and can be implemented in parallel.
The only shared dependency is `internal/agent/agent.go` (both need changes to
the agent loop), so coordination on that file is required if run in parallel.
Recommended order: Milestone 1 first, then Milestone 2, on separate branches.
