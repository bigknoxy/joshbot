package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
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

// sourceFilesChanged checks if any source files have changed since baseline.
func sourceFilesChanged(workspace string, baseline cacheBaseline) bool {
	if baseline.existed == nil {
		return true
	}

	currentFiles := collectSourceFiles(workspace)

	if len(currentFiles) != len(baseline.existed) {
		return true
	}

	for path, existed := range baseline.existed {
		info, err := os.Stat(path)
		currentlyExists := err == nil

		if existed != currentlyExists {
			return true
		}

		if currentlyExists && info.ModTime().After(baseline.maxMtime) {
			return true
		}
	}

	return false
}

// collectSourceFiles gathers all source file paths and their existence.
func collectSourceFiles(workspace string) map[string]bool {
	files := make(map[string]bool)

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

	memPath := filepath.Join(workspace, "memory", "MEMORY.md")
	_, err := os.Stat(memPath)
	files[memPath] = err == nil

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

// BuildPromptCached builds the static portion of the prompt with caching.
func BuildPromptCached(workspace string, skills SkillsLoader, memory MemoryLoader) string {
	globalPromptCache.mu.RLock()
	cached := globalPromptCache.prompt
	baseline := globalPromptCache.baseline
	globalPromptCache.mu.RUnlock()

	if cached != "" && !sourceFilesChanged(workspace, baseline) {
		return cached
	}

	globalPromptCache.mu.Lock()
	defer globalPromptCache.mu.Unlock()

	// Double-check after acquiring write lock
	if globalPromptCache.prompt != "" && !sourceFilesChanged(workspace, globalPromptCache.baseline) {
		return globalPromptCache.prompt
	}

	prompt := buildStaticPrompt(workspace, skills, memory)
	newBaseline := buildCacheBaseline(workspace)

	globalPromptCache.prompt = prompt
	globalPromptCache.baseline = newBaseline

	return prompt
}

// buildStaticPrompt builds the static portion of the system prompt.
func buildStaticPrompt(workspace string, skills SkillsLoader, memory MemoryLoader) string {
	parts := []string{buildCoreIdentity()}

	for name, content := range loadIdentityFiles(workspace) {
		if content != "" {
			parts = append(parts, fmt.Sprintf("<%s>\n%s\n</%s>", name, content, name))
		}
	}

	if memory != nil {
		if memContent, err := memory.LoadMemory(context.Background()); err == nil && memContent != "" {
			parts = append(parts, fmt.Sprintf("<memory>\n%s\n</memory>", memContent))
		}
	}

	if skills != nil {
		if summary, err := skills.LoadSummary(context.Background()); err == nil && summary != "" {
			parts = append(parts, fmt.Sprintf("<skills>\n%s\n</skills>", summary))
		}
	}

	return joinParts(parts)
}

// InvalidatePromptCache clears the global prompt cache.
func InvalidatePromptCache() {
	globalPromptCache.mu.Lock()
	defer globalPromptCache.mu.Unlock()
	globalPromptCache.prompt = ""
	globalPromptCache.baseline = cacheBaseline{}
}

// BuildPrompt builds the full system prompt from workspace files and injected context.
// Uses caching for static content to avoid redundant file I/O.
func BuildPrompt(workspace string, skills SkillsLoader, memory MemoryLoader, userName string) string {
	parts := []string{}

	staticPrompt := BuildPromptCached(workspace, skills, memory)
	if staticPrompt != "" {
		parts = append(parts, staticPrompt)
	}

	if userName != "" {
		parts = append(parts, fmt.Sprintf(`The user's name is %s. Use their name sparingly and naturally - occasional greetings, sign-offs, or personal touches are appropriate. Do not overuse it.`, userName))
	}

	now := time.Now().UTC().Format("2006-01-02 15:04 UTC")
	parts = append(parts, fmt.Sprintf("<current_time>%s</current_time>", now))

	return joinParts(parts)
}

// BuildSmartPrompt builds a system prompt with relevance-scored memory injection.
// The currentQuery parameter is reserved for future smart memory retrieval.
func BuildSmartPrompt(workspace string, skills SkillsLoader, mem MemoryLoader, userName string, currentQuery string) string {
	parts := []string{}

	staticPrompt := BuildPromptCached(workspace, skills, mem)
	if staticPrompt != "" {
		parts = append(parts, staticPrompt)
	}

	if userName != "" {
		parts = append(parts, fmt.Sprintf(`The user's name is %s.`, userName))
	}

	now := time.Now().UTC().Format("2006-01-02 15:04 UTC")
	parts = append(parts, fmt.Sprintf("<current_time>%s</current_time>", now))

	return joinParts(parts)
}

// buildCoreIdentity returns the core identity prompt.
func buildCoreIdentity() string {
	return `You are joshbot. A personal AI assistant built by someone who wanted a bot that actually works.

IDENTITY: Use your tools — filesystem, shell, web, memory, skills, subagents. Tools fetch, compute, and verify. Do not rely on knowledge alone.

WORK DISCIPLINE:
- Read before write — inspect files before changes.
- Batch operations — replace three calls with one when possible.
- Admit uncertainty, then investigate: search, grep, fetch, verify.
- Accept corrections. Each mistake improves future behavior.
- Create skills from repeating patterns. Offer them; never force them.

TOOL DIRECTIVES:
- Web: use web_search and web_fetch for internet data.
- Shell: use for builds, tests, git, and code execution. Safety guards active.
- Filesystem: read_file to inspect, write_file to save, grep to locate.
- Parallel: use parallel_subagent for independent research on multiple topics.
- Chain: use chain_execution for pipelines — research, outline, draft, polish.

MEMORY RULES:
- MEMORY.md is your long-term knowledge base — always in context at session start.
- Append notable events to HISTORY.md. The learning system distills them into MEMORY.md.
- Discovered user facts or project context? Update MEMORY.md.
- Append meaningful conversation summaries to HISTORY.md.

CONVERSATION RULES:
- Read every message fully. Track the flow — each turn builds on the prior one.
- Resolve pronouns ("that", "it", "this") from immediate context. Ask only when ambiguous.
- Stay on topic until the user changes direction.
- Do not invent missing context. State gaps directly.
- If the thread lost direction, summarize the last few exchanges and confirm.

CONTINUITY RULES:
- Read the <conversation_context> tag at the top of every prompt. Use it as your source of truth about the user.
- Acknowledge corrections immediately. Check <conversation_context> for accurate information. Do not repeat mistakes.
- Remember what was said earlier. Do not ask the user to re-introduce themselves.
- When the user says "/new", message history clears but session context (name, organization, role) is preserved.

CHAT HISTORY RULES:
- Recent messages are verbatim. Older tool outputs may be truncated — conversation structure and reasoning are preserved.
- If a tool output is truncated, read the message structure to understand the conversation flow.

`
}

// loadIdentityFiles loads identity/bootstrap files from workspace.
func loadIdentityFiles(workspace string) map[string]string {
	files := map[string]string{
		"agents":   "AGENTS.md",
		"soul":     "SOUL.md",
		"user":     "USER.md",
		"tools":    "TOOLS.md",
		"identity": "IDENTITY.md",
	}

	result := make(map[string]string)
	for key, filename := range files {
		path := filepath.Join(workspace, filename)
		data, err := os.ReadFile(path)
		if err == nil {
			result[key] = string(data)
		}
	}

	return result
}

// joinParts joins prompt parts with double newlines.
func joinParts(parts []string) string {
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(part)
	}
	return b.String()
}

// LoadMemoryFile loads the MEMORY.md file from the workspace.
func LoadMemoryFile(workspace string) (string, error) {
	path := filepath.Join(workspace, "memory", "MEMORY.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read MEMORY.md: %w", err)
	}
	return string(data), nil
}

// LoadHistoryFile loads the HISTORY.md file from the workspace.
func LoadHistoryFile(workspace string) (string, error) {
	path := filepath.Join(workspace, "memory", "HISTORY.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read HISTORY.md: %w", err)
	}
	return string(data), nil
}

// FormatToolDescriptions formats tool schemas for the prompt.
func FormatToolDescriptions(tools []providers.Tool) string {
	if len(tools) == 0 {
		return ""
	}

	// Format as JSON schema for tools
	schemas, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		return ""
	}
	return string(schemas)
}
