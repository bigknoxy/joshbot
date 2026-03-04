# Implementation Plans Index

**Created:** 2026-03-03  
**Status:** Ready for Implementation

---

## Overview

This directory contains detailed implementation plans for four major features inspired by PicoClaw research. Each plan is comprehensive enough for a junior developer to execute without prior knowledge.

---

## Plans

| # | Feature | Priority | Effort | Status |
|---|---------|----------|--------|--------|
| 1 | [System Prompt Caching](./01-system-prompt-caching.md) | High | 4-6h | ✅ Completed |
| 2 | [Model-Centric Config](./02-model-centric-config.md) | High | 8-12h | ✅ Completed |
| 3 | [Async Tools](./03-async-tools.md) | Medium | 6-8h | Not Started |
| 4 | [MCP Integration](./04-mcp-integration.md) | Medium | 12-16h | Not Started |

**Total Estimated Effort:** 30-42 hours

---

## Recommended Implementation Order

### Phase 1: High-Impact, Low-Complexity (Do First)

1. **System Prompt Caching** (4-6 hours)
   - Reduces file I/O on every message
   - High performance impact
   - Low risk, isolated change
   - No dependencies on other features

### Phase 2: Architecture Improvements

2. **Model-Centric Config** (8-12 hours)
   - Better user experience
   - Non-breaking (both formats supported)
   - Enables easier model management
   - Can be done in parallel with #3

3. **Async Tools** (6-8 hours)
   - Prevents timeout issues
   - Better UX for long-running operations
   - Independent of other features
   - Can be done in parallel with #2

### Phase 3: Extended Capabilities

4. **MCP Integration** (12-16 hours)
   - Access to external tool ecosystem
   - New dependency (MCP SDK)
   - More complex than others
   - Benefit from async tools being available

---

## Plan Structure

Each plan follows this structure:

1. **Header** - Status, priority, effort, impact, risk
2. **Goal** - What we're implementing and why
3. **Background** - Context and motivation
4. **Implementation Design** - Architecture and key concepts
5. **Step-by-Step Implementation** - Detailed code changes
6. **Verification Steps** - How to test the implementation
7. **Potential Issues** - Known risks and edge cases
8. **Files Changed** - Impact summary
9. **Completion Checklist** - Track progress
10. **Progress Log** - Update as you work

---

## How to Use These Plans

### For a Junior Developer

1. Read the plan from top to bottom
2. Follow each step in order
3. Copy code snippets exactly as written
4. Run verification steps after each file
5. Check off items in the completion checklist
6. Update the progress log with notes

### For Code Review

1. Check the completion checklist is fully checked
2. Verify all tests pass
3. Review the verification steps were executed
4. Check the progress log for any issues

### For Testing

Each plan includes:
- Unit tests to write
- Integration test scenarios
- Manual test procedures
- Expected outputs

---

## Dependencies Between Plans

```
System Prompt Caching
        │
        ▼
Model-Centric Config ──────┐
        │                   │
        ▼                   ▼
   Async Tools         (parallel possible)
        │
        ▼
  MCP Integration (benefits from async)
```

**Note:** Plans 2 and 3 can be done in parallel by different developers.

---

## Configuration Format Reference

### Current (Provider-Centric)

```json
{
  "providers": {
    "openrouter": {
      "api_key": "...",
      "enabled": true
    }
  },
  "agents": {
    "defaults": {
      "model": "openrouter/...",
      "provider": "openrouter"
    }
  }
}
```

### Proposed (Model-Centric)

```json
{
  "models": [
    {
      "name": "smart",
      "model": "anthropic/claude-sonnet-4",
      "api_key": "..."
    }
  ],
  "agent": {
    "model": "smart",
    "fallback": ["fast", "local"]
  }
}
```

---

## Key Files in joshbot

| File | Purpose |
|------|---------|
| `internal/agent/agent.go` | Core ReAct loop, tool execution |
| `internal/agent/context.go` | System prompt building |
| `internal/config/config.go` | Configuration types and loading |
| `internal/tools/tool.go` | Tool interface |
| `internal/tools/registry.go` | Tool registration and execution |
| `internal/tools/shell.go` | Shell command execution |
| `internal/providers/litellm.go` | LLM provider implementation |

---

## Testing Commands

```bash
# Build
go build ./...

# Run all tests
go test ./...

# Run specific package tests
go test ./internal/agent/... -v
go test ./internal/config/... -v
go test ./internal/tools/... -v
go test ./internal/mcp/... -v

# Run with coverage
go test -cover ./...

# Manual test
joshbot agent -m "hello"
```

---

## Research Sources

These plans are based on deep research of:

- **PicoClaw** (https://github.com/sipeed/picoclaw)
  - System prompt caching implementation
  - Async tool patterns
  - Model-centric configuration

- **MCP Go SDK** (https://github.com/modelcontextprotocol/go-sdk)
  - Client connection patterns
  - Tool discovery and execution
  - Transport types (stdio, HTTP)

---

## Next Steps

1. Choose a plan to implement
2. Create a feature branch: `git checkout -b feature/system-prompt-caching`
3. Follow the plan step-by-step
4. Run tests frequently
5. Update the progress log
6. Create PR when complete

---

## Questions?

If you encounter issues while implementing:

1. Check the "Potential Issues" section of the plan
2. Review the research sources for reference implementations
3. Check joshbot's existing patterns in similar files
4. Document any issues found in the progress log
