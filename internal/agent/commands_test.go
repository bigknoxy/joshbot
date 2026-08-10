package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/session"
)

// cmdMsg builds an inbound slash command message.
func cmdMsg(content string) bus.InboundMessage {
	return bus.InboundMessage{
		SenderID:  "user",
		Channel:   "cli",
		Content:   content,
		Timestamp: time.Now(),
	}
}

// modelCentricCfg returns a model-centric config with two named models.
func modelCentricCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.ModelsConfig = config.ModelsConfig{
		Models: []config.ModelConfig{
			{Name: "smart", Model: "nvidia/stepfun-ai/step-3.5-flash"},
			{Name: "fast", Model: "groq/llama-3.3-70b-versatile"},
		},
		Agent: config.AgentModelConfig{Model: "smart"},
	}
	return cfg
}

func TestModelCommandListsModels(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	provider := &scriptedProvider{}
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	resp, err := agent.Process(context.Background(), cmdMsg("/model"))
	if err != nil {
		t.Fatalf("Process(/model) returned %v", err)
	}
	for _, want := range []string{"Current model: smart", "smart - nvidia/stepfun-ai/step-3.5-flash", "fast - groq/llama-3.3-70b-versatile", "--global"} {
		if !strings.Contains(resp, want) {
			t.Errorf("/model list missing %q:\n%s", want, resp)
		}
	}
}

func TestModelCommandSwitchesSession(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	provider := &scriptedProvider{}
	sessions := newMockSessionManager()
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, sessions, newMockLogger())

	resp, err := agent.Process(context.Background(), cmdMsg("/model fast"))
	if err != nil {
		t.Fatalf("Process(/model fast) returned %v", err)
	}
	if !strings.Contains(resp, "fast") {
		t.Errorf("expected switch confirmation to name fast, got %q", resp)
	}

	sess, err := sessions.Load(context.Background(), "cli:user")
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if sess.ModelOverride != "fast" {
		t.Errorf("session model override = %q, want fast", sess.ModelOverride)
	}

	// A subsequent ordinary message must be routed to the overridden model.
	if _, err := agent.Process(context.Background(), cmdMsg("hello there")); err != nil {
		t.Fatalf("Process(hello) returned %v", err)
	}
	provider.mu.Lock()
	lastModel := ""
	if len(provider.requests) > 0 {
		lastModel = provider.requests[len(provider.requests)-1].Model
	}
	provider.mu.Unlock()
	if lastModel != "fast" {
		t.Errorf("next request used model %q, want session override fast", lastModel)
	}
}

func TestModelCommandAcceptsModelID(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	agent := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	resp, err := agent.Process(context.Background(), cmdMsg("/model groq/llama-3.3-70b-versatile"))
	if err != nil {
		t.Fatalf("Process returned %v", err)
	}
	if !strings.Contains(resp, "fast") {
		t.Errorf("a model ID should resolve to its configured name (fast), got %q", resp)
	}
}

func TestModelCommandRejectsUnknown(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	agent := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	resp, err := agent.Process(context.Background(), cmdMsg("/model does-not-exist"))
	if err != nil {
		t.Fatalf("Process returned %v", err)
	}
	if !strings.Contains(resp, "unknown model") {
		t.Errorf("expected an unknown-model error, got %q", resp)
	}
}

func TestModelCommandGlobalPersistsConfig(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)

	// Redirect the config path so Save does not touch a real install.
	origHome, origWorkspace := config.DefaultHome, config.DefaultWorkspace
	config.SetHome(t.TempDir())
	t.Cleanup(func() { config.SetHome(origHome); _ = origWorkspace })

	sessions := newMockSessionManager()
	agent := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger())

	resp, err := agent.Process(context.Background(), cmdMsg("/model fast --global"))
	if err != nil {
		t.Fatalf("Process(/model fast --global) returned %v", err)
	}
	if !strings.Contains(resp, "all sessions") {
		t.Errorf("expected a global-change confirmation, got %q", resp)
	}

	// The running process must route every new session to the new default.
	if _, err := agent.Process(context.Background(), cmdMsg("hi")); err != nil {
		t.Fatalf("Process(hi) returned %v", err)
	}

	// And the change must be on disk for the next boot.
	data, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if !strings.Contains(string(data), `"name": "fast"`) {
		t.Errorf("saved config does not reflect the new default:\n%s", data)
	}
}

func TestPersonalityCommandPresetAndCustom(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	sessions := newMockSessionManager()
	agent := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger())

	resp, err := agent.Process(context.Background(), cmdMsg("/personality pirate"))
	if err != nil {
		t.Fatalf("Process returned %v", err)
	}
	if !strings.Contains(resp, "pirate") {
		t.Errorf("expected the preset text in the confirmation, got %q", resp)
	}

	sess, _ := sessions.Load(context.Background(), "cli:user")
	if sess.Personality == "" {
		t.Fatal("personality was not stored on the session")
	}

	// Show current personality.
	resp, _ = agent.Process(context.Background(), cmdMsg("/personality"))
	if !strings.Contains(resp, "pirate") {
		t.Errorf("show-personality should mention the current personality, got %q", resp)
	}

	// A custom instruction is used verbatim.
	_, _ = agent.Process(context.Background(), cmdMsg("/personality answer in haiku"))
	sess, _ = sessions.Load(context.Background(), "cli:user")
	if sess.Personality != "answer in haiku" {
		t.Errorf("custom personality = %q, want verbatim", sess.Personality)
	}

	// none clears it.
	_, _ = agent.Process(context.Background(), cmdMsg("/personality none"))
	sess, _ = sessions.Load(context.Background(), "cli:user")
	if sess.Personality != "" {
		t.Errorf("personality not cleared: %q", sess.Personality)
	}
}

func TestPersonalityInjectedIntoSystemPrompt(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &scriptedProvider{}
	agent := NewAgent(cfg, provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	if _, err := agent.Process(context.Background(), cmdMsg("/personality technical")); err != nil {
		t.Fatalf("Process returned %v", err)
	}
	if _, err := agent.Process(context.Background(), cmdMsg("explain this code")); err != nil {
		t.Fatalf("Process returned %v", err)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) == 0 {
		t.Fatal("no request recorded")
	}
	system := provider.requests[len(provider.requests)-1].Messages[0].Content
	if !strings.Contains(system, "<personality>") || !strings.Contains(system, "technical audience") {
		t.Errorf("system prompt missing the personality block:\n%s", system)
	}
}

func TestCompactCommandCompactsSession(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "small"
	comp := &countingCompressor{summary: "a real summary of the conversation"}
	sessions := newMockSessionManager()
	agent := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger(),
		WithBudgetManager(ctxpkg.NewBudgetManager(ctxpkg.NewRegistry(), 3800)),
		WithContextCompressor(comp),
	)

	sess, _ := sessions.GetOrCreate(context.Background(), "cli:user")
	for i := 0; i < 5; i++ {
		sess.AddMessage(session.Message{Role: session.RoleUser, Content: fmt.Sprintf("message number %d", i), Timestamp: time.Now()})
	}
	if err := sessions.Save(context.Background(), sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	resp, err := agent.Process(context.Background(), cmdMsg("/compact"))
	if err != nil {
		t.Fatalf("Process(/compact) returned %v", err)
	}
	if !strings.Contains(resp, "Compressed") {
		t.Errorf("expected a compaction confirmation, got %q", resp)
	}
	if comp.count() != 1 {
		t.Errorf("compressor calls = %d, want exactly 1", comp.count())
	}

	loaded, _ := sessions.Load(context.Background(), "cli:user")
	if len(loaded.Messages) != 1 || !loaded.Messages[0].Compaction {
		t.Fatalf("session should hold exactly one compaction record after /compact, got %d messages", len(loaded.Messages))
	}
	if loaded.Messages[0].Content != session.CompactionEnvelope("a real summary of the conversation") {
		t.Errorf("compaction record content = %q", loaded.Messages[0].Content)
	}
}

func TestNewClearsSessionOverrideAndPersonality(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	sessions := newMockSessionManager()
	agent := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger())

	sess, _ := sessions.GetOrCreate(context.Background(), "cli:user")
	sess.AddMessage(session.Message{Role: session.RoleUser, Content: "hi", Timestamp: time.Now()})
	sess.ModelOverride = "fast"
	sess.Personality = "be terse"
	sessions.Save(context.Background(), sess)

	if _, err := agent.Process(context.Background(), cmdMsg("/new")); err != nil {
		t.Fatalf("Process(/new) returned %v", err)
	}

	loaded, _ := sessions.Load(context.Background(), "cli:user")
	if loaded.ModelOverride != "" {
		t.Errorf("/new kept the session model override %q", loaded.ModelOverride)
	}
	if loaded.Personality != "" {
		t.Errorf("/new kept the personality %q", loaded.Personality)
	}
}

func TestStatusShowsSessionModel(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	sessions := newMockSessionManager()
	agent := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger())

	if _, err := agent.Process(context.Background(), cmdMsg("/model fast")); err != nil {
		t.Fatalf("Process(/model fast) returned %v", err)
	}
	resp, err := agent.Process(context.Background(), cmdMsg("/status"))
	if err != nil {
		t.Fatalf("Process(/status) returned %v", err)
	}
	if !strings.Contains(resp, "Model: fast") {
		t.Errorf("/status should show the session override, got %q", resp)
	}
}
