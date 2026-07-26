package tools

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bigknoxy/joshbot/internal/cron"
)

// CronTool exposes scheduled reminders (create, list, delete) to the agent.
//
// Schedules are durations, not 5-field cron expressions: the underlying
// scheduler runs a job either once after a delay or repeatedly on an interval.
type CronTool struct {
	svc *cron.Service
	// defaultChannel receives a reminder when the caller does not name one.
	defaultChannel string
}

// NewCronTool creates a tool that wraps a cron.Service.
func NewCronTool(svc *cron.Service, defaultChannel string) *CronTool {
	if defaultChannel == "" {
		defaultChannel = "cli"
	}
	return &CronTool{svc: svc, defaultChannel: defaultChannel}
}

func (t *CronTool) Name() string { return "cron" }

func (t *CronTool) Description() string {
	return "cron: schedule a reminder to be delivered later (create), see what is scheduled (list), or cancel one (delete). " +
		"Schedules are durations such as 30m, 2h or 1d — not cron expressions."
}

func (t *CronTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "action",
			Type:        ParamString,
			Description: "Action: create, list, delete",
			Required:    true,
			Enum:        []string{"create", "list", "delete"},
		},
		{
			Name:        "schedule",
			Type:        ParamString,
			Description: "How long from now, as a duration: 30m, 2h, 1d, 1h30m (required: create)",
		},
		{
			Name:        "message",
			Type:        ParamString,
			Description: "The reminder text to deliver when the job fires (required: create)",
		},
		{
			Name:        "repeat",
			Type:        ParamBoolean,
			Description: "If true, repeat every interval instead of firing once. Default false.",
			Default:     false,
		},
		{
			Name:        "channel",
			Type:        ParamString,
			Description: "Channel to deliver to (e.g. cli, telegram). Defaults to the current channel.",
		},
		{
			Name:        "job_id",
			Type:        ParamString,
			Description: "ID of the job to cancel, as shown by list (required: delete)",
		},
	}
}

func (t *CronTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	if t.svc == nil {
		return ToolResult{Error: fmt.Errorf("scheduler not available")}
	}

	action, _ := args["action"].(string)
	switch action {
	case "create":
		return t.create(args)
	case "list":
		return t.list()
	case "delete":
		return t.delete(args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown action: %q (expected create, list, or delete)", action)}
	}
}

func (t *CronTool) create(args map[string]any) ToolResult {
	schedule := strings.TrimSpace(stringArg(args, "schedule"))
	if schedule == "" {
		return ToolResult{Error: fmt.Errorf("schedule is required (a duration such as 30m, 2h or 1d)")}
	}
	message := strings.TrimSpace(stringArg(args, "message"))
	if message == "" {
		return ToolResult{Error: fmt.Errorf("message is required (the reminder text to deliver)")}
	}

	d, err := cron.ParseDuration(schedule)
	if err != nil {
		return ToolResult{Error: fmt.Errorf(
			"could not read schedule %q: give a duration such as 30m, 2h, 1d or 1h30m "+
				"(cron expressions like \"0 9 * * *\" are not supported)", schedule)}
	}
	if d <= 0 {
		return ToolResult{Error: fmt.Errorf("schedule %q must be a positive duration such as 30m", schedule)}
	}

	kind := cron.KindDelay
	if b, ok := args["repeat"].(bool); ok && b {
		kind = cron.KindEvery
	}

	channel := strings.TrimSpace(stringArg(args, "channel"))
	if channel == "" {
		channel = t.defaultChannel
	}

	job := cron.Job{
		ID:       newJobID(),
		Schedule: kind + ":" + schedule,
		Channel:  channel,
		Content:  message,
	}
	if err := t.svc.AddJob(job); err != nil {
		return ToolResult{Error: fmt.Errorf("could not schedule reminder: %w", err)}
	}

	when := "once in " + d.String()
	if kind == cron.KindEvery {
		when = "every " + d.String()
	}
	return ToolResult{Output: fmt.Sprintf(
		"Scheduled %s, delivered to %s. Job ID: %s", when, channel, job.ID)}
}

func (t *CronTool) list() ToolResult {
	jobs := t.svc.ListJobs()
	if len(jobs) == 0 {
		return ToolResult{Output: "No scheduled reminders."}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d scheduled reminder(s):\n", len(jobs))
	for _, j := range jobs {
		fmt.Fprintf(&b, "- [%s] %s → %s: %s\n", j.ID, j.Schedule, j.Channel, j.Content)
	}
	return ToolResult{Output: strings.TrimRight(b.String(), "\n")}
}

func (t *CronTool) delete(args map[string]any) ToolResult {
	id := strings.TrimSpace(stringArg(args, "job_id"))
	if id == "" {
		return ToolResult{Error: fmt.Errorf("job_id is required (run the list action to see IDs)")}
	}
	if err := t.svc.DeleteJob(id); err != nil {
		return ToolResult{Error: err}
	}
	return ToolResult{Output: fmt.Sprintf("Cancelled reminder %s.", id)}
}

// stringArg reads a string argument, tolerating a missing or non-string value.
func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// jobSeq disambiguates IDs created within the same nanosecond, which two
// back-to-back tool calls can manage on a coarse clock.
var jobSeq atomic.Uint64

// newJobID returns a unique, human-referenceable job ID. The agent has to quote
// it back to delete a job, so it stays short.
func newJobID() string {
	return fmt.Sprintf("job-%d-%d", time.Now().Unix(), jobSeq.Add(1))
}
