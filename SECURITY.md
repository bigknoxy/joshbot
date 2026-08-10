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
- **File permissions**: Config (`config.json`), auth tokens, the skills trust store, session files (`~/.joshbot/sessions/*.jsonl`, their `.meta.json` sidecars, the `.history.jsonl` compaction archive and any `.jsonl.corrupt` quarantine) and the log file are all written `0600`, with their directories `0750`. Session and log files hold full conversation content and tool output, so they are treated as sensitive as the credential store.
- **Input validation**: All tool inputs are validated before execution
- **Command screening is defence in depth, not a boundary**: the shell tool's deny list closes known-dangerous command shapes, but no deny list can be sound against an adversarial model or a prompt-injected instruction (an interpreter can be made to spawn a shell, a script can be written then run). The only hard boundary is the OS-level sandbox below.
- **Shell sandbox (opt-in): what is enforced per platform.** Setting `tools.shell_sandbox: "workspace"` confines shell commands to the workspace and toolchain caches, with `$HOME` (SSH keys, cloud credentials, joshbot's own config) unreachable and outbound TCP denied unless `tools.shell_sandbox_allow_network` is `true`. It is off by default and fails closed — an unrecognized value, or a host/kernel that cannot enforce the sandbox, is a startup error rather than a silent no-op. The mechanism differs by platform, and joshbot never claims a boundary it did not apply:
  - **Linux** — enforced via **Landlock** (kernel LSM). Requires a kernel that lists `landlock` in its active LSMs; otherwise enabling the sandbox is a startup error.
  - **macOS** — enforced via **Seatbelt** (`/usr/bin/sandbox-exec`), a deny-by-default profile mirroring the Landlock policy. The command is wrapped in `sandbox-exec`; there is no re-exec helper. `TMPDIR` is pointed at a private scratch dir inside the workspace, so the shared system temp is not exposed.
  - **Other platforms (Windows, BSD, etc.)** — **no OS-level sandbox is available.** `tools.shell_sandbox: "workspace"` is a startup error there. Instead, on these platforms the shell tool **defaults to allowlist-only**: if the operator has not set `tools.shell_allow_list`, only a small set of non-escaping read/inspect commands is permitted, because the deny list alone is bypassable. Set `tools.shell_allow_list` to widen it.
- **Environment isolation**: shell commands no longer inherit joshbot's own process environment. They receive an allowlisted subset (PATH, common toolchain variables) with anything credential-shaped — API keys, tokens, secrets — stripped, so a spawned command cannot read joshbot's own provider credentials via `env`.
- **Telegram allowlist fails closed**: `channels.telegram.allow_from` is enforced deny-by-default. An empty or unset allowlist rejects every sender rather than admitting everyone, and the channel logs an actionable warning at startup naming the config key to set. This is a deliberate breaking change from the previous allow-all-when-empty behavior.
- **Discord allowlist fails closed**: `channels.discord.allow_from` follows the same deny-by-default rule as Telegram. An empty or unset allowlist rejects every sender, the channel logs an actionable startup warning naming the config key, and matching is by numeric Discord user ID (stable), username, or global display name. The channel also ignores messages authored by bots (including itself) so it never enters a self-reply loop.
- **Skill approval**: a `SKILL.md` found in the workspace — including one the agent creates for itself via `skill_registry` — is inert until approved via `joshbot skills trust`. Approval is bound to a SHA-256 digest of the **entire skill directory tree** (every regular file's relative path and content, walked in sorted order for determinism), not just `SKILL.md`. So editing, adding, or removing any file in an approved skill directory — including a sibling script the `SKILL.md` tells the agent to run — revokes trust. A trust store written by an older joshbot (which hashed only `SKILL.md`) will no longer match the new digest, which safely revokes affected skills rather than crashing; re-inspect and re-trust them. Skills bundled with the release are exempt.
- **Filesystem tool containment is symlink-resolved**: when `restrict_to_workspace` is set, the `filesystem` tool resolves symlinks (`filepath.EvalSymlinks`) before the workspace-containment check and operates on the resolved path, so a symlink that is lexically inside the workspace but points outside it (`ln -s /etc ws/link`) is rejected rather than followed. For a not-yet-existing file the nearest existing parent is resolved, so creating a new file inside the workspace still works while a symlinked ancestor is still caught. Resolving and then operating on the same resolved path closes the check/use (TOCTOU) gap.

- **MCP client trust model**: joshbot can spawn Model Context Protocol servers over stdio (`mcp.servers` in `config.json`) and expose their tools to the agent. This is a new untrusted-code path — a server is an arbitrary executable that runs with joshbot's permissions and whose tool results feed straight into the agent loop. Three controls bound it:
  - **`config.json` is the trust boundary.** Servers are declared only in `~/.joshbot/config.json`, which lives *outside* the workspace, is written `0600`, and cannot be modified by a workspace-confined `filesystem`/`shell` tool. So a prompt-injected agent cannot add or edit an MCP server for itself the way it could write a workspace `SKILL.md` — declaring a server is an operator-only act, comparable to installing a dependency. This is the same "approval lives outside the workspace" posture as skill trust, achieved through file location rather than a separate trust file.
  - **Inert until enabled.** Each server carries `"enabled": true`, mirroring the provider convention. A half-configured or commented-out entry never spawns a process.
  - **Namespaced tools — no shadowing.** Every discovered tool is registered as `mcp__<server>__<tool>`. Built-in tools (`shell`, `filesystem`, …) never carry that prefix and the registry refuses duplicate names, so a malicious or careless server cannot register a `shell` tool that shadows the built-in. The worst a bad server name can do is collide with another MCP server, which is surfaced as a warning, not a silent override.
  - **Lifecycle containment.** Servers are spawned lazily, every call is bounded by a context timeout (a hung server fails the single call, it does not hang the agent), and each process is closed by closing its stdin, waiting a short grace period, then killing and reaping it — so no zombie or leaked goroutine survives shutdown. MCP is fail-soft: a server that will not start or list tools is logged and skipped, never breaking the built-in tools or the agent.
  - **Not yet sandboxed.** MCP servers are *not* confined by `tools.shell_sandbox`; they run with joshbot's full environment and filesystem access. Treat an MCP server as fully trusted code. Sandboxing MCP child processes is future work.

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
