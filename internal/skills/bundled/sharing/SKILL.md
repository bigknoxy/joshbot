---
name: sharing
description: "How to send a workspace file to the user as a real attachment"
always: false
tags: [files, channels]
---

# Sharing files

Use the `send_file` tool to deliver a file from the workspace to the chat as a
native attachment. Prefer it over pasting a file's contents into your reply
whenever the user wants the file itself — an image, a report, a log, an export.

```json
{"path": "reports/q3.pdf", "caption": "Here is the Q3 report"}
```

## What it does

- The path must resolve inside the workspace. A path outside it, or one that
  reaches outside through a symlink, is refused and nothing is sent.
- The file's **content** decides how it arrives: images are sent inline as
  photos, everything else as a downloadable document. The filename never
  decides — a text file named `.png` arrives as a document.
- `caption` is optional. A file with no caption sends fine; do not invent one.
- `channel` defaults to the channel you are talking on.

## Limits

Oversize files are refused with an error naming the limit, rather than being
truncated. If a file is too large, say so and offer to send a summary or a
smaller extract instead.

Channels with no attachment support receive the caption plus the file's path,
so the user is told where the file is rather than being told it was sent.
