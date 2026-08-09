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
- **Memory Files**: `MEMORY.md` and `HISTORY.md` may contain sensitive information. Ensure `~/.joshbot/` directory permissions are restrictive
- **Workspace Skills**: A `SKILL.md` becomes part of the agent's standing instructions, so treat one you didn't write yourself as untrusted input until you've read it and run `joshbot skills trust <name>`
- **Updates**: Keep joshbot updated to the latest version to receive security patches

### Security Architecture

- **Output redaction**: everything joshbot logs or prints goes through `internal/redact` first — see the Redaction section below for exactly what is and is not covered. joshbot never logs its own configured credentials directly, but a tool result can carry one it was never meant to see (the model runs `cat config.yml` and the output is a debug log line), so the log writer redacts rather than relying on that.
- **File permissions**: Config (`config.json`), auth tokens, the skills trust store, session files (`~/.joshbot/sessions/*.jsonl`, their `.meta.json` sidecars, the `.history.jsonl` compaction archive and any `.jsonl.corrupt` quarantine) and the log file are all written `0600`, with their directories `0750`. Session and log files hold full conversation content and tool output, so they are treated as sensitive as the credential store.
- **Input validation**: All tool inputs are validated before execution
- **Command screening is defence in depth, not a boundary**: the shell tool's deny list closes known-dangerous command shapes, but no deny list can be sound against an adversarial model or a prompt-injected instruction. The only hard boundary is the OS-level sandbox below.
- **Shell sandbox (opt-in, Linux only)**: setting `tools.shell_sandbox: "workspace"` confines shell commands via Landlock — filesystem access limited to the workspace and toolchain caches, outbound TCP denied unless `tools.shell_sandbox_allow_network` is `true`. It's off by default (existing behavior is unchanged unless you opt in), and it fails closed: an unrecognized value, a non-Linux host, or a kernel without Landlock support is a startup error rather than a silent no-op.
- **Environment isolation**: shell commands no longer inherit joshbot's own process environment. They receive an allowlisted subset (PATH, common toolchain variables) with anything credential-shaped — API keys, tokens, secrets — stripped, so a spawned command cannot read joshbot's own provider credentials via `env`.
- **Skill approval**: a `SKILL.md` found in the workspace — including one the agent creates for itself via `skill_registry` — is inert until approved via `joshbot skills trust`. Approval is bound to the file's SHA-256, so editing an approved skill revokes it. Skills bundled with the release are exempt.

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
