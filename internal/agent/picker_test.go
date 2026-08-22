package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// The picker draws exactly what the text list names, so a button can never
// offer something /model would refuse. Every Spec must round-trip through
// resolveModelSpec and the session's current entry must be marked.
func TestModelChoicesModelCentric(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	cfg.ModelsConfig.Models = append(cfg.ModelsConfig.Models, config.ModelConfig{Name: "off", Model: "x/y", Disabled: true})
	a := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	if _, err := a.Process(context.Background(), cmdMsg("/model fast")); err != nil {
		t.Fatal(err)
	}
	choices, err := a.ModelChoices(context.Background(), cmdMsg("/model"))
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 2 {
		t.Fatalf("disabled model must be left out, got %+v", choices)
	}
	for _, c := range choices {
		if _, err := a.resolveModelSpec(c.Spec); err != nil {
			t.Errorf("spec %q does not resolve: %v", c.Spec, err)
		}
		if c.Current != (c.Spec == "fast") {
			t.Errorf("current marker wrong on %+v", c)
		}
		if !strings.Contains(c.Label, c.Spec) {
			t.Errorf("label %q should name the spec", c.Label)
		}
	}
}

func TestModelChoicesLegacyProviders(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "z-ai/glm-5.2"
	cfg.Providers = map[string]config.ProviderConfig{
		"nvidia":   {Enabled: true, APIKey: "k", Model: "z-ai/glm-5.2"},
		"poolside": {Enabled: true, APIKey: "k", Model: "poolside/laguna-s-2.1"},
		"groq":     {Enabled: true}, // no key: not switchable
		"openai":   {Enabled: false, APIKey: "k"},
	}
	a := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	if _, err := a.Process(context.Background(), cmdMsg("/model poolside")); err != nil {
		t.Fatal(err)
	}
	choices, err := a.ModelChoices(context.Background(), cmdMsg("/model"))
	if err != nil {
		t.Fatal(err)
	}
	var specs []string
	for _, c := range choices {
		specs = append(specs, c.Spec)
		if _, err := a.resolveModelSpec(c.Spec); err != nil {
			t.Errorf("spec %q does not resolve: %v", c.Spec, err)
		}
		if c.Current != (c.Spec == "poolside") {
			t.Errorf("current marker wrong on %+v", c)
		}
	}
	if got := strings.Join(specs, ","); got != "nvidia,poolside" {
		t.Errorf("choices = %q, want the keyed, enabled providers in sorted order", got)
	}
}

func TestPersonalityChoices(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	a := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	choices, err := a.PersonalityChoices(context.Background(), cmdMsg("/personality"))
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != len(personalityPresets)+1 || choices[len(choices)-1].Spec != "none" || !choices[len(choices)-1].Current {
		t.Fatalf("fresh session: presets plus a current 'none', got %+v", choices)
	}
	if _, err := a.Process(context.Background(), cmdMsg("/personality pirate")); err != nil {
		t.Fatal(err)
	}
	choices, _ = a.PersonalityChoices(context.Background(), cmdMsg("/personality"))
	for _, c := range choices {
		if c.Current != (c.Spec == "pirate") {
			t.Errorf("current marker wrong on %+v", c)
		}
	}
}

// The switch reply states the keep-history decision (#313): a button one tap
// away must not wipe the transcript, so the text says the conversation
// continues and how to start fresh.
func TestModelSwitchReplySaysHistoryIsKept(t *testing.T) {
	InvalidatePromptCache()
	a := NewAgent(modelCentricCfg(t), &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	resp, err := a.Process(context.Background(), cmdMsg("/model fast"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp, "conversation continues") || !strings.Contains(resp, "/new") {
		t.Errorf("switch reply should name the keep-history behaviour and /new: %q", resp)
	}
}
