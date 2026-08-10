# Security Policy

## Supported Versions

joshbot is a single-maintainer project and fixes land on the latest release
only. Pinning version numbers here is what let this section go stale by more
than a dozen releases, so it now states the policy rather than a snapshot.

| Version | Supported |
| ------- | --------- |
| [Latest release](https://github.com/bigknoxy/joshbot/releases/latest) | :white_check_mark: |
| Anything earlier | :x: |

If you are running an older build, upgrade before reporting — the issue may
already be fixed.

## Reporting a Vulnerability

We take the security of joshbot seriously. If you believe you have found a security vulnerability, please report it to us as described below.

**Please do NOT report security vulnerabilities through public GitHub issues.**

Instead, please report them via:

1. **Email**: Send a detailed description to [security@joshbot.dev](mailto:security@joshbot.dev) (or open a GitHub Security Advisory if available)
2. **GitHub**: Use the [Private Vulnerability Reporting](https://github.com/bigknoxy/joshbot/security/advisories/new) feature

### What to Include

Please include the following information in your report:

- Description of the vulnerability
- Steps to reproduce the issue
- Potential impact assessment
- Suggested fix (if you have one)

### What to Expect

- **Acknowledgment**: We will acknowledge receipt of your vulnerability report within 48 hours
- **Updates**: We will keep you informed of our progress
- **Resolution**: We aim to resolve critical vulnerabilities within 30 days
- **Credit**: We will credit you in the release notes (unless you prefer to remain anonymous)

### Security Best Practices

When running joshbot:

- **API Keys**: Never commit API keys or tokens to version control. Use environment variables or the config file — `~/.joshbot/config.json` is written `0600` automatically
- **Shell Tool**: The shell tool executes commands with the same permissions as the joshbot process by default. Run joshbot with the minimum required privileges, and consider enabling `tools.shell_sandbox` (see below) for real containment
- **Telegram Bot Token**: Keep your Telegram bot token secure. If compromised, regenerate it via @BotFather immediately
- **Telegram allowlist**: Set `channels.telegram.allow_from` to your numeric Telegram user ID before exposing the bot. An empty allowlist now denies every sender (fail-closed) — the bot logs a loud warning at startup and rejects all messages until you populate it. Do not leave it unset: whoever can message the bot otherwise reaches an agent loop holding the `shell` tool
- **Discord bot token / allowlist**: Keep your Discord bot token secure; if compromised, regenerate it in the Discord developer portal immediately. Set `channels.discord.allow_from` to your numeric Discord user ID (a snowflake) before exposing the bot. Like Telegram, the Discord allowlist is enforced deny-by-default: an empty or unset list rejects every sender, logs an actionable startup warning, and admits no one until you populate it
- **Memory Files**: `MEMORY.md` and `HISTORY.md` may contain sensitive information. Ensure `~/.joshbot/` directory permissions are restrictive
- **Workspace Skills**: A `SKILL.md` becomes part of the agent's standing instructions, so treat one you didn't write yourself as untrusted input until you've read it and run `joshbot skills trust <name>`
- **MCP servers**: An MCP server you add under `mcp.servers` runs as a child process with your permissions and can return arbitrary tool output the agent will act on. Add only servers you trust, exactly as you would trust a dependency you install — read the security note below before enabling one
- **Updates**: Keep joshbot updated to the latest version to receive security patches

### Security Architecture

- **Output redaction**: everything joshbot logs or prints goes through `internal/redact` first — see the Redaction section below for exactly what is and is not covered. joshbot never logs its own configured credentials directly, but a tool result can carry one it was never meant to see (the model runs `cat config.yml` and the output is a debug log line), so the log writer redacts rather than relying on that.
- **Credentials out of the config file**: a provider or model entry may set `api_key_env` naming an environment variable instead of `api_key` holding the secret, so a config file that is backed up, synced or attached to a bug report carries a variable name. Setting both on one entry is a startup error rather than a silent precedence choice, and `joshbot preflight` reports which source supplied a credential without printing it.
- **File permissions**: Config (`config.json`), auth tokens, the skills trust store, session files (`~/.joshbot/sessions/*.jsonl`, their `.meta.json` sidecars, the `.history.jsonl` compaction archive and any `.jsonl.corrupt` quarantine) and the log file are all written `0600`, and every directory joshbot creates under `~/.joshbot/` is `0700` — re-applied on startup, so an installation made before this was tightened is corrected rather than left as it was. The directory mode matters on its own: a session file is named `<channel>:<senderID>`, so a listable `sessions/` directory discloses who talks to the bot even though the transcripts themselves are unreadable. Session and log files hold full conversation content and tool output, so they are treated as sensitive as the credential store.
- **Input validation**: All tool inputs are validated before execution
- **The shell allowlist screens the whole command, not its first word**: while `tools.shell_allow_list` is in force — set explicitly, or defaulted on a platform with no sandbox — a command containing any construct that can introduce a second command word (`;`, `&`, `|`, a newline, a backtick, `$(`, `<(`, `>(`, a carriage return) **or that redirects I/O** (`>`, `<`, and by substring every form built on them: `>>`, `2>`, `&>`, `<<`, `<>`) is refused outright. The command is handed to `sh -c` unchanged, so matching only the first word admitted `echo hi; id` — and, until redirection was screened, `echo PWNED > /outside/escaped.txt`, which wrote outside the workspace as the user running joshbot with no sandbox involved. This deliberately refuses harmless pipelines and redirects too: on the platforms where this list is the *only* boundary, a check that has to parse shell grammar correctly to hold is not a boundary. The default list also no longer contains `find`, `go`, `git`, `rg` or `sort` — each launches a program of the caller's choosing (`find -exec sh -c`, `go run`, `git -c core.pager=...`, `rg --pre PROGRAM` run once per file, `sort --compress-program=PROGRAM` exec'd when the sort spills to disk). Screening flag *values* was rejected as the fix: it has to be exhaustive to hold, and removal is shaped like the property being enforced. An operator who needs them can name them explicitly, having chosen to.
- **Command screening is defence in depth, not a boundary**: the shell tool's deny list closes known-dangerous command shapes, but no deny list can be sound against an adversarial model or a prompt-injected instruction (an interpreter can be made to spawn a shell, a script can be written then run). The only hard boundary is the OS-level sandbox below.
- **Shell sandbox (opt-in): what is enforced per platform.** Setting `tools.shell_sandbox: "workspace"` confines shell commands to the workspace and toolchain caches, with `$HOME` (SSH keys, cloud credentials, joshbot's own config) unreachable and outbound TCP denied unless `tools.shell_sandbox_allow_network` is `true`. It is off by default and fails closed — an unrecognized value, or a host/kernel that cannot enforce the sandbox, is a startup error rather than a silent no-op. The mechanism differs by platform, and joshbot never claims a boundary it did not apply:
  - **Linux** — enforced via **Landlock** (kernel LSM). Requires a kernel that lists `landlock` in its active LSMs; otherwise enabling the sandbox is a startup error.
  - **macOS** — enforced via **Seatbelt** (`/usr/bin/sandbox-exec`), a deny-by-default profile mirroring the Landlock policy. The command is wrapped in `sandbox-exec`; there is no re-exec helper. `TMPDIR` is pointed at a private scratch dir inside the workspace, so the shared system temp is not exposed.
  - **Other platforms (Windows, BSD, etc.)** — **no OS-level sandbox is available.** `tools.shell_sandbox: "workspace"` is a startup error there. Instead, on these platforms the shell tool **defaults to allowlist-only**: if the operator has not set `tools.shell_allow_list`, only a small set of non-escaping read/inspect commands is permitted, because the deny list alone is bypassable. Set `tools.shell_allow_list` to widen it.
  - **The Seatbelt profile is passed to `sandbox-exec` with `-p`**, so the full policy text — including the absolute workspace path — appears in the process argv and is readable by any local user via `ps`. The policy itself grants nothing to whoever reads it, but treat the workspace location as disclosed to local users on a shared machine.
  - **`mach-lookup` in the macOS profile is an allowlist of named services** (`macMachServices` in `internal/tools/sandbox_darwin.go`), not a blanket grant. An unrestricted `mach-lookup` reaches XPC services that run *outside* the profile and will act on the sandboxed process's behalf, which would hollow out the file and network rules above. Adding a service name to that list is a security decision and must be justified in place.
- **Environment isolation**: shell commands no longer inherit joshbot's own process environment. They receive an allowlisted subset (PATH, common toolchain variables) with anything credential-shaped — API keys, tokens, secrets — stripped, so a spawned command cannot read joshbot's own provider credentials via `env`.
- **Telegram allowlist fails closed**: `channels.telegram.allow_from` is enforced deny-by-default. An empty or unset allowlist rejects every sender rather than admitting everyone, and the channel logs an actionable warning at startup naming the config key to set. This is a deliberate breaking change from the previous allow-all-when-empty behavior. Matching is partitioned by entry shape, exactly as on Discord: an all-digits entry matches **only** the numeric user ID, a non-numeric entry **only** the username or full name. Matching every entry against every field let a stranger set their free-form Telegram first name to the operator's numeric user ID and authenticate as them. Use the numeric ID.
- **Discord allowlist fails closed**: `channels.discord.allow_from` follows the same deny-by-default rule as Telegram. An empty or unset allowlist rejects every sender, the channel logs an actionable startup warning naming the config key, and matching is partitioned by entry shape: an all-digits entry matches **only** the numeric user ID, a non-numeric entry matches **only** username or global display name. Matching every entry against every field let a stranger set their free-form global display name to the operator's snowflake and authenticate as them, since global names are not unique. Use the numeric ID — a username can be changed at any time. The channel also ignores messages authored by bots (including itself) so it never enters a self-reply loop.
- **Skill approval**: a `SKILL.md` found in the workspace — including one the agent creates for itself via `skill_registry` — is inert until approved via `joshbot skills trust`. Approval is bound to a SHA-256 digest of the **entire skill directory tree** (every regular file's relative path and content, walked in sorted order for determinism), not just `SKILL.md`. So editing, adding, or removing any file in an approved skill directory — including a sibling script the `SKILL.md` tells the agent to run — revokes trust. Symlinks are folded in by name and raw target and are never dereferenced, so adding, repointing or removing a symlink inside an approved skill revokes trust too. A trust store written by an older joshbot (which hashed only `SKILL.md`) will no longer match the new digest, which safely revokes affected skills rather than crashing; re-inspect and re-trust them. Skills bundled with the release are exempt.
- **Filesystem tool containment is symlink-resolved**: when `restrict_to_workspace` is set, the `filesystem` tool resolves symlinks (`filepath.EvalSymlinks`) before the workspace-containment check and operates on the resolved path, so a symlink that is lexically inside the workspace but points outside it (`ln -s /etc ws/link`) is rejected rather than followed. For a not-yet-existing file the nearest existing parent is resolved, so creating a new file inside the workspace still works while a symlinked ancestor is still caught. Resolving each component — including ones being created — matters: `EvalSymlinks` reports a symlink whose *target* does not exist with the same ENOENT as a plain missing file, so a dangling `ws/evil -> /outside/x` used to be re-appended lexically, pass containment, and then be written. Resolving and then operating on the same resolved path narrows the check/use (TOCTOU) gap but does not close it, and `O_NOFOLLOW` on the open closes only part of what remains: it constrains the **last** component only, while the kernel still resolves every intermediate one through symlinks. So replacing `ws/sub` with a symlink to `/outside` after the check redirected the write to `/outside/file.txt`, and the read paths (`read_file`, `edit_file`'s pre-read, `grep`'s per-file read) used plain `os.ReadFile` with no `O_NOFOLLOW` at all. Every path the tool opens now goes through a single containment helper (`internal/tools/openat.go`) built on Go 1.24's `os.Root`: the containment root is held open as a descriptor and each component below it is resolved with `openat(2)` relative to the previous one, so a component that has become a symlink out of the root fails rather than being followed, and directory creation uses the same walk instead of `os.MkdirAll`. The root itself is still opened by name, because it is operator configuration and a workspace that legitimately lives behind a symlink (macOS `/tmp` → `/private/tmp`) has to keep working.

- **MCP client trust model**: joshbot can spawn Model Context Protocol servers over stdio (`mcp.servers` in `config.json`) and expose their tools to the agent. This is a new untrusted-code path — a server is an arbitrary executable that runs with joshbot's permissions and whose tool results feed straight into the agent loop. Several controls bound it:
  - **`config.json` is the trust boundary.** Servers are declared only in `~/.joshbot/config.json`, which lives *outside* the workspace, is written `0600`, and cannot be modified by a workspace-confined `filesystem`/`shell` tool. So a prompt-injected agent cannot add or edit an MCP server for itself the way it could write a workspace `SKILL.md` — declaring a server is an operator-only act, comparable to installing a dependency. This is the same "approval lives outside the workspace" posture as skill trust, achieved through file location rather than a separate trust file.
  - **Inert until enabled.** Each server carries `"enabled": true`, mirroring the provider convention. A half-configured or commented-out entry never spawns a process.
  - **Namespaced tools — no shadowing.** Every discovered tool is registered as `mcp__<server>__<tool>`. Built-in tools (`shell`, `filesystem`, …) never carry that prefix and the registry refuses duplicate names, so a malicious or careless server cannot register a `shell` tool that shadows the built-in. The worst a bad server name can do is collide with another MCP server, which is surfaced as a warning, not a silent override.
  - **Lifecycle containment.** Servers are spawned lazily, every call is bounded by a context timeout (a hung server fails the single call, it does not hang the agent), and each process is closed by closing its stdin, waiting a short grace period, then killing and reaping it — so no zombie or leaked goroutine survives shutdown. MCP is fail-soft: a server that will not start or list tools is logged and skipped, never breaking the built-in tools or the agent.
  - **Sanitized child environment.** An MCP server is spawned with the same allowlisted, credential-screened environment as a shell child (`internal/childenv.Sanitized`), so it cannot read joshbot's provider API keys out of `env`.
  - **Bounded output.** A tool result is truncated at 4,000 characters, the same convention as the `shell` and `filesystem` tools, and a single server message over 4 MiB kills the connection — a server cannot exhaust the agent's context or joshbot's heap by replying with an unbounded blob.
  - **Filesystem access is not yet sandboxed.** MCP servers are *not* confined by `tools.shell_sandbox`. The environment is screened, but the filesystem is not — treat an MCP server as trusted code. Sandboxing MCP child processes is future work.

### Redaction

`internal/redact` strips credentials and host-identifying paths from text joshbot
displays, logs or exports. The threat is not an attacker reading files — file
permissions cover that — it is the ordinary act of pasting a log or a status
dump into a bug report.

**Where it is applied**

| Surface | Redacted |
|---|---|
| Log output (stdout and the log file, every level) | Yes |
| `joshbot status` | Yes |
| Session files on disk (`*.jsonl`, `.meta.json`, `.history.jsonl`) | **No — deliberately** |
| Requests sent to the model provider | **No — the model needs the conversation** |

Session content is stored verbatim. Rewriting it on save would mangle
legitimate text (a user discussing a token format) and would add a second way
for a session file to be corrupted. The protection for data at rest is the
`0600` file mode above, not redaction.

**What is detected**

- Vendor key shapes: Anthropic (`sk-ant-`), OpenAI and compatible (`sk-`),
  OpenRouter (`sk-or-`), GitHub (`ghp_`/`gho_`/`ghu_`/`ghs_`/`github_pat_`),
  Slack (`xox*-`), Google (`AIza`), NVIDIA (`nvapi-`), Groq (`gsk_`) and AWS
  access key IDs (`AKIA`/`ASIA`).
- `Authorization: Bearer` and `Authorization: Basic` header values.
- Any value assigned to a credential-shaped name in JSON, YAML or an
  environment assignment — `api_key`, `api-key`, `token`, `secret`, `password`,
  `credential`, `private_key` and the rest of `redact.SecretNameFragments`.
  That list is shared with the shell-environment screen, so the two cannot
  drift apart.
- The host home directory, replaced with `~`.

Values are replaced with a fixed `[REDACTED]`, never a length-preserving mask —
a mask leaks the length of the secret.

**What is deliberately not detected**

Bare high-entropy strings. Hashes, base64 payloads, UUIDs and git SHAs are
indistinguishable from secrets by entropy alone, and redacting them would make
ordinary output unreadable — which is how redaction ends up being switched off.
Redaction is a safety net over the file-permission and environment-isolation
controls above, not a substitute for them: treat it as reducing accidental
disclosure, not as a guarantee that any given output is secret-free.

### Tool-Specific Security Notes

- **`shell` tool**: Screens commands against a deny list (defence in depth, not a boundary — see above) and, when `restrict_to_workspace` is set, confines the working directory. Enable `tools.shell_sandbox` for actual OS-level containment.
- **`memory_search` tool**: Can read all stored facts, including potentially sensitive information in MEMORY.md. Access control is file-permission based — ensure `~/.joshbot/` directory permissions are restrictive.
- **`skill_registry` tool**: Can create, list, and delete skills (SKILL.md files). Skill creation writes files under `~/.joshbot/workspace/skills/`, but a newly created skill does not take effect until an operator approves it with `joshbot skills trust` — see Skill Approval above. Treat skill changes as configuration changes — review skill content before approving.
- **`web_fetch` / `web_search` tools**: Refuse to connect to localhost, private IP ranges, and cloud metadata hosts, enforced at connection time (not just URL string matching) to resist DNS-rebinding-style bypasses.
