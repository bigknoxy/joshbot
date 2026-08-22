package agent

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
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

// The derived topic hint used to be the user's words, lowercased, cut
// mid-word at 60 bytes with "..." appended, and injected as "Current topic:"
// — which a model read as a message from the user that broke off, and
// answered that instead of the real question (a live Telegram transcript:
// "you were saying 'wanting to do a new topi...' and then radio silence").
func TestInferTopicCutsOnAWordWithNoEllipsisAndKeepsCase(t *testing.T) {
	in := "Ha! You son of a gun!  I'm actually wanting to do a new topic about the weather in Wichita"
	got := inferTopic(in)
	if len(got) > topicMaxLen || strings.HasSuffix(got, "...") || strings.HasSuffix(got, "topi") {
		t.Errorf("topic = %q: must be ≤%d bytes, cut on a word, no ellipsis", got, topicMaxLen)
	}
	if !strings.HasPrefix(got, "Ha! You son") {
		t.Errorf("topic = %q: casing must be the user's", got)
	}
	if inferTopic("tell me about Go generics") != "Go generics" {
		t.Errorf("question-word strip broke: %q", inferTopic("tell me about Go generics"))
	}
	if inferTopic(strings.Repeat("x", 80)) != strings.Repeat("x", topicMaxLen) {
		t.Errorf("a single long word is hard-cut: %q", inferTopic(strings.Repeat("x", 80)))
	}
	// strings.ToLower changes byte length for İ (2 → 3 bytes): the prefix
	// strip must index the original by the prefix's own length, not by an
	// offset taken from the lowered copy.
	if got := inferTopic("What İstanbul is famous for"); got != "İstanbul is famous for" {
		t.Errorf("length-changing rune misaligned the strip: %q", got)
	}
	// A hard cut through CJK text must land on a rune boundary.
	if got := inferTopic(strings.Repeat("日本語", 30)); !utf8.ValidString(got) || len(got) > topicMaxLen {
		t.Errorf("hard cut produced invalid UTF-8 or overran: %q (%d bytes)", got, len(got))
	}
}

// A 404/410 from the addressed provider is a retired model, not an outage;
// the notice says so and names the recovery instead of reporting a status.
func TestFallbackNoticeNamesARetiredModel(t *testing.T) {
	got := formatFallbackNotice(providers.FallbackNotice{From: "nvidia", To: "poolside", Model: "poolside/laguna-s-2.1", Reason: "http_410"})
	if !strings.Contains(got, "no longer serves this model") || !strings.Contains(got, "/model") {
		t.Errorf("notice = %q", got)
	}
	if got := formatFallbackNotice(providers.FallbackNotice{From: "nvidia", To: "poolside", Reason: "rate_limit"}); strings.Contains(got, "/model") {
		t.Errorf("a rate limit is transient and must not suggest switching: %q", got)
	}
}
