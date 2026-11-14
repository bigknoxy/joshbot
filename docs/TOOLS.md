# Tool error codes

Tool failures carry a stable, machine-readable code alongside the
human-readable message. The message is written for the model and may change
at any time; the codes are the interface. Scripts and callers that need to
branch on *why* a tool failed — denied versus timed out versus not found —
match on the code, never on the prose.

## Reading a code

`ToolResult.Error` is a plain `error` whose text is unchanged from before
codes existed. A code rides inside it:

```go
res := tool.Execute(ctx, args)
if res.Error != nil {
    switch tools.ErrorCode(res.Error) {
    case tools.CodeDeny:            // refused by policy or by a human
    case tools.CodeApprovalTimeout: // the approval prompt expired unanswered
    case tools.CodeTimeout:         // cut off by a deadline
    case tools.CodeNotFound:        // the named thing does not exist
    default:                        // "" — unclassified, see below
    }
}
```

`tools.ErrorCode` reads through `%w` wrapping, so a code set inside a tool
survives every layer above it — including `Registry.Execute`'s
`tool execution failed: …` wrapper. Existing `errors.Is`/`errors.As` checks
are unaffected: the code wrapper preserves the wrapped chain, and the prose
is byte-for-byte what the path produced before.

## The codes

| Code                | Meaning                                                              |
|---------------------|----------------------------------------------------------------------|
| `deny`              | Refused by policy or by a human: the shell deny list, the shell allowlist, or an approval gate that answered no — including the automatic refusal an unattended turn (cron, heartbeat) gets from `DenyAll`. |
| `approval_timeout`  | An approval prompt expired unanswered: nobody said no, the wait ran out. A cancelled prompt (Stop button, Ctrl-C) is `deny`, not a timeout — the wait was ended, not expired. |
| `timeout`           | Work cut off by a deadline, such as a shell command past its timeout budget. |
| `not_found`         | A named thing does not exist: a file or directory handed to the filesystem tool, a working directory a shell command was pointed at, or a tool name missing from the registry. |

### Where each code is set today

- `deny` — shell deny-list refusals, shell allowlist refusals, and approval
  refusals from both gated tools (`shell`, `send_file`), synchronous and
  async paths.
- `approval_timeout` — the approval gates of `shell` and `send_file`, when
  the approver reports a deadline expiry.
- `timeout` — the shell tool's command deadline
  (`command timed out after …`).
- `not_found` — filesystem `read_file`/`edit_file`/`list_dir` on a missing
  path, a missing shell `working_dir`, and an unknown tool name in the
  registry.

## Contract

- Codes are **additive and stable**: new codes may be added; an existing
  code never changes meaning.
- The human-readable text is **never** rewritten to embed a code, and no
  existing message changed when a code was introduced. A code change that
  alters prose is a prose change and is reviewed as one.
- An error that carries no code reads as `""`. Empty means *unclassified*,
  not *success* — `res.Error != nil` remains the only success test. Permission
  failures, send failures, argument validation and web backend failures are
  unclassified today; they may gain codes later, which is why consumers must
  treat unknown (including empty) codes as "no information", not as an error
  they know.
- A missing path is `not_found`; an existing path that cannot be accessed is
  not — `EACCES` is a different fact and stays unclassified rather than
  being misreported.
