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

	if !strings.Contains(prompt1, "Original content") {
		t.Error("First prompt should contain original content")
	}

	mockMemory.memoryFn = func(ctx context.Context) (string, error) {
		return "New content from loader", nil
	}

	prompt2 := BuildPromptCached(tmpDir, nil, mockMemory)

	if !strings.Contains(prompt2, "Original content") {
		t.Error("Cache hit - should still contain original content (mock changes don't affect file mtime)")
	}

	if strings.Contains(prompt2, "New content from loader") {
		t.Error("Cache should not have updated - file didn't change")
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

func TestBuildPromptCached_SkillsDetection(t *testing.T) {
	tmpDir := t.TempDir()

	InvalidatePromptCache()

	mockSkills := &mockSkillsLoader{
		summaryFn: func(ctx context.Context) (string, error) {
			return "Test skill summary from loader", nil
		},
	}

	prompt := BuildPromptCached(tmpDir, mockSkills, nil)

	if !strings.Contains(prompt, "Test skill summary from loader") {
		t.Error("Prompt should contain skill summary from loader")
	}
}

func TestBuildPromptCached_FileBasedInvalidation(t *testing.T) {
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

	if !sourceFilesChanged(tmpDir, baseline) {
		t.Error("Should detect change after mtime update")
	}
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

	memDir := filepath.Join(tmpDir, "memory")
	_ = os.Mkdir(memDir, 0o755)
	_ = os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("memory"), 0o644)

	skillsDir := filepath.Join(tmpDir, "skills", "test")
	_ = os.MkdirAll(skillsDir, 0o755)
	_ = os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("skill"), 0o644)

	files := collectSourceFiles(tmpDir)

	expectedFiles := []string{
		filepath.Join(tmpDir, "AGENTS.md"),
		filepath.Join(tmpDir, "USER.md"),
		filepath.Join(tmpDir, "memory", "MEMORY.md"),
		filepath.Join(tmpDir, "skills", "test", "SKILL.md"),
	}

	for _, expected := range expectedFiles {
		if !files[expected] {
			t.Errorf("Expected file to exist: %s", expected)
		}
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
