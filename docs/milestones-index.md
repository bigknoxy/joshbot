# Milestones

Top-2 highest-leverage features for joshbot, ranked by user value / market gap / effort ratio.

| Rank | Milestone | User Value | Market Gap | Effort (CC) | File |
|------|-----------|------------|------------|-------------|------|
| 1 | **Intelligent Memory System** | Critical | No Go-based assistant has structured, user-editable, cross-session memory | ~2h | `milestone-01-intelligent-memory.md` |
| 2 | **Skill Self-Creation** | High | Only Hermes Agent + SwarmAI have it (both Python/TS 100K+ LOC) | ~1.5h | `milestone-02-skill-self-creation.md` |

## Methodology

1. **Codebase exploration**: Read all internal/ packages, configuration, existing
   plans, and test coverage. Identified current capabilities and gaps.
2. **Market research**: Searched and compared 20+ open-source AI assistant projects
   (OpenClaw, Hermes, Goose, Aider, fabric, Moltis, Bernard, Fera, etc.).
3. **Synthesis**: Ranked features by user demand, market differentiation, and
   implementation feasibility with joshbot's existing architecture.

## Detailed specs

Each milestone document contains:

- **Why this matters**: Market context and user value
- **Current state**: What exists today
- **Design**: Architecture, data model, interfaces
- **Files to create/modify**: Exact file list with changes
- **Test plan**: Unit tests, edge cases, integration tests
- **Effort estimate**: CC + human-equivalent time
- **Success criteria**: Measurable outcomes

## How to use these

Each milestone is designed to be picked up by a coding agent and implemented
independently. Follow the spec in order:

1. Read the milestone document
2. Read the referenced existing files for context
3. Implement in the order specified
4. Run the test plan
5. Verify success criteria
