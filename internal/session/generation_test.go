package session

import (
	"context"
	"errors"
	"testing"
)

func newGenManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// A turn that loaded its prefix before a reset must not be able to write that
// prefix back. This is the whole point of #319: /new is routed ahead of the
// per-key turn lock, so the fence is the only thing between the cleared
// transcript and the in-flight turn.
func TestSaveRefusesAWriteSupersededByAReset(t *testing.T) {
	ctx := context.Background()
	m := newGenManager(t)

	inflight := NewSession("cli:1")
	inflight.AddMessage(Message{Role: RoleUser, Content: "first"})
	if err := m.Save(ctx, inflight); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := m.ResetConversation(ctx, "cli:1"); err != nil {
		t.Fatalf("ResetConversation: %v", err)
	}

	// The in-flight turn finishes and tries to publish its transcript.
	inflight.AddMessage(Message{Role: RoleAssistant, Content: "late reply"})
	err := m.Save(ctx, inflight)
	if !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("Save after reset = %v, want ErrSessionSuperseded", err)
	}

	after, err := m.Load(ctx, "cli:1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(after.Messages) != 0 {
		t.Fatalf("reset transcript was resurrected: %d messages", len(after.Messages))
	}
}

// The fence has to survive a round trip through the sidecar. A reset clears
// every other metadata field, so a Save that treats "no metadata" as "remove
// the sidecar" would erase the generation and let the very next stale write in.
func TestResetGenerationSurvivesReloadWithNoOtherMetadata(t *testing.T) {
	ctx := context.Background()
	m := newGenManager(t)

	sess := NewSession("cli:2")
	sess.AddMessage(Message{Role: RoleUser, Content: "hi"})
	if err := m.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.ResetConversation(ctx, "cli:2"); err != nil {
		t.Fatalf("ResetConversation: %v", err)
	}

	reloaded, err := m.Load(ctx, "cli:2")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Generation != 1 {
		t.Fatalf("Generation after reload = %d, want 1", reloaded.Generation)
	}
}

// A reset clears the transcript and the per-conversation overrides, but user
// facts are deliberately kept: /new starts a new conversation, it does not
// forget who the user is.
func TestResetClearsOverridesButKeepsConversationContext(t *testing.T) {
	ctx := context.Background()
	m := newGenManager(t)

	sess := NewSession("cli:3")
	sess.AddMessage(Message{Role: RoleUser, Content: "hi"})
	sess.ModelOverride = "openrouter/some-model"
	sess.Personality = "terse"
	sess.UpdateContext("name", "Josh")
	if err := m.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reset, err := m.ResetConversation(ctx, "cli:3")
	if err != nil {
		t.Fatalf("ResetConversation: %v", err)
	}
	if len(reset.Messages) != 0 || reset.ModelOverride != "" || reset.Personality != "" {
		t.Fatalf("reset left state behind: %+v", reset)
	}
	if got := reset.ConversationContext["name"]; got != "Josh" {
		t.Fatalf("ConversationContext[name] = %q, want Josh", got)
	}
}

// Resetting a session that was never written must work — /new as the very first
// thing a user does is routine — and must still lay down the fence.
func TestResetOnAnUnknownSessionCreatesAFencedSession(t *testing.T) {
	ctx := context.Background()
	m := newGenManager(t)

	reset, err := m.ResetConversation(ctx, "cli:4")
	if err != nil {
		t.Fatalf("ResetConversation: %v", err)
	}
	if reset.Generation != 1 {
		t.Fatalf("Generation = %d, want 1", reset.Generation)
	}
}

// The CLI-facing Reset archives the transcript rather than clearing it, so it
// needs the same fence: without it a turn still running would re-create the
// archived conversation under the live name.
func TestInventoryResetAlsoBumpsTheGeneration(t *testing.T) {
	ctx := context.Background()
	m := newGenManager(t)

	inflight := NewSession("cli:5")
	inflight.AddMessage(Message{Role: RoleUser, Content: "first"})
	if err := m.Save(ctx, inflight); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.Reset(ctx, "cli:5"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	inflight.AddMessage(Message{Role: RoleAssistant, Content: "late reply"})
	if err := m.Save(ctx, inflight); !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("Save after archive-reset = %v, want ErrSessionSuperseded", err)
	}
}

// A session that has never been reset carries generation 0 and must not gain a
// sidecar it did not have. The sidecar is what `sessions list` and the metadata
// reload read; creating one for every plain save is a behaviour change, and a
// stray file is how "no metadata" starts meaning something.
func TestOrdinarySaveWritesNoSidecar(t *testing.T) {
	ctx := context.Background()
	m := newGenManager(t)

	sess := NewSession("cli:6")
	sess.AddMessage(Message{Role: RoleUser, Content: "hi"})
	if err := m.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := m.readGenerationLocked("cli:6"); got != 0 {
		t.Fatalf("generation for an unreset session = %d, want 0", got)
	}
}
