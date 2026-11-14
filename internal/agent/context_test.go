package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildPromptCached_BasicCaching(t *testing.T) {
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, "memory")
	_ = os.Mkdir(memDir, 0o755)

	memPath := filepath.Join(memDir, "MEMORY.md")
	_ = os.WriteFile(memPath, []byte("Test memory content"), 0o644)

	InvalidatePromptCache()

	prompt1 := BuildPromptCached(tmpDir, nil, nil)

	prompt2 := BuildPromptCached(tmpDir, nil, nil)

	if prompt1 != prompt2 {
		t.Error("Cached prompt should be identical")
	}

	if !strings.Contains(prompt2, "joshbot") {
		t.Error("Prompt should contain core identity")
	}
}

func TestBuildPromptCached_Invalidation(t *testing.T) {
	tmpDir := t.TempDir()

	InvalidatePromptCache()

	mockMemory := &mockMemoryLoader{
		memoryFn: func(ctx context.Context) (string, error) {
			return "Original content", nil
		},
	}

	prompt1 := BuildPromptCached(tmpDir, nil, mockMemory)

	// The cached block no longer contains <memory> content;
	// volatile content is injected at runtime in BuildPrompt.
	if strings.Contains(prompt1, "Original content") {
		t.Error("Cached prompt should no longer contain <memory> content")
	}

	mockMemory.memoryFn = func(ctx context.Context) (string, error) {
		return "New content from loader", nil
	}

	prompt2 := BuildPromptCached(tmpDir, nil, mockMemory)

	// On cache hit, the same (static) prompt is returned;
	// the mock memory change is not reflected until InvalidatePromptCache.
	if prompt1 != prompt2 {
		t.Error("Cache hit - should return same cached prompt")
	}
}

func TestBuildPromptCached_NewFileInvalidates(t *testing.T) {
	tmpDir := t.TempDir()

	InvalidatePromptCache()

	_ = BuildPromptCached(tmpDir, nil, nil)

	_ = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("New file"), 0o644)

	files := collectSourceFiles(tmpDir)
	if !files[filepath.Join(tmpDir, "AGENTS.md")] {
		t.Error("New file should be detected in source files")
	}
}

func TestBuildPromptCached_NewFile(t *testing.T) {
	tmpDir := t.TempDir()

	InvalidatePromptCache()

	_ = BuildPromptCached(tmpDir, nil, nil)

	_ = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("New agent content"), 0o644)

	prompt := BuildPromptCached(tmpDir, nil, nil)

	if !strings.Contains(prompt, "New agent content") {
		t.Error("New prompt should contain new file content")
	}
}

func TestBuildPromptCached_DeletedFile(t *testing.T) {
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, "memory")
	_ = os.Mkdir(memDir, 0o755)

	memPath := filepath.Join(memDir, "MEMORY.md")
	_ = os.WriteFile(memPath, []byte("Memory content"), 0o644)

	InvalidatePromptCache()

	_ = BuildPromptCached(tmpDir, nil, nil)

	_ = os.Remove(memPath)

	prompt := BuildPromptCached(tmpDir, nil, nil)

	if strings.Contains(prompt, "Memory content") {
		t.Error("Prompt should not contain deleted file content")
	}
}

func TestBuildPromptCached_SkillsExcludedFromCache(t *testing.T) {
	tmpDir := t.TempDir()

	InvalidatePromptCache()

	mockSkills := &mockSkillsLoader{
		summaryFn: func(ctx context.Context) (string, error) {
			return "Test skill summary from loader", nil
		},
	}

	prompt := BuildPromptCached(tmpDir, mockSkills, nil)

	// The cached block no longer contains <skills> content;
	// volatile content is injected at runtime in BuildPrompt.
	if strings.Contains(prompt, "Test skill summary from loader") {
		t.Error("Cached prompt should no longer contain <skills> content")
	}

	// Cache still functions correctly - same prompt returned on repeated calls.
	prompt2 := BuildPromptCached(tmpDir, mockSkills, nil)
	if prompt != prompt2 {
		t.Error("Cache hit - should return same cached prompt")
	}
}
func TestBuildPromptCached_FileBasedInvalidation(t *testing.T) {
	tmpDir := t.TempDir()

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	_ = os.WriteFile(agentsPath, []byte("Original"), 0o644)

	InvalidatePromptCache()

	baseline := buildCacheBaseline(tmpDir)

	if sourceFilesChanged(tmpDir, baseline) {
		t.Error("Should not detect change immediately after building baseline")
	}

	newTime := time.Now().Add(time.Second)
	_ = os.Chtimes(agentsPath, newTime, newTime)

	if !sourceFilesChanged(tmpDir, baseline) {
		t.Error("Should detect change after mtime update")
	}
}

// A memory write must not invalidate the cached prefix. MEMORY.md is no
// longer part of the cached block -- it is injected at runtime -- so
// changing it changes the prompt the model sees without disturbing the
// cache. Before this, every memory write threw the whole prefix away, so
// the hit rate fell exactly when the agent was learning something.
func TestBuildPromptCached_MemoryWriteDoesNotInvalidate(t *testing.T) {
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, "memory")
	_ = os.Mkdir(memDir, 0o755)

	memPath := filepath.Join(memDir, "MEMORY.md")
	_ = os.WriteFile(memPath, []byte("Original"), 0o644)

	InvalidatePromptCache()

	baseline := buildCacheBaseline(tmpDir)

	if sourceFilesChanged(tmpDir, baseline) {
		t.Error("Should not detect change immediately after building baseline")
	}

	newTime := time.Now().Add(time.Second)
	_ = os.Chtimes(memPath, newTime, newTime)

	if sourceFilesChanged(tmpDir, baseline) {
		t.Error("A MEMORY.md write should not invalidate the cached prompt")
	}
}

// The split moved a boundary; it must not have dropped content. The cached
// block excludes memory and skills, but the prompt the model actually sees
// must still contain both.
func TestBuildPrompt_StillIncludesMemoryAndSkills(t *testing.T) {
	tmpDir := t.TempDir()

	InvalidatePromptCache()

	mockSkills := &mockSkillsLoader{
		summaryFn: func(ctx context.Context) (string, error) {
			return "Test skill summary from loader", nil
		},
	}
	mockMemory := &stubMemoryLoader{body: "Fact the agent learned"}

	prompt := BuildPrompt(tmpDir, mockSkills, mockMemory, "")

	if !strings.Contains(prompt, "Fact the agent learned") {
		t.Error("The assembled prompt should contain the memory content")
	}
	if !strings.Contains(prompt, "Test skill summary from loader") {
		t.Error("The assembled prompt should contain the skills summary")
	}
}

// stubMemoryLoader returns a fixed memory body.
type stubMemoryLoader struct{ body string }

func (m *stubMemoryLoader) LoadMemory(ctx context.Context) (string, error) {
	return m.body, nil
}

func (m *stubMemoryLoader) LoadHistory(ctx context.Context, query string) (string, error) {
	return "", nil
}

func TestInvalidatePromptCache(t *testing.T) {
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, "memory")
	_ = os.Mkdir(memDir, 0o755)

	_ = os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("Test"), 0o644)

	InvalidatePromptCache()

	_ = BuildPromptCached(tmpDir, nil, nil)

	globalPromptCache.mu.RLock()
	cachedPrompt := globalPromptCache.prompt
	globalPromptCache.mu.RUnlock()

	if cachedPrompt == "" {
		t.Error("Cache should be populated after first call")
	}

	InvalidatePromptCache()

	globalPromptCache.mu.RLock()
	cachedPrompt = globalPromptCache.prompt
	globalPromptCache.mu.RUnlock()

	if cachedPrompt != "" {
		t.Error("Cache should be empty after invalidation")
	}
}

func TestCollectSourceFiles(t *testing.T) {
	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("agents"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "USER.md"), []byte("user"), 0o644)

	skillsDir := filepath.Join(tmpDir, "skills", "test")
	_ = os.MkdirAll(skillsDir, 0o755)
	// Skills SKILL.md is no longer collected for the cache key.
	//_ = os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("skill"), 0o644)

	files := collectSourceFiles(tmpDir)

	expectedFiles := []string{
		filepath.Join(tmpDir, "AGENTS.md"),
		filepath.Join(tmpDir, "USER.md"),
	}

	for _, expected := range expectedFiles {
		if !files[expected] {
			t.Errorf("Expected file to exist: %s", expected)
		}
	}

	if files[filepath.Join(tmpDir, "memory", "MEMORY.md")] {
		t.Error("MEMORY.md should not be in collected source files (removed from cache key)")
	}

	if files[filepath.Join(tmpDir, "skills", "test", "SKILL.md")] {
		t.Error("skills SKILL.md should not be in collected source files (removed from cache key)")
	}

	if files[filepath.Join(tmpDir, "SOUL.md")] {
		t.Error("SOUL.md should not exist")
	}
}

func TestSourceFilesChanged(t *testing.T) {
	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("test"), 0o644)

	baseline := buildCacheBaseline(tmpDir)

	if sourceFilesChanged(tmpDir, baseline) {
		t.Error("No changes detected, should return false")
	}

	time.Sleep(100 * time.Millisecond)

	_ = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("modified"), 0o644)

	if !sourceFilesChanged(tmpDir, baseline) {
		t.Error("File modified, should detect change")
	}
}

func TestBuildPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, "memory")
	_ = os.Mkdir(memDir, 0o755)
	_ = os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("Test memory"), 0o644)

	InvalidatePromptCache()

	prompt := BuildPrompt(tmpDir, nil, nil, "Alice")

	if !strings.Contains(prompt, "joshbot") {
		t.Error("Prompt should contain core identity")
	}

	if !strings.Contains(prompt, "Alice") {
		t.Error("Prompt should contain user name")
	}

	if !strings.Contains(prompt, "current_time") {
		t.Error("Prompt should contain current time")
	}
}

func TestBuildPrompt_DynamicContent(t *testing.T) {
	tmpDir := t.TempDir()

	InvalidatePromptCache()

	prompt := BuildPrompt(tmpDir, nil, nil, "")

	if !strings.Contains(prompt, "current_time") {
		t.Error("Prompt should contain current_time tag")
	}

	if !strings.Contains(prompt, "joshbot") {
		t.Error("Prompt should contain core identity")
	}
}

func TestBuildPrompt_UserName(t *testing.T) {
	tmpDir := t.TempDir()

	InvalidatePromptCache()

	prompt := BuildPrompt(tmpDir, nil, nil, "Bob")

	if !strings.Contains(prompt, "Bob") {
		t.Error("Prompt should contain user name")
	}
}
