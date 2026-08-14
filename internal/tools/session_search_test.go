package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/session"
)

func searchToolFixture(t *testing.T) *SessionSearchTool {
	t.Helper()
	mgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sess, err := mgr.GetOrCreate(ctx, "cli:cli_user")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddMessage(session.Message{Role: session.RoleUser, Content: "deploy the banana service", Timestamp: time.Now()})
	sess.AddMessage(session.Message{Role: session.RoleAssistant, Content: "api_key = sk-verysecretvalue123", Timestamp: time.Now()})
	if err := mgr.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	return NewSessionSearchTool(mgr)
}

func TestSessionSearchToolFindsAndFormats(t *testing.T) {
	tool := searchToolFixture(t)
	res := tool.Execute(context.Background(), map[string]any{"query": "banana", "max_results": float64(5)})
	if res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	for _, want := range []string{"1 match(es)", "cli:cli_user", "banana service"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q:\n%s", want, res.Output)
		}
	}
}

func TestSessionSearchToolNoMatchAndValidation(t *testing.T) {
	tool := searchToolFixture(t)
	res := tool.Execute(context.Background(), map[string]any{"query": "durian"})
	if res.Error != nil || !strings.Contains(res.Output, "No past messages match") {
		t.Errorf("no-match = %+v", res)
	}
	if res := tool.Execute(context.Background(), map[string]any{}); res.Error == nil {
		t.Error("missing query must error")
	}
}

// Session files are stored unredacted; the tool's output re-enters a live
// conversation and must not resurface a captured credential.
func TestSessionSearchToolRedactsOutput(t *testing.T) {
	tool := searchToolFixture(t)
	res := tool.Execute(context.Background(), map[string]any{"query": "api_key"})
	if res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if strings.Contains(res.Output, "sk-verysecretvalue123") {
		t.Errorf("credential resurfaced: %s", res.Output)
	}
	if !strings.Contains(res.Output, "api_key") {
		t.Errorf("match itself lost: %s", res.Output)
	}
}
