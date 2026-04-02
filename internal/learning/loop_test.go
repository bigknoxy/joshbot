package learning

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewLoop(t *testing.T) {
	cfg := DefaultLoopConfig("/tmp/test-joshbot-loop")
	loop := NewLoop(nil, nil, nil, cfg)
	if loop == nil {
		t.Fatal("expected non-nil loop")
	}
	if loop.config.ExperienceWindow != 20 {
		t.Errorf("expected ExperienceWindow=20, got %d", loop.config.ExperienceWindow)
	}
	if loop.config.MaxExperiences != 50 {
		t.Errorf("expected MaxExperiences=50, got %d", loop.config.MaxExperiences)
	}
}

func TestDefaultLoopConfig(t *testing.T) {
	cfg := DefaultLoopConfig("/workspace")
	if cfg.SkillDir != "/workspace/skills" {
		t.Errorf("expected SkillDir=/workspace/skills, got %s", cfg.SkillDir)
	}
	if cfg.UserModelPath != "/workspace/USER.md" {
		t.Errorf("expected UserModelPath=/workspace/USER.md, got %s", cfg.UserModelPath)
	}
	if cfg.ConsolidationInterval != 15*time.Minute {
		t.Errorf("expected 15m interval, got %v", cfg.ConsolidationInterval)
	}
}

func TestLoopRunOnce_NilMemory(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))
	err := loop.RunOnce(context.Background())
	if err == nil {
		t.Error("expected error for nil memory manager")
	}
}

func TestLoopLogExperience(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))

	loop.LogExperience(Experience{
		Pattern:   "code review",
		Outcome:   "success",
		ToolsUsed: []string{"read_file", "grep"},
	})

	if len(loop.experiences) != 1 {
		t.Errorf("expected 1 experience, got %d", len(loop.experiences))
	}
	if loop.experiences[0].Pattern != "code review" {
		t.Errorf("expected pattern 'code review', got %s", loop.experiences[0].Pattern)
	}
	if loop.experiences[0].Timestamp == "" {
		t.Error("expected timestamp to be set")
	}
}

func TestLoopLogExperience_TrimsToMax(t *testing.T) {
	cfg := DefaultLoopConfig("/tmp")
	cfg.MaxExperiences = 3
	loop := NewLoop(nil, nil, nil, cfg)

	for i := 0; i < 5; i++ {
		loop.LogExperience(Experience{Pattern: "pattern", Outcome: "success"})
	}

	if len(loop.experiences) != 3 {
		t.Errorf("expected 3 experiences (max), got %d", len(loop.experiences))
	}
	// Should keep the most recent ones
	if loop.experiences[0].Pattern != "pattern" {
		t.Error("expected most recent experiences retained")
	}
}

func TestLoopRateSkill(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))

	loop.RateSkill("test-skill", true, 8.0)
	loop.RateSkill("test-skill", true, 9.0)
	loop.RateSkill("test-skill", false, 3.0)

	r := loop.ratings["test-skill"]
	if r == nil {
		t.Fatal("expected rating for test-skill")
	}
	if r.UsageCount != 3 {
		t.Errorf("expected UsageCount=3, got %d", r.UsageCount)
	}
	if r.SuccessCount != 2 {
		t.Errorf("expected SuccessCount=2, got %d", r.SuccessCount)
	}
	if r.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", r.FailCount)
	}
	// Average should be (8+9+3)/3 = 6.67
	if r.AvgRating < 6.0 || r.AvgRating > 7.0 {
		t.Errorf("expected avg rating ~6.67, got %.2f", r.AvgRating)
	}
}

func TestLoopGetPreferences(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))
	loop.preferences = []UserPreference{
		{Category: "technical", Key: "language", Value: "Go"},
	}

	prefs := loop.GetPreferences()
	if len(prefs) != 1 {
		t.Errorf("expected 1 preference, got %d", len(prefs))
	}
	if prefs[0].Value != "Go" {
		t.Errorf("expected value 'Go', got %s", prefs[0].Value)
	}
}

func TestIdentifyPatterns_NotEnoughData(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))
	loop.experiences = []Experience{
		{Pattern: "one", Outcome: "success"},
		{Pattern: "two", Outcome: "success"},
	}

	patterns, err := loop.identifyPatterns(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if patterns != nil {
		t.Error("expected nil patterns with < 3 experiences")
	}
}

func TestIdentifyPatterns_NilProvider(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))
	loop.experiences = []Experience{
		{Pattern: "one", Outcome: "success"},
		{Pattern: "two", Outcome: "success"},
		{Pattern: "three", Outcome: "success"},
	}

	patterns, err := loop.identifyPatterns(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if patterns != nil {
		t.Error("expected nil patterns with nil provider")
	}
}

func TestGenerateSkills_EmptyPatterns(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))
	err := loop.generateSkills(context.Background(), []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGenerateSkills_NilProvider(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultLoopConfig(tmpDir)
	loop := NewLoop(nil, nil, nil, cfg)

	err := loop.generateSkills(context.Background(), []string{"test pattern"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not create any files without a provider
	skillPath := filepath.Join(tmpDir, "skills", "test-pattern", "SKILL.md")
	if _, err := os.Stat(skillPath); err == nil {
		t.Error("expected no skill file created without provider")
	}
}

func TestUpdateUserModel_NilProvider(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))
	err := loop.updateUserModel(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRateAndImproveSkills_NilLoader(t *testing.T) {
	loop := NewLoop(nil, nil, nil, DefaultLoopConfig("/tmp"))
	err := loop.rateAndImproveSkills(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoopStartStop(t *testing.T) {
	cfg := DefaultLoopConfig("/tmp")
	cfg.ConsolidationInterval = 100 * time.Millisecond // fast for testing
	loop := NewLoop(nil, nil, nil, cfg)

	loop.Start()
	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)
	loop.Stop()
	// Should not hang or panic
}
