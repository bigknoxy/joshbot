# Plan: System Prompt Caching

**Status:** ✅ Completed  
**Priority:** High  
**Estimated Effort:** 4-6 hours (~200 LOC)  
**Impact:** High (faster processing, lower CPU usage)  
**Risk:** Low (isolated change)

---

## Goal

Implement intelligent caching of the static system prompt with mtime-based invalidation. This reduces redundant file I/O on every message and improves response latency.

---

## Background

Currently, `BuildPrompt()` in `internal/agent/context.go` reads files from disk on every call:
- `MEMORY.md` 
- `AGENTS.md`, `SOUL.md`, `USER.md`, `TOOLS.md`, `IDENTITY.md`
- All skill files in `workspace/skills/*/SKILL.md`

These files rarely change between messages. PicoClaw solves this by caching the prompt and only rebuilding when files actually change (detected via modification time).

---

## Implementation Design

### Key Concepts

1. **Static vs Dynamic Content**
   - Static: identity files, MEMORY.md, skills (rarely change)
   - Dynamic: current time (changes every second)
   
2. **Cache Key**: Track file existence + max modification time
3. **Invalidation**: Rebuild cache when files change

### Data Structures

```go
// cacheBaseline tracks the state of source files for cache invalidation
type cacheBaseline struct {
    existed  map[string]bool // Which files existed
    maxMtime time.Time       // Latest modification time
}

// CachedPrompt holds cached prompt with its baseline
type CachedPrompt struct {
    prompt   string
    baseline cacheBaseline
    mu       sync.RWMutex
}
```

### Cache Invalidation Logic

A cache is valid if:
1. All files that existed before still exist
2. All files that didn't exist before still don't exist
3. No file's mtime is newer than cached maxMtime

---

## Step-by-Step Implementation

### Step 1: Create the cache structure

**File:** `internal/agent/context.go`

Add these new types and variables after the imports:

```go
import (
    // ... existing imports ...
    "sync"
)

// cacheBaseline tracks the state of source files for cache invalidation.
type cacheBaseline struct {
    existed  map[string]bool // Which files existed when cache was built
    maxMtime time.Time       // Latest modification time of all files
}

// promptCache holds the cached static portion of the system prompt.
type promptCache struct {
    prompt   string        // Cached prompt content
    baseline cacheBaseline // State when cache was built
    mu       sync.RWMutex  // Protects concurrent access
}

// globalPromptCache is the singleton cache for system prompts.
var globalPromptCache promptCache

// sourceFile tracks a file path and whether it existed.
type sourceFile struct {
    path    string
    existed bool
    mtime   time.Time
}
```

### Step 2: Implement cache invalidation check

Add these helper functions:

```go
// sourceFilesChanged checks if any source files have changed since baseline.
func sourceFilesChanged(workspace string, baseline cacheBaseline) bool {
    // Get current state of all source files
    currentFiles := collectSourceFiles(workspace)
    
    // Check if file set changed
    if len(currentFiles) != len(baseline.existed) {
        return true
    }
    
    // Check each file
    for path, existed := range baseline.existed {
        info, err := os.Stat(path)
        currentlyExists := err == nil
        
        // File existence changed
        if existed != currentlyExists {
            return true
        }
        
        // File still exists, check mtime
        if currentlyExists && info.ModTime().After(baseline.maxMtime) {
            return true
        }
    }
    
    return false
}

// collectSourceFiles gathers all source file paths and their existence.
func collectSourceFiles(workspace string) map[string]bool {
    files := make(map[string]bool)
    
    // Identity files
    identityFiles := []string{
        filepath.Join(workspace, "AGENTS.md"),
        filepath.Join(workspace, "SOUL.md"),
        filepath.Join(workspace, "USER.md"),
        filepath.Join(workspace, "TOOLS.md"),
        filepath.Join(workspace, "IDENTITY.md"),
    }
    
    for _, path := range identityFiles {
        _, err := os.Stat(path)
        files[path] = err == nil
    }
    
    // Memory file
    memPath := filepath.Join(workspace, "memory", "MEMORY.md")
    _, err := os.Stat(memPath)
    files[memPath] = err == nil
    
    // Skills files (recursive walk)
    skillsDir := filepath.Join(workspace, "skills")
    filepath.Walk(skillsDir, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return nil
        }
        if !info.IsDir() && (strings.HasSuffix(path, "SKILL.md") || strings.HasSuffix(path, "skill.md")) {
            files[path] = true
        }
        return nil
    })
    
    return files
}

// buildCacheBaseline creates a baseline from current file state.
func buildCacheBaseline(workspace string) cacheBaseline {
    files := collectSourceFiles(workspace)
    var maxMtime time.Time
    
    for path, existed := range files {
        if existed {
            info, err := os.Stat(path)
            if err == nil && info.ModTime().After(maxMtime) {
                maxMtime = info.ModTime()
            }
        }
    }
    
    return cacheBaseline{
        existed:  files,
        maxMtime: maxMtime,
    }
}
```

### Step 3: Implement cached prompt builder

Add the main caching function:

```go
// BuildPromptCached builds the static portion of the prompt with caching.
// Returns (staticPrompt, needsCache).
func BuildPromptCached(workspace string, skills SkillsLoader, memory MemoryLoader) string {
    // Fast path: read lock to check cache
    globalPromptCache.mu.RLock()
    if globalPromptCache.prompt != "" && !sourceFilesChanged(workspace, globalPromptCache.baseline) {
        cached := globalPromptCache.prompt
        globalPromptCache.mu.RUnlock()
        return cached
    }
    globalPromptCache.mu.RUnlock()
    
    // Slow path: write lock to rebuild cache
    globalPromptCache.mu.Lock()
    defer globalPromptCache.mu.Unlock()
    
    // Double-check after acquiring write lock
    if globalPromptCache.prompt != "" && !sourceFilesChanged(workspace, globalPromptCache.baseline) {
        return globalPromptCache.prompt
    }
    
    // Build new cache
    prompt := buildStaticPrompt(workspace, skills, memory)
    baseline := buildCacheBaseline(workspace)
    
    globalPromptCache.prompt = prompt
    globalPromptCache.baseline = baseline
    
    return prompt
}

// buildStaticPrompt builds the static portion of the system prompt.
// This is the cached part - excludes dynamic content like current time.
func buildStaticPrompt(workspace string, skills SkillsLoader, memory MemoryLoader) string {
    parts := []string{}
    
    // Core identity (hardcoded)
    parts = append(parts, buildCoreIdentity())
    
    // Load identity files from workspace
    identity := loadIdentityFiles(workspace)
    for name, content := range identity {
        if content != "" {
            parts = append(parts, fmt.Sprintf("<%s>\n%s\n</%s>", name, content, name))
        }
    }
    
    // Memory context (always loaded)
    if memory != nil {
        memContent, err := memory.LoadMemory(context.Background())
        if err == nil && memContent != "" {
            parts = append(parts, fmt.Sprintf("<memory>\n%s\n</memory>", memContent))
        }
    }
    
    // Skills summary
    if skills != nil {
        skillsSummary, err := skills.LoadSummary(context.Background())
        if err == nil && skillsSummary != "" {
            parts = append(parts, fmt.Sprintf("<skills>\n%s\n</skills>", skillsSummary))
        }
    }
    
    return joinParts(parts)
}
```

### Step 4: Update BuildPrompt to use cache

Replace the existing `BuildPrompt` function:

```go
// BuildPrompt builds the full system prompt from workspace files and injected context.
// Uses caching for static content to avoid redundant file I/O.
func BuildPrompt(workspace string, skills SkillsLoader, memory MemoryLoader, userName string) string {
    parts := []string{}
    
    // Get cached static prompt (identity files, memory, skills)
    staticPrompt := BuildPromptCached(workspace, skills, memory)
    if staticPrompt != "" {
        parts = append(parts, staticPrompt)
    }
    
    // User name context - add after identity but before dynamic content
    if userName != "" {
        parts = append(parts, fmt.Sprintf(`The user's name is %s. Use their name sparingly and naturally - occasional greetings, sign-offs, or personal touches are appropriate. Do not overuse it.`, userName))
    }
    
    // Current time (dynamic, not cached)
    now := time.Now().UTC().Format("2006-01-02 15:04 UTC")
    parts = append(parts, fmt.Sprintf("<current_time>%s</current_time>", now))
    
    return joinParts(parts)
}
```

### Step 5: Add a function to invalidate cache (for testing/force refresh)

```go
// InvalidatePromptCache clears the global prompt cache.
// Use this to force a rebuild on next request.
func InvalidatePromptCache() {
    globalPromptCache.mu.Lock()
    defer globalPromptCache.mu.Unlock()
    globalPromptCache.prompt = ""
    globalPromptCache.baseline = cacheBaseline{}
}
```

### Step 6: Add tests

**File:** `internal/agent/context_test.go`

```go
package agent

import (
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestBuildPromptCached_BasicCaching(t *testing.T) {
    // Create temp workspace
    tmpDir := t.TempDir()
    memDir := filepath.Join(tmpDir, "memory")
    _ = os.Mkdir(memDir, 0o755)
    
    // Create a MEMORY.md
    memPath := filepath.Join(memDir, "MEMORY.md")
    _ = os.WriteFile(memPath, []byte("Test memory content"), 0o644)
    
    // First call builds cache
    prompt1 := BuildPromptCached(tmpDir, nil, nil)
    
    // Second call should use cache
    prompt2 := BuildPromptCached(tmpDir, nil, nil)
    
    if prompt1 != prompt2 {
        t.Error("Cached prompt should be identical")
    }
}

func TestBuildPromptCached_Invalidation(t *testing.T) {
    tmpDir := t.TempDir()
    memDir := filepath.Join(tmpDir, "memory")
    _ = os.Mkdir(memDir, 0o755)
    
    memPath := filepath.Join(memDir, "MEMORY.md")
    _ = os.WriteFile(memPath, []byte("Original content"), 0o644)
    
    // Build initial cache
    prompt1 := BuildPromptCached(tmpDir, nil, nil)
    
    // Wait a moment to ensure mtime is different
    time.Sleep(100 * time.Millisecond)
    
    // Modify file
    _ = os.WriteFile(memPath, []byte("Modified content"), 0o644)
    
    // Should detect change and rebuild
    prompt2 := BuildPromptCached(tmpDir, nil, nil)
    
    if prompt1 == prompt2 {
        t.Error("Prompt should change after file modification")
    }
    
    if !contains(prompt2, "Modified content") {
        t.Error("New prompt should contain modified content")
    }
}

func TestBuildPromptCached_NewFile(t *testing.T) {
    tmpDir := t.TempDir()
    
    // Build cache without any files
    _ = BuildPromptCached(tmpDir, nil, nil)
    
    // Create new file
    _ = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("New agent"), 0o644)
    
    // Should detect new file and rebuild
    prompt := BuildPromptCached(tmpDir, nil, nil)
    
    if !contains(prompt, "New agent") {
        t.Error("New prompt should contain new file content")
    }
}

func TestInvalidatePromptCache(t *testing.T) {
    tmpDir := t.TempDir()
    memDir := filepath.Join(tmpDir, "memory")
    _ = os.Mkdir(memDir, 0o755)
    
    _ = os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("Test"), 0o644)
    
    // Build cache
    _ = BuildPromptCached(tmpDir, nil, nil)
    
    // Cache should be populated
    globalPromptCache.mu.RLock()
    cachedPrompt := globalPromptCache.prompt
    globalPromptCache.mu.RUnlock()
    
    if cachedPrompt == "" {
        t.Error("Cache should be populated after first call")
    }
    
    // Invalidate
    InvalidatePromptCache()
    
    // Cache should be empty
    globalPromptCache.mu.RLock()
    cachedPrompt = globalPromptCache.prompt
    globalPromptCache.mu.RUnlock()
    
    if cachedPrompt != "" {
        t.Error("Cache should be empty after invalidation")
    }
}

func contains(s, substr string) bool {
    return len(s) > 0 && len(substr) > 0 && 
           (s == substr || len(s) >= len(substr) && (s[:len(substr)] == substr || contains(s[1:], substr)))
}
```

---

## Verification Steps

1. **Build and test:**
   ```bash
   go build ./...
   go test ./internal/agent/... -v
   ```

2. **Run integration test:**
   ```bash
   # Start joshbot and send a message, verify it works
   joshbot agent -m "hello"
   
   # Modify MEMORY.md
   echo "\nTest addition" >> ~/.joshbot/workspace/memory/MEMORY.md
   
   # Send another message, verify cache was invalidated
   joshbot agent -m "what's in your memory?"
   ```

3. **Check performance improvement:**
   ```bash
   # Before: measure time
   time joshbot agent -m "hello"
   
   # After: measure time
   time joshbot agent -m "hello"
   
   # Second call should be faster (cache hit)
   time joshbot agent -m "hello again"
   ```

---

## Potential Issues

1. **Race conditions**: The double-checked locking pattern handles concurrent access safely.

2. **File system precision**: Some file systems have low mtime precision (1-2 seconds). This is acceptable for our use case.

3. **Memory usage**: Cache stores the full static prompt in memory. For typical prompts (<50KB), this is negligible.

4. **Skill discovery**: New skills added to `workspace/skills/` are detected via the recursive walk.

---

## Future Enhancements

1. **Configurable cache TTL**: Add option to force rebuild after N minutes
2. **Cache statistics**: Log cache hits/misses for monitoring
3. **Selective invalidation**: Only rebuild changed portions (harder to implement)

---

## Files Changed

| File | Changes |
|------|---------|
| `internal/agent/context.go` | Add caching types, `BuildPromptCached()`, update `BuildPrompt()` |
| `internal/agent/context_test.go` | Add unit tests for caching |

---

## Completion Checklist

- [x] Added cache types (`cacheBaseline`, `promptCache`)
- [x] Implemented `sourceFilesChanged()`
- [x] Implemented `collectSourceFiles()`
- [x] Implemented `buildCacheBaseline()`
- [x] Implemented `BuildPromptCached()`
- [x] Implemented `buildStaticPrompt()`
- [x] Updated `BuildPrompt()` to use cache
- [x] Added `InvalidatePromptCache()`
- [x] Added unit tests
- [x] Verified build passes
- [x] Verified tests pass
- [x] Tested manually with joshbot agent

---

## Progress Log

| Date | Status | Notes |
|------|--------|-------|
| 2026-03-03 | Not Started | Plan created |
| 2026-03-03 | In Progress | Started implementation |
| 2026-03-03 | Completed | All tasks done, tests pass, manual test successful |
