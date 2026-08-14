package session

import (
	"context"
	"strings"
	"testing"
	"time"
)

func searchFixture(t *testing.T) *Manager {
	t.Helper()
	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	add := func(id string, role Role, content string, at time.Time) {
		sess, err := mgr.GetOrCreate(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		sess.AddMessage(Message{Role: role, Content: content, Timestamp: at})
		if err := mgr.Save(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	add("cli:cli_user", RoleUser, "let's plan the banana deployment", base)
	add("cli:cli_user", RoleAssistant, "The BANANA deployment is scheduled for Friday.", base.Add(time.Minute))
	add("telegram:12345", RoleUser, "remind me about the apple harvest", base.Add(2*time.Minute))
	return mgr
}

func TestSearchFindsAcrossSessionsCaseInsensitive(t *testing.T) {
	mgr := searchFixture(t)
	matches, err := mgr.Search(context.Background(), "banana", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2 (case-insensitive)", len(matches))
	}
	// Newest first.
	if !matches[0].Timestamp.After(matches[1].Timestamp) {
		t.Errorf("matches not newest-first: %v then %v", matches[0].Timestamp, matches[1].Timestamp)
	}
	if matches[0].Role != RoleAssistant || !strings.Contains(matches[0].Snippet, "BANANA deployment") {
		t.Errorf("match[0] = %+v", matches[0])
	}
}

func TestSearchLimitAndNoMatch(t *testing.T) {
	mgr := searchFixture(t)
	matches, err := mgr.Search(context.Background(), "banana", 1)
	if err != nil || len(matches) != 1 {
		t.Fatalf("limit ignored: %d matches, err=%v", len(matches), err)
	}
	matches, err = mgr.Search(context.Background(), "durian", 10)
	if err != nil || len(matches) != 0 {
		t.Fatalf("phantom matches: %v, err=%v", matches, err)
	}
	if m, err := mgr.Search(context.Background(), "   ", 10); err != nil || m != nil {
		t.Fatalf("blank query must return nothing, got %v, %v", m, err)
	}
}

func TestSearchSkipsSidecars(t *testing.T) {
	mgr := searchFixture(t)
	ctx := context.Background()
	// The compaction archive also ends in .jsonl; a search over it would
	// resurrect messages compaction removed and report a phantom session.
	if err := mgr.Archive(ctx, "cli:cli_user", []Message{{Role: RoleUser, Content: "archived banana", Timestamp: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	matches, err := mgr.Search(ctx, "archived banana", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("search read a sidecar: %+v", matches)
	}
}

func TestSnippetWindowsAroundTheHit(t *testing.T) {
	long := strings.Repeat("pad ", 100) + "NEEDLE here" + strings.Repeat(" tail", 100)
	s := snippetAround(long, "needle")
	if !strings.Contains(s, "NEEDLE") {
		t.Fatalf("snippet lost the hit: %q", s)
	}
	if len([]rune(s)) > maxSnippetRunes+2 {
		t.Errorf("snippet too long: %d runes", len([]rune(s)))
	}
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Errorf("snippet should mark truncation on both ends: %q", s)
	}
}
