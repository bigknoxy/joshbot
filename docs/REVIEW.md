# joshbot Review Notes

Generated: 2026-02-23

This document tracks high-level review outcomes for the Go implementation of joshbot.

## Status Snapshot

### Addressed
- Significant-turn history persistence is now wired in the agent loop and recorded into `HISTORY.md`.
- `memory_window` is enforced in message construction before token compression.
- Prompt budget accounting now includes system prompt token cost.
- Filesystem operation dispatch now validates paths per operation (instead of globally requiring `path`).
- Skill/docs naming aligned to runtime shell tool (`shell`, not `exec`).
- Default `restrict_to_workspace` is now enabled.

### Remaining Work
- Improve model context window lookup accuracy beyond heuristic buckets.
- Expand filesystem/shell security hardening and add explicit threat-model tests.
- Add richer end-to-end tests for memory consolidation quality.

## Notes
- This repository is Go-first; prior Python-path findings were removed to avoid confusion.
