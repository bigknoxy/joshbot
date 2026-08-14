package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/session"
)

func newTestSessions(t *testing.T) *session.Manager {
	t.Helper()
	mgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func addTurn(t *testing.T, mgr *session.Manager, id, user, assistant string) {
	t.Helper()
	ctx := context.Background()
	sess, err := mgr.GetOrCreate(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	sess.AddMessage(session.Message{Role: session.RoleUser, Content: user, Timestamp: time.Now()})
	sess.AddMessage(session.Message{Role: session.RoleAssistant, Content: assistant, Timestamp: time.Now()})
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
}

// --continue resolves to the most recently updated session, whatever channel
// it came from.
func TestLatestSessionPicksMostRecent(t *testing.T) {
	mgr := newTestSessions(t)
	addTurn(t, mgr, "cli:cli_user", "old question", "old answer")
	time.Sleep(20 * time.Millisecond) // mtime granularity
	addTurn(t, mgr, "telegram:12345", "newer question", "newer answer")

	info, err := latestSession(context.Background(), mgr)
	if err != nil {
		t.Fatalf("latestSession: %v", err)
	}
	if info.ID != "telegram:12345" {
		t.Errorf("latestSession = %q, want the newer telegram session", info.ID)
	}
}

func TestLatestSessionEmptyDirErrors(t *testing.T) {
	mgr := newTestSessions(t)
	if _, err := latestSession(context.Background(), mgr); err == nil {
		t.Fatal("no sessions must be an error, not a silent fresh session")
	}
}

// The interactive recap shows the last exchange and is silent for a fresh
// session — a banner about nothing is noise.
func TestPrintSessionRecap(t *testing.T) {
	mgr := newTestSessions(t)
	var out bytes.Buffer
	printSessionRecap(context.Background(), mgr, &out)
	if out.Len() != 0 {
		t.Errorf("fresh session should print nothing, got %q", out.String())
	}

	addTurn(t, mgr, "cli:cli_user", "what is the plan for today", "Ship phase three.")
	out.Reset()
	printSessionRecap(context.Background(), mgr, &out)
	s := out.String()
	for _, want := range []string{"Continuing conversation", "what is the plan for today", "Ship phase three.", "/new"} {
		if !strings.Contains(s, want) {
			t.Errorf("recap missing %q:\n%s", want, s)
		}
	}
}

func TestTruncateLineFlattensAndCaps(t *testing.T) {
	got := truncateLine("a\nb\t c   d", 100)
	if got != "a b c d" {
		t.Errorf("flatten = %q", got)
	}
	long := strings.Repeat("x", 150)
	if got := truncateLine(long, 100); len([]rune(got)) != 101 || !strings.HasSuffix(got, "…") {
		t.Errorf("cap = %q (len %d)", got, len([]rune(got)))
	}
}

func TestHumanizeSince(t *testing.T) {
	now := time.Now()
	cases := []struct {
		age  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, tc := range cases {
		if got := humanizeSince(now.Add(-tc.age)); got != tc.want {
			t.Errorf("humanizeSince(-%v) = %q, want %q", tc.age, got, tc.want)
		}
	}
}

func TestSplitCommaList(t *testing.T) {
	if got := splitCommaList(" a, b ,,c "); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("splitCommaList = %v", got)
	}
	if got := splitCommaList(""); got != nil {
		t.Errorf("empty should be nil, got %v", got)
	}
}
