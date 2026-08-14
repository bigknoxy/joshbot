package providers

import (
	"context"
	"testing"
	"time"
)

func TestFallbackNoticeFiresOnFallbackAnswer(t *testing.T) {
	primary := &countingProvider{
		name:     "primary",
		failWith: &FallbackError{StatusCode: 429, Message: "limited", Provider: "primary"},
		failFor:  1 << 30,
	}
	backup := &countingProvider{name: "backup"}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "primary"})
	installFastClock(mp)
	mp.Register("primary", primary, "model-a", 0, true)
	mp.Register("backup", backup, "model-b", 1, true)

	var got *FallbackNotice
	ctx := WithFallbackNotice(context.Background(), func(n FallbackNotice) { got = &n })

	if _, err := mp.Chat(ctx, ChatRequest{Model: "primary"}); err != nil {
		t.Fatalf("Chat = %v", err)
	}
	if got == nil {
		t.Fatal("no notice fired for a fallback answer")
	}
	if got.From != "primary" || got.To != "backup" || got.Model != "model-b" || got.Reason != "rate_limit" {
		t.Errorf("notice = %+v", *got)
	}
}

func TestFallbackNoticeSilentWhenAddressedAnswers(t *testing.T) {
	p := &countingProvider{name: "primary"}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "primary"})
	installFastClock(mp)
	mp.Register("primary", p, "model-a", 0, true)

	fired := false
	ctx := WithFallbackNotice(context.Background(), func(FallbackNotice) { fired = true })
	if _, err := mp.Chat(ctx, ChatRequest{Model: "primary"}); err != nil {
		t.Fatalf("Chat = %v", err)
	}
	if fired {
		t.Error("notice fired although the addressed provider answered")
	}
}

func TestFallbackNoticeReportsCooldownWhenPrimaryNotDialled(t *testing.T) {
	primary := &countingProvider{
		name:     "primary",
		failWith: &FallbackError{StatusCode: 429, Message: "later", Provider: "primary", RetryAfter: time.Minute},
		failFor:  1 << 30,
	}
	backup := &countingProvider{name: "backup"}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "primary"})
	installFastClock(mp)
	mp.Register("primary", primary, "model-a", 0, true)
	mp.Register("backup", backup, "model-b", 1, true)

	// First turn dials the primary, fails, and cools it down.
	if _, err := mp.Chat(context.Background(), ChatRequest{Model: "primary"}); err != nil {
		t.Fatalf("first Chat = %v", err)
	}

	// Second turn skips it entirely — the notice must say why anyway.
	var got *FallbackNotice
	ctx := WithFallbackNotice(context.Background(), func(n FallbackNotice) { got = &n })
	if _, err := mp.Chat(ctx, ChatRequest{Model: "primary"}); err != nil {
		t.Fatalf("second Chat = %v", err)
	}
	if got == nil {
		t.Fatal("no notice fired")
	}
	if got.Reason != "cooldown" {
		t.Errorf("Reason = %q, want cooldown", got.Reason)
	}
}

func TestFallbackNoticeOnStream(t *testing.T) {
	primary := &countingProvider{
		name:     "primary",
		failWith: &FallbackError{StatusCode: 503, Message: "down", Provider: "primary"},
		failFor:  1 << 30,
	}
	backup := &countingProvider{name: "backup"}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "primary"})
	installFastClock(mp)
	mp.Register("primary", primary, "model-a", 0, true)
	mp.Register("backup", backup, "model-b", 1, true)

	var got *FallbackNotice
	ctx := WithFallbackNotice(context.Background(), func(n FallbackNotice) { got = &n })
	if _, err := mp.ChatStream(ctx, ChatRequest{Model: "primary"}); err != nil {
		t.Fatalf("ChatStream = %v", err)
	}
	if got == nil || got.To != "backup" || got.Reason != "server_error" {
		t.Errorf("notice = %+v", got)
	}
}

// A stale or never-registered default (e.g. the constructor's "openrouter" in
// a config that only registered nvidia) must not become the addressed
// provider: the notice would blame a provider that does not exist and the
// addressed-only rules would apply to nobody.
func TestUnregisteredDefaultResolvesToChainHead(t *testing.T) {
	primary := &countingProvider{
		name:     "nvidia",
		failWith: &FallbackError{StatusCode: 429, Message: "limited", Provider: "nvidia"},
		failFor:  1 << 30,
	}
	backup := &countingProvider{name: "poolside"}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "openrouter"}) // never registered
	installFastClock(mp)
	mp.Register("nvidia", primary, "model-a", 0, true)
	mp.Register("poolside", backup, "model-b", 1, true)

	var got *FallbackNotice
	ctx := WithFallbackNotice(context.Background(), func(n FallbackNotice) { got = &n })
	if _, err := mp.Chat(ctx, ChatRequest{}); err != nil {
		t.Fatalf("Chat = %v", err)
	}
	if got == nil {
		t.Fatal("no notice fired")
	}
	if got.From != "nvidia" || got.Reason != "rate_limit" {
		t.Errorf("notice blames the wrong provider: %+v", *got)
	}
}
