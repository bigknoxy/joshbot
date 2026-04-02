package learning

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/skills"
)

// Experience represents a logged interaction pattern
type Experience struct {
	Timestamp    string   `json:"timestamp"`
	Pattern      string   `json:"pattern"`
	Outcome      string   `json:"outcome"` // success, failure, partial
	ToolsUsed    []string `json:"tools_used"`
	Duration     string   `json:"duration"`
	UserFeedback string   `json:"user_feedback,omitempty"`
}

// SkillRating tracks how well a skill performs
type SkillRating struct {
	Name         string    `json:"name"`
	UsageCount   int       `json:"usage_count"`
	SuccessCount int       `json:"success_count"`
	FailCount    int       `json:"fail_count"`
	LastUsed     time.Time `json:"last_used"`
	AvgRating    float64   `json:"avg_rating"` // 0-10
}

// UserPreference represents a learned user preference
type UserPreference struct {
	Category    string  `json:"category"` // communication, technical, workflow, domain
	Key         string  `json:"key"`
	Value       string  `json:"value"`
	Confidence  float64 `json:"confidence"` // 0.0-1.0
	LastUpdated string  `json:"last_updated"`
	Source      string  `json:"source"` // explicit, implicit, inferred
}

// LoopConfig configures the learning loop
type LoopConfig struct {
	ConsolidationInterval time.Duration
	ExperienceWindow      int     // number of recent interactions to analyze
	MinConfidence         float64 // minimum confidence to auto-apply (0.0-1.0)
	SkillDir              string  // path to workspace/skills/
	UserModelPath         string  // path to USER.md
	MaxExperiences        int     // max experiences to keep
	MaxPreferences        int     // max preferences to track
}

// DefaultLoopConfig returns sensible defaults
func DefaultLoopConfig(workspaceDir string) LoopConfig {
	return LoopConfig{
		ConsolidationInterval: 15 * time.Minute,
		ExperienceWindow:      20,
		MinConfidence:         0.7,
		SkillDir:              filepath.Join(workspaceDir, "skills"),
		UserModelPath:         filepath.Join(workspaceDir, "USER.md"),
		MaxExperiences:        50,
		MaxPreferences:        30,
	}
}

// Loop implements the Hermes-style learning loop:
// experience -> pattern extraction -> skill creation -> user modeling -> improvement
type Loop struct {
	mem         *memory.Manager
	provider    providers.Provider
	skillLoader *skills.Loader
	config      LoopConfig
	stopCh      chan struct{}
	wg          sync.WaitGroup
	mu          sync.RWMutex

	// In-memory state
	experiences []Experience
	ratings     map[string]*SkillRating
	preferences []UserPreference
}

// NewLoop creates a new learning loop
func NewLoop(mem *memory.Manager, provider providers.Provider, skillLoader *skills.Loader, cfg LoopConfig) *Loop {
	return &Loop{
		mem:         mem,
		provider:    provider,
		skillLoader: skillLoader,
		config:      cfg,
		stopCh:      make(chan struct{}),
		ratings:     make(map[string]*SkillRating),
	}
}

// Start begins the learning loop
func (l *Loop) Start() {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		ticker := time.NewTicker(l.config.ConsolidationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := l.RunOnce(context.Background()); err != nil {
					// best-effort: ignore errors
					_ = err
				}
			case <-l.stopCh:
				return
			}
		}
	}()
}

// Stop halts the learning loop
func (l *Loop) Stop() {
	close(l.stopCh)
	l.wg.Wait()
}

// RunOnce performs a single learning pass
func (l *Loop) RunOnce(ctx context.Context) error {
	if l.mem == nil {
		return fmt.Errorf("no memory manager")
	}

	// Phase 1: Extract experiences from recent history
	if err := l.extractExperiences(ctx); err != nil {
		return fmt.Errorf("extract experiences: %w", err)
	}

	// Phase 2: Identify patterns from experiences
	patterns, err := l.identifyPatterns(ctx)
	if err != nil {
		return fmt.Errorf("identify patterns: %w", err)
	}

	// Phase 3: Generate skills from patterns
	if err := l.generateSkills(ctx, patterns); err != nil {
		return fmt.Errorf("generate skills: %w", err)
	}

	// Phase 4: Update user model
	if err := l.updateUserModel(ctx); err != nil {
		return fmt.Errorf("update user model: %w", err)
	}

	// Phase 5: Rate and improve existing skills
	if err := l.rateAndImproveSkills(ctx); err != nil {
		return fmt.Errorf("rate and improve skills: %w", err)
	}

	return nil
}

// LogExperience records a single interaction experience
func (l *Loop) LogExperience(exp Experience) {
	l.mu.Lock()
	defer l.mu.Unlock()
	exp.Timestamp = time.Now().UTC().Format("2006-01-02 15:04:05")
	l.experiences = append(l.experiences, exp)
	// Trim to max
	if len(l.experiences) > l.config.MaxExperiences {
		l.experiences = l.experiences[len(l.experiences)-l.config.MaxExperiences:]
	}
}

// RateSkill records the outcome of a skill execution
func (l *Loop) RateSkill(name string, success bool, rating float64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	r, ok := l.ratings[name]
	if !ok {
		r = &SkillRating{
			Name: name,
		}
		l.ratings[name] = r
	}

	r.UsageCount++
	r.LastUsed = time.Now()
	if success {
		r.SuccessCount++
	} else {
		r.FailCount++
	}

	// Running average
	if rating > 0 {
		r.AvgRating = (r.AvgRating*float64(r.UsageCount-1) + rating) / float64(r.UsageCount)
	}
}

// GetPreferences returns current user preferences
func (l *Loop) GetPreferences() []UserPreference {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.preferences
}

// extractExperiences pulls interaction data from history
func (l *Loop) extractExperiences(ctx context.Context) error {
	hist, err := l.mem.LoadHistory(ctx, "")
	if err != nil {
		return err
	}
	if strings.TrimSpace(hist) == "" {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(hist), "\n")
	n := l.config.ExperienceWindow
	if len(lines) < n {
		n = len(lines)
	}
	recent := strings.Join(lines[len(lines)-n:], "\n")

	if l.provider == nil {
		return nil
	}

	prompt := fmt.Sprintf(`Analyze these recent interactions and extract structured experiences.
For each interaction, identify:
1. The pattern/task type (e.g., "code review", "debugging", "research", "writing")
2. Tools used (if mentioned)
3. Outcome (success/failure/partial)
4. Duration estimate (short/medium/long)
5. Any explicit user feedback

Return as a JSON array of objects with fields: pattern, tools_used (array), outcome, duration, user_feedback.
Only include interactions where tools were used or the conversation was substantive.

Recent interactions:
%s`, recent)

	req := providers.ChatRequest{
		Model:       l.provider.Config().Model,
		Messages:    []providers.Message{{Role: providers.RoleUser, Content: prompt}},
		MaxTokens:   1000,
		Temperature: 0.1,
	}
	resp, err := l.provider.Chat(ctx, req)
	if err != nil {
		return err
	}
	if len(resp.Choices) == 0 {
		return nil
	}

	// Parse the response
	content := resp.Choices[0].Message.Content
	if content == "" {
		return nil
	}

	// Log extracted experiences
	l.mu.Lock()
	defer l.mu.Unlock()

	// Try to extract JSON array from response (may be wrapped in markdown code blocks)
	jsonContent := content
	if idx := strings.Index(content, "```"); idx >= 0 {
		// Extract from code block
		rest := content[idx+3:]
		if endIdx := strings.Index(rest, "```"); endIdx > 0 {
			jsonContent = rest[:endIdx]
		}
	}

	// Parse JSON array of experiences
	content = strings.TrimSpace(jsonContent)
	if strings.HasPrefix(content, "[") {
		// Simple JSON parsing: extract pattern and outcome from each object
		for _, obj := range strings.Split(content, "}") {
			obj = strings.TrimSpace(obj)
			if obj == "" || obj == "[" || obj == "]" {
				continue
			}
			exp := Experience{Outcome: "success"}
			// Extract pattern
			if idx := strings.Index(obj, `"pattern"`); idx >= 0 {
				rest := obj[idx:]
				if start := strings.Index(rest, `": "`); start >= 0 {
					rest = rest[start+4:]
					if end := strings.Index(rest, `"`); end >= 0 {
						exp.Pattern = rest[:end]
					}
				}
			}
			// Extract outcome
			if idx := strings.Index(obj, `"outcome"`); idx >= 0 {
				rest := obj[idx:]
				if start := strings.Index(rest, `": "`); start >= 0 {
					rest = rest[start+4:]
					if end := strings.Index(rest, `"`); end >= 0 {
						exp.Outcome = rest[:end]
					}
				}
			}
			// Extract tools_used
			if idx := strings.Index(obj, `"tools_used"`); idx >= 0 {
				rest := obj[idx:]
				if start := strings.Index(rest, `[`); start >= 0 {
					rest = rest[start+1:]
					if end := strings.Index(rest, `]`); end >= 0 {
						toolsStr := rest[:end]
						for _, tool := range strings.Split(toolsStr, ",") {
							tool = strings.Trim(tool, ` "'`)
							if tool != "" {
								exp.ToolsUsed = append(exp.ToolsUsed, tool)
							}
						}
					}
				}
			}
			if exp.Pattern != "" {
				l.experiences = append(l.experiences, exp)
			}
		}
	} else {
		// Fallback: parse as plain text lines
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "- ") {
				continue
			}
			if strings.Contains(strings.ToLower(line), "pattern") || strings.Contains(strings.ToLower(line), "outcome") {
				exp := Experience{
					Pattern:   line,
					Outcome:   "success",
					ToolsUsed: []string{},
				}
				l.experiences = append(l.experiences, exp)
			}
		}
	}

	// Trim to max
	if len(l.experiences) > l.config.MaxExperiences {
		l.experiences = l.experiences[len(l.experiences)-l.config.MaxExperiences:]
	}

	return nil
}

// identifyPatterns finds recurring patterns in experiences
func (l *Loop) identifyPatterns(ctx context.Context) ([]string, error) {
	l.mu.RLock()
	exps := make([]Experience, len(l.experiences))
	copy(exps, l.experiences)
	l.mu.RUnlock()

	if len(exps) < 3 {
		return nil, nil // not enough data
	}

	if l.provider == nil {
		return nil, nil
	}

	// Format experiences for analysis
	var expText strings.Builder
	for i, exp := range exps {
		expText.WriteString(fmt.Sprintf("%d. Pattern: %s, Outcome: %s, Tools: %v\n",
			i+1, exp.Pattern, exp.Outcome, exp.ToolsUsed))
	}

	prompt := fmt.Sprintf(`Analyze these %d interaction experiences and identify 3-5 recurring patterns or workflows.
For each pattern:
- Give it a concise name (2-4 words)
- Describe when it applies (1 sentence)
- List the steps or tools typically used

Return as a numbered list. Only include patterns that appeared 2+ times.

Experiences:
%s`, len(exps), expText.String())

	req := providers.ChatRequest{
		Model:       l.provider.Config().Model,
		Messages:    []providers.Message{{Role: providers.RoleUser, Content: prompt}},
		MaxTokens:   800,
		Temperature: 0.2,
	}
	resp, err := l.provider.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, nil
	}

	content := resp.Choices[0].Message.Content
	if content == "" {
		return nil, nil
	}

	// Extract pattern names
	var patterns []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Look for numbered items or bullet points
		if strings.HasPrefix(line, "- ") || (len(line) > 2 && line[1] == '.' && line[0] >= '0' && line[0] <= '9') {
			// Extract the pattern name
			cleaned := strings.TrimPrefix(line, "- ")
			if idx := strings.Index(cleaned, ":"); idx > 0 {
				patterns = append(patterns, strings.TrimSpace(cleaned[:idx]))
			} else {
				patterns = append(patterns, cleaned)
			}
		}
	}

	return patterns, nil
}

// generateSkills creates SKILL.md files for identified patterns
func (l *Loop) generateSkills(ctx context.Context, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}

	if l.provider == nil {
		return nil
	}

	// Check which skills already exist
	existingSkills := make(map[string]bool)
	if l.skillLoader != nil {
		for _, skill := range l.skillLoader.List() {
			existingSkills[strings.ToLower(skill.Name)] = true
		}
	}

	for _, pattern := range patterns {
		skillName := strings.ToLower(strings.ReplaceAll(pattern, " ", "-"))
		if existingSkills[skillName] {
			continue // skip existing skills
		}

		skillPath := filepath.Join(l.config.SkillDir, skillName, "SKILL.md")
		if _, err := os.Stat(skillPath); err == nil {
			continue // file already exists
		}

		// Generate skill content
		prompt := fmt.Sprintf(`Create a skill definition for the pattern: "%s"

This skill should help an AI assistant recognize and execute this pattern effectively.

Return a SKILL.md file with:
1. YAML frontmatter: name, description, tags
2. Body: When to use this skill, step-by-step instructions, common pitfalls, examples

Keep it concise (under 500 words). Focus on actionable guidance.`, pattern)

		req := providers.ChatRequest{
			Model:       l.provider.Config().Model,
			Messages:    []providers.Message{{Role: providers.RoleUser, Content: prompt}},
			MaxTokens:   1500,
			Temperature: 0.3,
		}
		resp, err := l.provider.Chat(ctx, req)
		if err != nil {
			log.Debug("Learning loop: failed to generate skill content for pattern %q: %v", pattern, err)
			continue
		}
		if len(resp.Choices) == 0 {
			log.Debug("Learning loop: empty response for skill generation pattern %q", pattern)
			continue
		}

		content := resp.Choices[0].Message.Content
		if content == "" {
			continue
		}

		// Ensure skill directory exists
		if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
			log.Debug("Learning loop: failed to create skill directory %q: %v", skillPath, err)
			continue
		}

		// Write skill file
		if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
			log.Debug("Learning loop: failed to write skill file %q: %v", skillPath, err)
			continue
		}

		log.Debug("Learning loop: generated new skill %q from pattern %q", skillName, pattern)
	}

	return nil
}

// updateUserModel extracts user preferences and updates USER.md
func (l *Loop) updateUserModel(ctx context.Context) error {
	if l.provider == nil {
		return nil
	}

	hist, err := l.mem.LoadHistory(ctx, "")
	if err != nil {
		return err
	}
	if strings.TrimSpace(hist) == "" {
		return nil
	}

	// Take recent history for analysis
	lines := strings.Split(strings.TrimSpace(hist), "\n")
	n := 30 // analyze more lines for user modeling
	if len(lines) < n {
		n = len(lines)
	}
	recent := strings.Join(lines[len(lines)-n:], "\n")

	prompt := fmt.Sprintf(`Analyze these conversations and extract 3-5 new facts about the user's preferences, working style, or domain knowledge.
Focus on:
- Communication preferences (format, tone, detail level)
- Technical preferences (languages, frameworks, tools)
- Workflow preferences (how they like to work, review processes)
- Domain knowledge areas
- Explicit statements about what they want/don't want

Return each fact as a single line starting with "- ". Be specific and actionable.

Recent conversations:
%s`, recent)

	req := providers.ChatRequest{
		Model:       l.provider.Config().Model,
		Messages:    []providers.Message{{Role: providers.RoleUser, Content: prompt}},
		MaxTokens:   500,
		Temperature: 0.1,
	}
	resp, err := l.provider.Chat(ctx, req)
	if err != nil {
		return err
	}
	if len(resp.Choices) == 0 {
		return nil
	}

	content := resp.Choices[0].Message.Content
	if content == "" {
		return nil
	}

	// Read existing USER.md
	existingUserModel := ""
	if data, err := os.ReadFile(l.config.UserModelPath); err == nil {
		existingUserModel = string(data)
	}

	// Extract new preferences
	var newPrefs []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") && len(line) > 2 {
			// Check for duplicates
			isDup := false
			for _, existing := range strings.Split(existingUserModel, "\n") {
				if strings.Contains(existing, line[2:]) {
					isDup = true
					break
				}
			}
			if !isDup {
				newPrefs = append(newPrefs, line)
			}
		}
	}

	if len(newPrefs) == 0 {
		return nil
	}

	// Update USER.md
	var updatedModel strings.Builder
	if existingUserModel != "" {
		updatedModel.WriteString(existingUserModel)
		if !strings.HasSuffix(existingUserModel, "\n") {
			updatedModel.WriteString("\n")
		}
		updatedModel.WriteString("\n## Learned Preferences (auto-generated)\n")
	} else {
		updatedModel.WriteString("# User Model\n\n")
		updatedModel.WriteString("## Learned Preferences (auto-generated)\n")
	}

	for _, pref := range newPrefs {
		updatedModel.WriteString(pref + "\n")
	}

	// Write atomically
	tmpPath := l.config.UserModelPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(updatedModel.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, l.config.UserModelPath)
}

// rateAndImproveSkills evaluates existing skills and flags low performers
func (l *Loop) rateAndImproveSkills(ctx context.Context) error {
	if l.skillLoader == nil {
		return nil
	}

	allSkills := l.skillLoader.List()
	if len(allSkills) == 0 {
		return nil
	}

	l.mu.RLock()
	ratings := make(map[string]*SkillRating)
	for k, v := range l.ratings {
		ratings[k] = v
	}
	l.mu.RUnlock()

	// Find low-rated skills
	var lowRated []string
	for _, skill := range allSkills {
		r, ok := ratings[strings.ToLower(skill.Name)]
		if ok && r.UsageCount >= 3 && r.AvgRating < 5.0 {
			lowRated = append(lowRated, skill.Name)
		}
	}

	if len(lowRated) == 0 {
		return nil
	}

	// Generate improvement suggestions
	if l.provider == nil {
		return nil
	}

	prompt := fmt.Sprintf(`These skills are underperforming (low usage or ratings). Suggest specific improvements for each:

%s

For each skill, provide:
1. What might be wrong (unclear instructions? too long? missing examples?)
2. One concrete improvement that would make it more effective

Return as a numbered list.`, strings.Join(lowRated, ", "))

	req := providers.ChatRequest{
		Model:       l.provider.Config().Model,
		Messages:    []providers.Message{{Role: providers.RoleUser, Content: prompt}},
		MaxTokens:   800,
		Temperature: 0.2,
	}
	resp, err := l.provider.Chat(ctx, req)
	if err != nil {
		return err
	}
	if len(resp.Choices) == 0 {
		return nil
	}

	content := resp.Choices[0].Message.Content
	if content == "" {
		return nil
	}

	// Log improvement suggestions to MEMORY.md
	memText, err := l.mem.LoadMemory(ctx)
	if err != nil {
		return err
	}

	improvementSection := fmt.Sprintf("\n## Skill Improvement Suggestions (%s)\n%s\n",
		time.Now().UTC().Format("2006-01-02"), content)

	newMem := memText + improvementSection
	return l.mem.WriteMemory(ctx, newMem)
}
