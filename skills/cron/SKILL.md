---
name: cron
description: "How to schedule reminders and recurring tasks"
always: false
tags: [scheduling, automation]
---

# Scheduling & Reminders

Use the `cron` tool to schedule tasks and reminders.

## Creating Reminders

### One-time delay
```
cron create --name "Meeting reminder" --schedule "30m" --message "Your meeting starts in 30 minutes!"
```

Delay formats: `5m` (minutes), `2h` (hours), `1d` (days)

### Recurring (cron expression)
```
cron create --name "Daily standup" --schedule "0 9 * * *" --message "Time for standup!"
```

Common cron expressions:
- `*/5 * * * *` - Every 5 minutes
- `0 * * * *` - Every hour
- `0 9 * * *` - Daily at 9 AM
- `0 9 * * 1-5` - Weekdays at 9 AM
- `0 0 * * 0` - Weekly on Sunday

## Managing Tasks
- `cron list` - View all scheduled tasks
- `cron delete --job_id "..."` - Remove a task

## Best Practices
1. Name tasks descriptively so they're easy to identify
2. Always specify the target channel for message delivery
3. Use reasonable intervals (not every minute unless necessary)
4. Clean up tasks that are no longer needed
