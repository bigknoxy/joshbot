---
name: memory
description: "How to use the long-term memory system (MEMORY.md and HISTORY.md)"
always: true
---

# Memory System

You have a two-file memory system for maintaining context across conversations:

## MEMORY.md (Long-Term Facts)
- Location: `memory/MEMORY.md` in your workspace
- **Always loaded** into your context at the start of every conversation
- Contains: user preferences, project context, relationships, technical decisions, important notes
- **Updated during memory consolidation** - when conversations get long, key facts are extracted here
- You can also update it manually with `write_file` or `edit_file` when you learn something important

## HISTORY.md (Event Log)
- Location: `memory/HISTORY.md` in your workspace
- **Append-only** log of timestamped conversation summaries
- NOT loaded into context (to save space) - search it when needed
- Search with: `exec` tool running `grep -i "keyword" memory/HISTORY.md`
- Each entry is 2-5 sentences with timestamp `[YYYY-MM-DD HH:MM]`

## Best Practices
1. When you learn something important about the user, update MEMORY.md immediately
2. When the user references something from the past, search HISTORY.md
3. Keep MEMORY.md organized with clear sections
4. Don't store sensitive information (passwords, tokens) in memory files
5. During consolidation, focus on facts that will be useful in future conversations
