package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/cron"
)

func newCronTool(t *testing.T) (*CronTool, *cron.Service, *bus.MessageBus) {
	t.Helper()
	b := bus.NewMessageBus()
	svc := cron.NewService(b, t.TempDir())
	return NewCronTool(svc, "cli"), svc, b
}

func TestCronTool_Name(t *testing.T) {
	t.Parallel()
	tool, _, _ := newCronTool(t)
	if tool.Name() != "cron" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "cron")
	}
}

// The skill tells the agent to use these actions; the enum must offer them.
func TestCronTool_AdvertisesCreateListDelete(t *testing.T) {
	t.Parallel()
	tool, _, _ := newCronTool(t)

	var action *Parameter
	for i, p := range tool.Parameters() {
		if p.Name == "action" {
			action = &tool.Parameters()[i]
		}
	}
	if action == nil {
		t.Fatal("cron tool has no 'action' parameter")
	}
	if !action.Required {
		t.Error("'action' must be required")
	}
	for _, want := range []string{"create", "list", "delete"} {
		found := false
		for _, e := range action.Enum {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("action enum missing %q; got %v", want, action.Enum)
		}
	}
}

func TestCronTool_CreateSchedulesAndPersists(t *testing.T) {
	t.Parallel()
	tool, svc, _ := newCronTool(t)

	res := tool.Execute(context.Background(), map[string]any{
		"action":   "create",
		"schedule": "1h",
		"message":  "stand up",
	})
	if res.Error != nil {
		t.Fatalf("create failed: %v", res.Error)
	}

	jobs := svc.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Schedule != "delay:1h" {
		t.Errorf("schedule = %q, want %q", jobs[0].Schedule, "delay:1h")
	}
	if jobs[0].Content != "stand up" {
		t.Errorf("content = %q", jobs[0].Content)
	}
	// The agent needs the ID back in order to delete the job later.
	if !strings.Contains(res.Output, jobs[0].ID) {
		t.Errorf("create output %q does not contain job ID %q", res.Output, jobs[0].ID)
	}
}

func TestCronTool_CreateRepeatingUsesEvery(t *testing.T) {
	t.Parallel()
	tool, svc, _ := newCronTool(t)

	res := tool.Execute(context.Background(), map[string]any{
		"action":   "create",
		"schedule": "30m",
		"message":  "drink water",
		"repeat":   true,
	})
	if res.Error != nil {
		t.Fatalf("create failed: %v", res.Error)
	}
	if got := svc.ListJobs()[0].Schedule; got != "every:30m" {
		t.Errorf("schedule = %q, want %q", got, "every:30m")
	}
}

func TestCronTool_CreateDefaultsToDefaultChannel(t *testing.T) {
	t.Parallel()
	tool, svc, _ := newCronTool(t)

	tool.Execute(context.Background(), map[string]any{
		"action": "create", "schedule": "1h", "message": "x",
	})
	if got := svc.ListJobs()[0].Channel; got != "cli" {
		t.Errorf("channel = %q, want the tool's default %q", got, "cli")
	}
}

func TestCronTool_CreateHonoursExplicitChannel(t *testing.T) {
	t.Parallel()
	tool, svc, _ := newCronTool(t)

	tool.Execute(context.Background(), map[string]any{
		"action": "create", "schedule": "1h", "message": "x", "channel": "telegram",
	})
	if got := svc.ListJobs()[0].Channel; got != "telegram" {
		t.Errorf("channel = %q, want %q", got, "telegram")
	}
}

func TestCronTool_CreateAssignsUniqueIDs(t *testing.T) {
	t.Parallel()
	tool, svc, _ := newCronTool(t)

	for i := 0; i < 5; i++ {
		res := tool.Execute(context.Background(), map[string]any{
			"action": "create", "schedule": "1h", "message": "x",
		})
		if res.Error != nil {
			t.Fatalf("create %d failed: %v", i, res.Error)
		}
	}
	if got := len(svc.ListJobs()); got != 5 {
		t.Errorf("expected 5 distinct jobs, got %d — IDs are colliding", got)
	}
}

func TestCronTool_CreateRejectsBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing schedule", map[string]any{"action": "create", "message": "x"}},
		{"missing message", map[string]any{"action": "create", "schedule": "1h"}},
		{"cron expression", map[string]any{"action": "create", "schedule": "0 9 * * *", "message": "x"}},
		{"unparseable", map[string]any{"action": "create", "schedule": "soon", "message": "x"}},
		{"negative", map[string]any{"action": "create", "schedule": "-5m", "message": "x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, svc, _ := newCronTool(t)
			res := tool.Execute(context.Background(), tc.args)
			if res.Error == nil {
				t.Errorf("expected an error, got output %q", res.Output)
			}
			if len(svc.ListJobs()) != 0 {
				t.Error("a rejected job was still scheduled")
			}
		})
	}
}

// A 5-field cron expression is what the old skill taught. The error must say
// what to use instead, or the agent will just retry the same thing.
func TestCronTool_CronExpressionErrorIsActionable(t *testing.T) {
	t.Parallel()
	tool, _, _ := newCronTool(t)
	res := tool.Execute(context.Background(), map[string]any{
		"action": "create", "schedule": "0 9 * * *", "message": "x",
	})
	if res.Error == nil {
		t.Fatal("expected an error")
	}
	msg := res.Error.Error()
	if !strings.Contains(msg, "30m") && !strings.Contains(msg, "duration") {
		t.Errorf("error %q does not tell the agent the accepted format", msg)
	}
}

func TestCronTool_List(t *testing.T) {
	t.Parallel()
	tool, _, _ := newCronTool(t)

	res := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if res.Error != nil {
		t.Fatalf("list failed: %v", res.Error)
	}
	if !strings.Contains(strings.ToLower(res.Output), "no scheduled") {
		t.Errorf("empty list output = %q, want a clear empty message", res.Output)
	}

	tool.Execute(context.Background(), map[string]any{
		"action": "create", "schedule": "2h", "message": "call mum", "repeat": true,
	})
	res = tool.Execute(context.Background(), map[string]any{"action": "list"})
	if res.Error != nil {
		t.Fatalf("list failed: %v", res.Error)
	}
	for _, want := range []string{"call mum", "every:2h"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("list output %q missing %q", res.Output, want)
		}
	}
}

func TestCronTool_Delete(t *testing.T) {
	t.Parallel()
	tool, svc, _ := newCronTool(t)

	tool.Execute(context.Background(), map[string]any{
		"action": "create", "schedule": "1h", "message": "x",
	})
	id := svc.ListJobs()[0].ID

	res := tool.Execute(context.Background(), map[string]any{"action": "delete", "job_id": id})
	if res.Error != nil {
		t.Fatalf("delete failed: %v", res.Error)
	}
	if len(svc.ListJobs()) != 0 {
		t.Error("job survived delete")
	}
}

func TestCronTool_DeleteRequiresAndValidatesID(t *testing.T) {
	t.Parallel()
	tool, _, _ := newCronTool(t)

	if res := tool.Execute(context.Background(), map[string]any{"action": "delete"}); res.Error == nil {
		t.Error("delete without job_id should error")
	}
	if res := tool.Execute(context.Background(), map[string]any{"action": "delete", "job_id": "nope"}); res.Error == nil {
		t.Error("delete of an unknown job_id should error")
	}
}

func TestCronTool_UnknownAction(t *testing.T) {
	t.Parallel()
	tool, _, _ := newCronTool(t)
	res := tool.Execute(context.Background(), map[string]any{"action": "explode"})
	if res.Error == nil {
		t.Error("unknown action should error")
	}
}

func TestCronTool_NilServiceDoesNotPanic(t *testing.T) {
	t.Parallel()
	tool := NewCronTool(nil, "cli")
	res := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if res.Error == nil {
		t.Error("a tool with no scheduler should report that, not claim success")
	}
}

// End-to-end: a job created through the tool actually reaches the bus.
func TestCronTool_CreatedJobFires(t *testing.T) {
	t.Parallel()
	b := bus.NewMessageBus()
	b.Start()
	svc := cron.NewService(b, t.TempDir())
	svc.Start()
	defer svc.Stop()

	tool := NewCronTool(svc, "test")

	got := make(chan string, 1)
	b.Subscribe("test", func(ctx context.Context, msg bus.InboundMessage) {
		got <- msg.Content
	})

	res := tool.Execute(context.Background(), map[string]any{
		"action": "create", "schedule": "50ms", "message": "ping",
	})
	if res.Error != nil {
		t.Fatalf("create failed: %v", res.Error)
	}

	select {
	case v := <-got:
		if v != "ping" {
			t.Errorf("delivered %q, want %q", v, "ping")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job created via the tool never fired")
	}
}

// Jobs must survive a restart, which is the whole point of persisting them.
func TestCronTool_JobSurvivesServiceRestart(t *testing.T) {
	t.Parallel()
	b := bus.NewMessageBus()
	ws := t.TempDir()

	svc := cron.NewService(b, ws)
	tool := NewCronTool(svc, "cli")
	tool.Execute(context.Background(), map[string]any{
		"action": "create", "schedule": "24h", "message": "daily", "repeat": true,
	})

	revived := cron.NewService(b, ws)
	if err := revived.Load(); err != nil {
		t.Fatalf("Load error: %v", err)
	}
	jobs := revived.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected the job to survive restart, got %d jobs", len(jobs))
	}
	if jobs[0].Content != "daily" || jobs[0].Schedule != "every:24h" {
		t.Errorf("job came back altered: %+v", jobs[0])
	}
}

// The tool must actually reach the agent — registration is the step that was
// missing and produced issue #90.
func TestRegistryWithDefaults_RegistersCronWhenServiceGiven(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	svc := cron.NewService(bus.NewMessageBus(), ws)

	reg := RegistryWithDefaults(ws, true, 30, 30, nil, nil, nil, nil, WithCronService(svc, "cli"))
	if _, ok := reg.Get("cron"); !ok {
		t.Fatalf("cron tool not registered; registered tools: %v", reg.List())
	}
}

// Without a scheduler there is nothing to deliver a reminder, so the tool must
// not be advertised at all.
func TestRegistryWithDefaults_OmitsCronWithoutService(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	reg := RegistryWithDefaults(ws, true, 30, 30, nil, nil, nil, nil)
	if _, ok := reg.Get("cron"); ok {
		t.Error("cron tool registered with no scheduler behind it")
	}
}
