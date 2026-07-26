---
name: cron
description: "How to schedule reminders and recurring tasks"
always: false
tags: [scheduling, automation]
---

# Scheduling & Reminders

Use the `cron` tool to schedule a message to be delivered back to the user later.

When a job fires, its message is delivered to the chosen channel as if the user
had sent it — so write the message as the reminder text itself.

## Creating a reminder

Schedules are **durations**, not cron expressions. Accepted units are `s`, `m`,
`h` and `d`, and they can be combined (`1h30m`).

One-time, the default:

```json
{"action": "create", "schedule": "30m", "message": "Your meeting starts in 30 minutes"}
```

Recurring — set `repeat` to true, and `schedule` becomes the interval:

```json
{"action": "create", "schedule": "24h", "message": "Time for standup", "repeat": true}
```

Delivery goes to the current channel unless you name one:

```json
{"action": "create", "schedule": "2h", "message": "Take a break", "channel": "telegram"}
```

`create` returns the job ID. Keep it if the user might want to cancel.

## Managing reminders

```json
{"action": "list"}
{"action": "delete", "job_id": "job-1753142400-3"}
```

`list` shows each job's ID, schedule, target channel and message. Run it first
when the user asks to cancel something but does not know the ID.

## Limits worth knowing

- **No calendar scheduling.** "Every weekday at 9am" cannot be expressed. The
  closest is `{"repeat": true, "schedule": "24h"}`, which fires 24 hours after
  it is created and every 24 hours after that. Say so rather than implying the
  reminder is pinned to a clock time.
- One-time reminders are removed once they fire. Recurring ones run until
  deleted.
- Reminders are persisted, so they survive a restart — but the countdown
  **restarts with them**. A "remind me in 30 minutes" that is 29 minutes in when
  joshbot restarts will fire 30 minutes after the restart, not one minute later.
  For anything time-critical, tell the user this rather than letting them assume
  the original moment is kept.

## Best practices

1. Confirm what you scheduled, in plain terms, including when it will fire.
2. Prefer the user's own wording for the message — they are the one who reads it.
3. Use sensible intervals; a recurring job every few minutes becomes noise.
4. Offer to clean up recurring reminders that are no longer wanted.
